// Phase 1 of the roadmap: the same Phase 0 contract (ChatInput/ChatRequest)
// is now also served in SSE streaming mode, token by token, backed by a real
// LLM (or the canned stub if ANTHROPIC_API_KEY is not set — see internal/llm).
// The Phase 0 stub endpoint (POST /v1/tenant/{tenant_id}/user/{user_id}/chat)
// remains intact.
//
// Phase 2 follow-up "C": this is the streaming path that actually reads and
// writes the BadgerDB working-memory tier (internal/memory). Before calling
// the LLM, the session's condensed Summary plus its Recent messages are
// folded into the system prompt so the model sees prior turns; the incoming
// user message is appended immediately, and the assistant's full reply is
// appended only after the stream completes successfully (Done, not Err or
// ctx cancellation) — a partial or failed turn is never persisted (see
// docs/architecture/memory-tiers.md).
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"

	"github.com/Luum-Home/aisotropy/internal/bitrate"
	"github.com/Luum-Home/aisotropy/internal/costmeter"
	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/gateway"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/semantic"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// recentMessagesWindow bounds how many recent active-buffer messages are
// folded into the system prompt alongside the condensed summary.
const recentMessagesWindow = 12

// factsWindow bounds how many facts-memory engrams are injected into the
// system prompt (docs say the top 3-4 by score; see rqlite-facts-memory.md).
const factsWindow = 4

// semanticWindow bounds how many semantic-memory fragments are injected into
// the system prompt (the vector tier's RAG contribution; see
// qdrant-semantic-memory.md).
const semanticWindow = 4

// semanticCandidatePoolFactor over-fetches ANN candidates before a configured
// semantic.Reranker reorders and trims them to semanticWindow. Reranking only
// adds value when it has more candidates than the injection window to choose
// from, so when a reranker is present each scoped search fetches
// semanticWindow*factor points; without a reranker the pre-existing behavior
// (fetch exactly semanticWindow, order by score) is preserved. Mirrors the
// facts tier's candidatePoolFactor.
const semanticCandidatePoolFactor = 4

// costInputPricePer1M / costOutputPricePer1M are the default USD prices per
// 1M tokens used to estimate per-tenant cost for the /costs view. Rough
// defaults; a real deployment sets a per-model price table.
const (
	costInputPricePer1M  = 0.15
	costOutputPricePer1M = 0.60
)

// outboxRef derives a stable per-turn reference from the session and prompt.
// It is the outbox event id (and, downstream, the seed of the semantic point's
// UUIDv5), so re-sending the identical prompt in a session consolidates onto
// the same fact/vector rather than duplicating.
func outboxRef(key memory.Key, prompt string) string {
	h := sha256.Sum256([]byte(key.Tenant + "\x00" + key.User + "\x00" + key.Session + "\x00" + prompt))
	return hex.EncodeToString(h[:12])
}

// StreamTokenEvent is the payload of each "token" SSE event: an incremental
// fragment (delta) of the LLM response.
type StreamTokenEvent struct {
	Text string `json:"text" doc:"Incremental fragment of the response"`
}

// StreamDoneEvent is the payload of the final "done" SSE event: the stream
// finished normally (not due to client cancellation or an error).
type StreamDoneEvent struct {
	TenantID string `json:"tenant_id" doc:"Tenant that served the request"`
}

// StreamErrorEvent is the payload of the "error" SSE event: the LLM returned
// an error before completing the stream.
type StreamErrorEvent struct {
	Message string `json:"message" doc:"Error description"`
}

// sseEventToken / sseEventDone name the SSE event kinds registered below.
const (
	sseEventToken = "token"
	sseEventDone  = "done"
)

// RegisterStream mounts the chat endpoint in SSE streaming mode. It receives
// the already-built llm.Client (llm.FromEnv() in main.go), the shared
// *memory.WorkingMemory, and the facts-memory Store + Extractor (Phase 3)
// instead of creating them here — no global state, consistent with isolation
// via ctx (see docs/vision/principles.md).
//
// Deliberately reuses ChatInput/ChatRequest from chat.go: the same struct
// governs the contract both in the stub endpoint (Phase 0) and in real
// streaming (Phase 1+) — see "the struct as the single source of truth"
// (docs/vision/thesis.md).
//
// persona supplies the base system prompt per product_id (Phase 8 adoption
// seam); nil selects the built-in default persona. inPricePer1M/outPricePer1M
// are the USD prices per 1M tokens used for the /costs estimate; a value <= 0
// falls back to the package default (costInputPricePer1M/costOutputPricePer1M).
//
// bptSampler (Bet 6, bits-per-token-per-tenant) reserves a deterministic ~5% of
// turns as held-out samples for offline surprisal measurement (internal/bitrate);
// nil disables sampling. A reserved turn is EXCLUDED from that turn's
// facts/semantic consolidation so a later lift measurement is not spurious —
// scoring text already distilled into memory would trivially predict it
// (leakage). This is the request-side half of the anti-leakage contract; the
// other half is bitrate.ShouldSample.
//
// gw is the model-selection gateway seam (internal/gateway): when non-nil the
// per-turn client is chosen by its routing policy (selectClient) instead of the
// passed client; the quota-pressure degrade (llm.DegradeAt) below still applies
// as an OVERRIDE on top of that pick. A nil gw resolves to the passed client,
// preserving pre-gateway behavior. guard bundles the input/output guardrails:
// the user prompt is redacted before it reaches memory or the model (secrets
// never persist or reach the model), a suspicious injection verdict is
// annotated (or rejected when BlockInjection is enabled), and the ASSEMBLED
// final response is inspected before it reaches the client. Output handling has
// two honest modes, keyed on Guard.BlockOutput: when OFF (default) the response
// is streamed live token by token and the assembled text is inspected
// annotate-only afterward (annotateOutput) — no latency change; when ON the
// whole response is BUFFERED (tokens accumulated WITHOUT flushing), inspected
// via checkOutput, and only then emitted — either the safe text, or a safe
// refusal if it tripped a guardrail, so unsafe output is never sent. A nil guard
// disables guardrails entirely.
//
//nolint:gocognit,cyclop,funlen // large cohesive request handler; refactor tracked in docs/architecture/phases-3-7-as-built.md debt ledger
func RegisterStream(api huma.API, client llm.Client, wm *memory.WorkingMemory, store facts.Store, limiter *memory.Limiter, toolReg *tools.Registry, meter *costmeter.Meter, cheapClient llm.Client, semStore semantic.Store, embedder semantic.Embedder, reranker semantic.Reranker, persona PersonaProvider, inPricePer1M, outPricePer1M float64, bptSampler bitrate.SampleRecorder, gw *gateway.Gateway, guard *Guard) {
	sse.Register(api, huma.Operation{
		OperationID: "chat-stream",
		Method:      httpMethodPost,
		Path:        "/v1/tenant/{tenant_id}/user/{user_id}/chat/stream",
		Summary:     "Chat with the agent via SSE streaming (Phase 1+)",
		Description: "Streams the LLM response token by token, hydrating the " +
			"system prompt from the session's working memory (summary + " +
			"recent messages) and persisting both the user and assistant " +
			"turns once the stream completes successfully. Uses " +
			"ANTHROPIC_API_KEY if set; otherwise falls back to the canned " +
			"stub (internal/llm) so the SSE path can be verified without " +
			"credentials. Honors ctx cancellation: if the client cuts the " +
			"connection, the stream stops immediately and no partial " +
			"assistant turn is persisted.",
	}, map[string]any{
		sseEventToken: StreamTokenEvent{},
		sseEventDone:  StreamDoneEvent{},
		"error":       StreamErrorEvent{},
	}, func(ctx context.Context, in *ChatInput, send sse.Sender) {
		ctx, err := withIdentity(ctx, in.TenantID, in.UserID, in.Body.SessionID)
		if err != nil {
			_ = send.Data(StreamErrorEvent{Message: fmt.Sprintf("invalid tenant/user/session identity: %v", err)})
			return
		}
		// In addition to the api-layer ctx injection above (withIdentity,
		// consumed via TenantFromContext/UserFromContext in this package),
		// set the same identity through internal/llm's own ctx carrier so
		// the WithOTel decorator (internal/llm/otel.go) can attribute its
		// gen_ai span with tenant_id/user_id without internal/llm importing
		// internal/api (see ADR-0010's "Observability decision" and
		// internal/llm/context.go).
		ctx = llm.WithTenant(ctx, in.TenantID)
		ctx = llm.WithUser(ctx, in.UserID)
		key := memory.Key{Tenant: in.TenantID, User: in.UserID, Session: in.Body.SessionID}
		// Facts memory (Phase 3) is scoped per (tenant, user) — not per
		// session: an Engram journal persists across a user's sessions.
		// NewScope validates identity through internal/tenancy (ADR-0011).
		scope, err := facts.NewScope(in.TenantID, in.UserID) //nolint:contextcheck // facts.NewScope has no context parameter (internal/facts, out of scope for this cleanup)
		if err != nil {
			_ = send.Data(StreamErrorEvent{Message: fmt.Sprintf("invalid tenant/user identity: %v", err)})
			return
		}

		// Quota gate (Phase 7 / constitutional gate): reject BEFORE any LLM call
		// so a tenant over its rate limit never spends tokens. Per-tenant token
		// bucket in BadgerDB (internal/memory.Limiter).
		if limiter != nil {
			allowed, err := limiter.Allow(ctx, in.TenantID)
			if err != nil {
				_ = send.Data(StreamErrorEvent{Message: fmt.Sprintf("quota check failed: %v", err)})
				return
			}
			if !allowed {
				slog.WarnContext(ctx, "chat stream rate-limited", "tenant_id", in.TenantID)
				_ = send.Data(StreamErrorEvent{Message: "rate limit exceeded for tenant " + in.TenantID})
				return
			}
		}

		// Input guardrail (redact + detect ALWAYS; block opt-in). forwarded is
		// the REDACTED prompt: it — never the raw prompt — hydrates the system
		// prompt, persists to working memory (and the consolidation outbox /
		// bitrate held-out sample), and reaches the model, so a secret in the
		// prompt never leaks into the model or any memory tier. A suspicious
		// injection verdict is annotated by default and only rejected when
		// BlockInjection is enabled.
		forwarded, blocked := guard.checkInput(ctx, in.TenantID, in.UserID, in.Body.Prompt)
		if blocked {
			_ = send.Data(StreamErrorEvent{Message: "request blocked by input guardrail (possible prompt injection)"})
			return
		}

		slog.InfoContext(ctx, "chat stream request",
			"tenant_id", in.TenantID,
			"user_id", in.UserID,
			"session_id", in.Body.SessionID,
			"product_id", in.Body.ProductID,
			"prompt_len", len(in.Body.Prompt),
			"llm", client.Name(),
		)

		system, err := hydrateSystemPrompt(ctx, wm, store, semStore, embedder, reranker, persona, key, scope, in.Body.ProductID, forwarded)
		if err != nil {
			_ = send.Data(StreamErrorEvent{Message: fmt.Sprintf("failed to hydrate working memory: %v", err)})
			return
		}

		// Scope-gated tools (Phase 7): inject only the tools the caller's scopes
		// permit. A forbidden tool never appears in the payload, so the model
		// cannot know it exists (docs/architecture/granular-control.md). The
		// scopes are the AUTHORITATIVE ones (effectiveScopes): under a real
		// authenticator a client cannot escalate via body scopes; under NoAuth
		// the body scopes are the fallback.
		if toolReg != nil {
			if frag := toolReg.PromptFragment(effectiveScopes(ctx, in.Body.Scopes)); frag != "" {
				system += "\n\n" + frag
			}
		}

		// Persist the user's turn AND its consolidation intent in a single
		// BadgerDB transaction (Transactional Outbox, Phase 5): the message
		// lands in working memory and a pending event is queued for the async
		// worker to distil into the facts + semantic tiers
		// (internal/consolidation). The two writes are atomic — a crash cannot
		// keep one without the other — and this happens before the LLM call
		// because the user's message is real input that already happened.
		ref := outboxRef(key, forwarded)
		if bptSampler != nil && bitrate.ShouldSample(ref) {
			// Held-out bitrate sample (Bet 6): reserve this turn for offline
			// surprisal measurement and DO NOT enqueue it for facts/semantic
			// consolidation. The user message still enters working memory (it
			// really happened); replacing the consolidation enqueue with the
			// reservation is what keeps the scored text out of the tenant's
			// long-term memory, so the later lift is not spurious (leakage).
			if err := wm.Append(ctx, key, memory.Message{Role: roleUser, Content: forwarded}); err != nil {
				slog.ErrorContext(ctx, "failed to append held-out user message", "err", err)
			}
			if err := bptSampler.Reserve(ctx, in.TenantID, in.UserID, in.Body.SessionID, ref, forwarded); err != nil {
				slog.ErrorContext(ctx, "failed to reserve bitrate held-out sample", "err", err)
			}
		} else {
			event := memory.NewOutboxEvent(key, ref, forwarded, time.Now().UTC())
			if err := wm.AppendWithOutbox(ctx, key, memory.Message{Role: roleUser, Content: forwarded}, event); err != nil {
				slog.ErrorContext(ctx, "failed to append user message + outbox event", "err", err)
			}
		}

		// Model selection through the gateway seam first (nil gw -> the passed
		// client). This is where routing policy (e.g. cost-budget shaping) picks
		// the target.
		active := selectClient(ctx, gw, client, meter, in.TenantID, in.UserID, forwarded, in.Body.ModelPreference, len(in.Body.Images) > 0)

		// Quota-pressure degradation (Phase 7) applies as an OVERRIDE on top of
		// the gateway pick: when this tenant is near its limit, route to the
		// cheaper model instead of the gateway-chosen one — graceful degradation
		// rather than a hard 429. Requires a configured cheap model
		// (AISOTROPY_CHEAP_MODEL); otherwise cheapClient is nil and the gateway
		// pick stands.
		if cheapClient != nil && limiter != nil {
			if pressure, perr := limiter.Pressure(ctx, in.TenantID); perr == nil {
				if choice := llm.DegradeAt(pressure, llm.DefaultDegradeThreshold, active.Name(), cheapClient.Name()); choice.Degraded {
					active = cheapClient
					slog.WarnContext(ctx, "degrading to cheap model under quota pressure",
						"tenant_id", in.TenantID, "pressure", pressure, "model", cheapClient.Name())
				}
			}
		}

		tokens, err := active.StreamChat(ctx, system, forwarded)
		if err != nil {
			_ = send.Data(StreamErrorEvent{Message: fmt.Sprintf("failed to start stream: %v", err)})
			return
		}

		// Hard output-blocking mode (opt-in, AISOTROPY_GUARDRAILS_BLOCK_OUTPUT):
		// when ON, tokens are ACCUMULATED WITHOUT being flushed to the client, so
		// the assembled response can be inspected before a single byte is emitted.
		// Tradeoff: buffering the full response sacrifices token-streaming latency
		// (TTFT) for the guarantee that unsafe (toxic/leaky) output is never sent.
		// When OFF (default), tokens flush live as they arrive (no latency change)
		// and the assembled text is inspected annotate-only afterward — a streamed
		// response cannot be un-sent.
		blocking := guard.outputBlockingEnabled()

		var assistantReply strings.Builder
		for {
			select {
			case <-ctx.Done():
				// The client closed the connection (or the request expired).
				// There is nothing else to do here: the `tokens` channel is
				// closed by its own producer upon observing the same
				// ctx.Done() (see internal/llm/stub.go and anthropic.go) —
				// token spend is cut there, not here. The partial
				// assistant reply accumulated so far is deliberately NOT
				// persisted: a cancelled/failed turn is not a completed one.
				slog.InfoContext(ctx, "chat stream cancelled", "tenant_id", in.TenantID, "user_id", in.UserID)
				return
			case tok, ok := <-tokens:
				if !ok {
					return
				}
				if tok.Err != nil {
					// Mid-stream failure: surface it as an SSE error event and stop,
					// so the client never mistakes a truncated stream for success.
					// The partial reply is not persisted (see above). In blocking
					// mode nothing has been flushed, so no toxic partial escaped.
					_ = send.Data(StreamErrorEvent{Message: tok.Err.Error()})
					return
				}
				if tok.Done {
					// Only a clean, complete stream gets its assistant turn
					// persisted to working memory.
					if blocking {
						// Buffered path: nothing has been flushed yet. Inspect the
						// assembled text via checkOutput, which returns it unchanged
						// when safe or the safe refusal when it tripped a guardrail.
						// ONLY that (safe) text is emitted and persisted — the
						// toxic/leaky text never reaches the wire nor memory,
						// mirroring the non-streaming route.
						if assistantReply.Len() > 0 {
							safe := guard.checkOutput(ctx, in.TenantID, in.UserID, forwarded, assistantReply.String(), system)
							if err := wm.Append(ctx, key, memory.Message{Role: roleAssistant, Content: safe}); err != nil {
								slog.ErrorContext(ctx, "failed to append assistant message to working memory", "err", err)
							}
							if err := send.Data(StreamTokenEvent{Text: safe}); err != nil {
								// Client disconnected before the buffered reply
								// could be emitted; stop without persisting cost.
								return
							}
						}
					} else {
						if assistantReply.Len() > 0 {
							if err := wm.Append(ctx, key, memory.Message{Role: roleAssistant, Content: assistantReply.String()}); err != nil {
								slog.ErrorContext(ctx, "failed to append assistant message to working memory", "err", err)
							}
							// Output guardrail on the ASSEMBLED final text
							// (annotate-only: the stream was already sent token by
							// token, so a refusal can't replace it here — that is
							// exactly what blocking mode above trades latency for).
							guard.annotateOutput(ctx, in.TenantID, in.UserID, forwarded, assistantReply.String(), system)
						}
					}
					// Per-tenant cost accounting (Phase 7 observability): record the
					// real provider token usage carried on the terminal token, so
					// the /costs view can show cost per token per tenant.
					if meter != nil && tok.Usage != nil {
						inP, outP := costInputPricePer1M, costOutputPricePer1M
						if inPricePer1M > 0 {
							inP = inPricePer1M
						}
						if outPricePer1M > 0 {
							outP = outPricePer1M
						}
						cost := costmeter.EstimateCostUSD(tok.Usage.InputTokens, tok.Usage.OutputTokens, inP, outP)
						meter.Record(in.TenantID, tok.Usage.InputTokens, tok.Usage.OutputTokens, cost)
					}
					_ = send.Data(StreamDoneEvent{TenantID: in.TenantID})
					return
				}
				assistantReply.WriteString(tok.Text)
				if !blocking {
					// Live flush. In blocking mode this is deliberately skipped:
					// the token is buffered above and emitted (or refused) only
					// after the full response has been inspected.
					if err := send.Data(StreamTokenEvent{Text: tok.Text}); err != nil {
						// Client disconnected mid-write: there is no way to keep
						// writing, so we stop. The partial reply is not persisted.
						return
					}
				}
			}
		}
	})
}

// hydrateSystemPrompt builds the system prompt for the given product_id and
// folds in the session's working-memory context (the condensed summary plus
// its most recent active-buffer messages), so the model sees prior turns of
// the conversation instead of starting fresh on every request (see
// docs/architecture/memory-tiers.md).
//
//nolint:gocognit,cyclop // large cohesive request handler; refactor tracked in docs/architecture/phases-3-7-as-built.md debt ledger
func hydrateSystemPrompt(ctx context.Context, wm *memory.WorkingMemory, store facts.Store, semStore semantic.Store, embedder semantic.Embedder, reranker semantic.Reranker, persona PersonaProvider, key memory.Key, scope facts.Scope, productID, prompt string) (string, error) {
	if persona == nil {
		persona = defaultPersonaProvider{}
	}
	base, err := persona.BasePrompt(ctx, productID)
	if err != nil {
		return "", fmt.Errorf("persona base prompt: %w", err)
	}

	// Working memory (Phase 2): the live session buffer. Optional — a nil wm
	// yields no summary/recent. The Bet 6 bpt scorer passes nil here on purpose:
	// it measures CONSOLIDATED memory (facts + semantic) vs the bare base, never
	// the live buffer, which would still contain the held-out turn being scored
	// (leakage). Real request handlers always pass a non-nil wm.
	var summary string
	var recent []memory.Message
	if wm != nil {
		summary, err = wm.Summary(ctx, key)
		if err != nil {
			return "", fmt.Errorf("read working-memory summary: %w", err)
		}
		recent, err = wm.Recent(ctx, key, recentMessagesWindow)
		if err != nil {
			return "", fmt.Errorf("read working-memory recent messages: %w", err)
		}
	}

	// Facts memory (Phase 3): the top engrams for this user whose content
	// matches the incoming prompt (FTS5, ranked by Relevance×Importance×
	// Recency). Unlike working memory, a facts retrieval failure is NOT fatal
	// — facts are an enrichment, not a hard dependency — so it degrades to
	// "no facts" with a log line rather than failing the request.
	var relevant []facts.ScoredFact
	if store != nil {
		relevant, err = store.Search(ctx, scope, prompt, factsWindow)
		if err != nil {
			slog.ErrorContext(ctx, "facts search failed; proceeding without facts", "err", err)
			relevant = nil
		}
	}

	// Semantic memory (Phase 4 + ADR-0019 tenant-shared merge): see
	// searchSemanticContext's doc for the full rationale (kind filtering,
	// non-fatal degradation, the tenant-shared merge, optional reranking).
	semantics := searchSemanticContext(ctx, semStore, embedder, reranker, scope, prompt)

	if summary == "" && len(recent) == 0 && len(relevant) == 0 && len(semantics) == 0 {
		return base, nil
	}

	var sb strings.Builder
	sb.WriteString(base)
	if len(relevant) > 0 {
		sb.WriteString("\n\nKnown facts about this user (long-term memory):\n")
		for _, f := range relevant {
			fmt.Fprintf(&sb, "- %s\n", f.Content)
		}
	}
	if len(semantics) > 0 {
		sb.WriteString("\n\nRelevant prior context (semantic memory):\n")
		for _, p := range semantics {
			fmt.Fprintf(&sb, "- %s\n", p.Content)
		}
	}
	if summary != "" {
		sb.WriteString("\n\nSummary of the conversation so far: ")
		sb.WriteString(summary)
	}
	if len(recent) > 0 {
		sb.WriteString("\n\nMost recent messages in this session:\n")
		for _, m := range recent {
			fmt.Fprintf(&sb, "%s: %s\n", m.Role, m.Content)
		}
	}
	return sb.String(), nil
}

// searchSemanticContext returns the semantic-memory fragments to inject into
// the system prompt for scope's tenant/user against prompt: vectors
// semantically similar to prompt (RAG over what the consolidation sink
// embedded). Non-fatal enrichment throughout — an embed/search/scope failure
// degrades to "no semantic context" (or "user results only") rather than
// failing the request. ADR-0017 phase 3: the primary RAG injection targets
// only the high-value kinds (curated Document knowledge and distilled Fact
// projections); raw KindTurn vectors are excluded so they never dominate the
// injected context.
//
// ADR-0019 (tenant-wide document sharing): a SECOND, single-scope search
// against the tenant-shared bucket (semantic.NewTenantScope, KindDocument
// only) is merged in with the user's own results, client-side — never a
// widened/OR-filtered store scan. The two searches degrade independently: a
// failure on either side still lets the other contribute.
//
// ADR-0033 reranker (this wave): when reranker is non-nil, both scoped searches
// OVER-FETCH a larger candidate pool (semanticWindow*semanticCandidatePoolFactor)
// and the merged, deduped, isolation-clean pool is reordered and trimmed to
// semanticWindow by the reranker before injection — the "maintain relevance"
// step of the semantic tier (the component doc's reranking; ADR-0006). The
// reranker runs strictly AFTER the per-scope isolation filter, so it only ever
// reorders points the caller is already entitled to; it can never widen scope.
// A nil reranker preserves the pre-existing behavior exactly (fetch
// semanticWindow, order by score, no rerank).
func searchSemanticContext(ctx context.Context, semStore semantic.Store, embedder semantic.Embedder, reranker semantic.Reranker, scope facts.Scope, prompt string) []semantic.ScoredPoint {
	if semStore == nil || embedder == nil {
		return nil
	}
	vecs, eerr := embedder.Embed(ctx, []string{prompt})
	if eerr != nil || len(vecs) == 0 {
		return nil
	}
	query := vecs[0]

	// Over-fetch only when a reranker will trim the pool back down; otherwise
	// fetch exactly the injection window (unchanged legacy behavior).
	fetch := semanticWindow
	if reranker != nil {
		fetch = semanticWindow * semanticCandidatePoolFactor
	}

	var userPts, tenantPts []semantic.ScoredPoint

	if semScope, serr := semantic.NewScope(scope.Tenant(), scope.User()); serr == nil { //nolint:contextcheck // semantic.NewScope has no context parameter (internal/semantic, out of scope for this cleanup)
		if pts, serr := semStore.Search(ctx, semScope, query, fetch, semantic.KindDocument, semantic.KindFact); serr == nil {
			userPts = pts
		} else {
			slog.ErrorContext(ctx, "semantic search failed; proceeding without it", "err", serr)
		}
	} else {
		slog.ErrorContext(ctx, "semantic scope construction failed; proceeding without it", "err", serr)
	}

	if tenantScope, terr := semantic.NewTenantScope(scope.Tenant()); terr == nil { //nolint:contextcheck // semantic.NewTenantScope has no context parameter (internal/semantic, out of scope for this cleanup)
		if pts, serr := semStore.Search(ctx, tenantScope, query, fetch, semantic.KindDocument); serr == nil {
			tenantPts = pts
		} else {
			slog.ErrorContext(ctx, "tenant-shared semantic search failed; proceeding without it", "err", serr)
		}
	} else {
		slog.ErrorContext(ctx, "tenant scope construction failed; proceeding without it", "err", terr)
	}

	merged := mergeScoredPoints(fetch, userPts, tenantPts)
	if reranker != nil {
		// Rerank the isolation-clean candidate pool down to the injection window.
		return reranker.Rerank(ctx, prompt, merged, semanticWindow)
	}
	if len(merged) > semanticWindow {
		merged = merged[:semanticWindow]
	}
	return merged
}

// mergeScoredPoints merges already-scoped semantic search result sets (a
// user's own search plus the tenant-shared Document search, ADR-0019),
// dedupes by point ID (first occurrence wins — sets is ordered
// user-scope-first, so a user's own copy of a point would win over a
// same-ID tenant-shared one, though in practice PointID's scope-derived hash
// makes that collision structurally impossible), sorts by score descending,
// and truncates to limit. Two clean single-scope searches merged
// client-side — never a widened/OR-filtered store scan, per ADR-0019's
// "Alternative A rejected".
func mergeScoredPoints(limit int, sets ...[]semantic.ScoredPoint) []semantic.ScoredPoint {
	seen := make(map[string]struct{})
	var merged []semantic.ScoredPoint
	for _, set := range sets {
		for _, p := range set {
			if _, dup := seen[p.ID]; dup {
				continue
			}
			seen[p.ID] = struct{}{}
			merged = append(merged, p)
		}
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Score > merged[j].Score })
	if limit > 0 && len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}
