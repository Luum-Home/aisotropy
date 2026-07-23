package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Luum-Home/aisotropy/internal/bitrate"
	"github.com/Luum-Home/aisotropy/internal/costmeter"
	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/gateway"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/semantic"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// StreamDeps bundles the dependencies the streaming chat pipeline needs, so
// StreamCore can be driven from the SSE HTTP handler (RegisterStream) AND from
// any alternate wire encoder (the Vercel AI SDK and AG-UI protocol adapters)
// without duplicating the pipeline.
//
// The fields mirror RegisterStream's parameters one-for-one — this is
// deliberate, exactly like core.go's ChatDeps mirrors Register's: every entry
// point (aisotropy SSE, AI SDK, AG-UI) MUST run the SAME code over the SAME
// dependencies, so the only difference between them is how the assistant output
// is RE-ENCODED, never what the isolation/quota/guardrail/hydration pipeline
// does with the request. Nil semantics are identical to RegisterStream's (a nil
// Limiter/ToolReg/Persona/Meter/GW/Guard/SemStore/Embedder/Reranker/BPTSampler/
// CheapClient degrades exactly as documented on the corresponding helper), so a
// leaner wiring gets the same defined behavior the SSE path would.
//
// It is a superset of ChatDeps (core.go): the streaming path additionally wires
// the semantic RAG tier (SemStore/Embedder/Reranker), the quota-pressure cheap
// fallback (CheapClient), the bits-per-token held-out sampler (BPTSampler), and
// the per-turn cost price basis (InPricePer1M/OutPricePer1M) that the
// non-streaming route does not use.
type StreamDeps struct {
	Client        llm.Client
	WM            *memory.WorkingMemory
	Store         facts.Store
	Limiter       *memory.Limiter
	ToolReg       *tools.Registry
	Meter         *costmeter.Meter
	CheapClient   llm.Client
	SemStore      semantic.Store
	Embedder      semantic.Embedder
	Reranker      semantic.Reranker
	Persona       PersonaProvider
	InPricePer1M  float64
	OutPricePer1M float64
	BPTSampler    bitrate.SampleRecorder
	GW            *gateway.Gateway
	Guard         *Guard
}

// StreamFinal is the terminal-success payload handed to StreamSink.OnDone: the
// full assistant output actually delivered (post output-guardrail safe text),
// the tenant that served the turn, and the provider-reported token usage when
// available. A wire encoder uses it to emit its protocol's final/finish frame
// (e.g. the AI SDK's `finish` part with usage, or AG-UI's RUN_FINISHED).
type StreamFinal struct {
	// Text is the complete assembled assistant reply that was delivered —
	// in the default (non-blocking) posture this equals the concatenation of
	// every OnToken delta; in output-blocking mode it is the single safe
	// (possibly refusal-substituted) text that OnToken emitted once.
	Text string
	// TenantID is the tenant that served the request (echoed for parity with
	// the aisotropy SSE `done` event's tenant_id field).
	TenantID string
	// Usage carries the provider-reported total token counts for the completed
	// stream. Nil when the provider reported none (e.g. the canned stub, or the
	// homegrown fallback path) — an encoder must treat nil as "usage unknown".
	Usage *llm.Usage
}

// StreamSink is the wire-encoding seam: StreamCore drives the full streaming
// pipeline (identity->ctx, quota gate, input guardrail, gateway selection,
// hydration, streaming LLM, output guardrail, cost) and delivers the assistant's
// output through these callbacks INSTEAD of writing SSE, so any protocol
// (aisotropy SSE, Vercel AI SDK, AG-UI) can encode it while the
// isolation/key-brokering guarantees hold identically — one pipeline, no drift.
//
// OnToken is called once per delivered delta of assistant text, in order. It
// returns an error so the encoder can signal a failed write (client
// disconnect): on a non-nil return StreamCore aborts the turn WITHOUT emitting
// a terminal frame and WITHOUT recording cost, mirroring the aisotropy SSE
// handler's early return when a token write fails. In the default (non-blocking)
// posture OnToken fires live as tokens arrive; in output-blocking mode it fires
// exactly once, with the fully-inspected safe text, after the stream completes.
//
// OnDone is called exactly once on terminal success, AFTER the output guardrail
// and cost accounting have run. It is best-effort (its result is not observed),
// matching the aisotropy SSE handler, which ignores the `done` event's write
// result. It is NOT called when the turn errors, is cancelled, or aborts on a
// token-write failure.
//
// Pipeline errors (invalid identity, quota, blocked input, hydration failure,
// stream-start failure, mid-stream LLM error) are RETURNED from StreamCore, not
// delivered through the sink — the caller encodes them per protocol. Client
// cancellation and a clean end-of-stream both return nil with no terminal frame.
type StreamSink struct {
	OnToken func(text string) error
	OnDone  func(final StreamFinal)
}

// ErrRateLimited marks the pre-spend quota rejection (the constitutional
// reject-before-spending gate). Wire adapters that own an HTTP-status or
// typed-error vocabulary (e.g. the OpenAI-compat shim's 429/rate_limit_error)
// detect it with errors.Is; the aisotropy SSE/AI-SDK/AG-UI paths keep treating
// it as a plain error frame, unchanged.
var ErrRateLimited = errors.New("rate limit exceeded")

// StreamCore runs the full streaming chat pipeline (identity->ctx, quota gate,
// input guardrail, gateway selection + quota-pressure degrade, hydration,
// streaming LLM, output guardrail, cost) and delivers the assistant's output
// through the sink callbacks instead of writing SSE — so any wire protocol
// (aisotropy SSE, Vercel AI SDK, AG-UI) can encode it while the
// isolation/key-brokering guarantees hold identically. It is the EXACT code the
// SSE handler (RegisterStream) runs; extracting it from that closure keeps the
// codebase's core value — a single pipeline — literally single, so the SSE
// endpoint and the two protocol adapters share this body verbatim rather than
// each maintaining a copy that can drift on the constitutional gates (tenant
// isolation via ctx, quota-before-spend, input/output guardrails).
//
// Unlike ChatCore (core.go) the returned errors are PLAIN errors, not
// huma.StatusError values: the streaming protocols surface failures as their
// own error frames (an SSE `error` event, an AI SDK error part, an AG-UI
// RUN_ERROR), never as an HTTP status, so the error string is the payload and
// each caller wraps it in its own frame. A clean end-of-stream and a client
// cancellation both return nil (the sink's OnDone is called only on the former).
//
//nolint:gocognit,cyclop,funlen // one cohesive streaming pipeline extracted verbatim from RegisterStream; splitting it would re-introduce the drift risk this extraction exists to remove
func StreamCore(ctx context.Context, d StreamDeps, in *ChatInput, sink StreamSink) error {
	ctx, err := withIdentity(ctx, in.TenantID, in.UserID, in.Body.SessionID)
	if err != nil {
		return fmt.Errorf("invalid tenant/user/session identity: %w", err)
	}
	// In addition to the api-layer ctx injection above (withIdentity, consumed
	// via TenantFromContext/UserFromContext in this package), set the same
	// identity through internal/llm's own ctx carrier so the WithOTel decorator
	// (internal/llm/otel.go) can attribute its gen_ai span with tenant_id/
	// user_id without internal/llm importing internal/api (see ADR-0010's
	// "Observability decision" and internal/llm/context.go).
	ctx = llm.WithTenant(ctx, in.TenantID)
	ctx = llm.WithUser(ctx, in.UserID)
	// Boundary bridge (ADR-0068 Layer 2): drain trace/span ids and stamp the
	// request/session/identity observability metadata once, here, so every
	// downstream *Context log line auto-correlates (see o11y_bridge.go).
	ctx = stampTelemetry(ctx, in.TenantID, in.UserID, in.Body.SessionID)
	key := memory.Key{Tenant: in.TenantID, User: in.UserID, Session: in.Body.SessionID}
	// Facts memory (Phase 3) is scoped per (tenant, user) — not per session: an
	// Engram journal persists across a user's sessions. NewScope validates
	// identity through internal/tenancy (ADR-0011).
	scope, err := facts.NewScope(in.TenantID, in.UserID) //nolint:contextcheck // facts.NewScope has no context parameter (internal/facts, out of scope for this cleanup)
	if err != nil {
		return fmt.Errorf("invalid tenant/user identity: %w", err)
	}

	// Quota gate (Phase 7 / constitutional gate): reject BEFORE any LLM call so
	// a tenant over its rate limit never spends tokens. Per-tenant token bucket
	// in BadgerDB (internal/memory.Limiter).
	if d.Limiter != nil {
		allowed, aerr := d.Limiter.Allow(ctx, in.TenantID)
		if aerr != nil {
			return fmt.Errorf("quota check failed: %w", aerr)
		}
		if !allowed {
			slog.WarnContext(ctx, "chat stream rate-limited", "tenant_id", in.TenantID)
			return fmt.Errorf("%w for tenant %s", ErrRateLimited, in.TenantID)
		}
	}

	// Input guardrail (redact + detect ALWAYS; block opt-in). forwarded is the
	// REDACTED prompt: it — never the raw prompt — hydrates the system prompt,
	// persists to working memory (and the consolidation outbox / bitrate
	// held-out sample), and reaches the model, so a secret in the prompt never
	// leaks into the model or any memory tier. A suspicious injection verdict is
	// annotated by default and only rejected when BlockInjection is enabled.
	forwarded, blocked := d.Guard.checkInput(ctx, in.TenantID, in.UserID, in.Body.Prompt)
	if blocked {
		return errors.New("request blocked by input guardrail (possible prompt injection)")
	}

	slog.InfoContext(ctx, "chat stream request",
		"tenant_id", in.TenantID,
		"user_id", in.UserID,
		"session_id", in.Body.SessionID,
		"product_id", in.Body.ProductID,
		"prompt_len", len(in.Body.Prompt),
		"llm", d.Client.Name(),
	)

	system, err := hydrateSystemPrompt(ctx, d.WM, d.Store, d.SemStore, d.Embedder, d.Reranker, d.Persona, key, scope, in.Body.ProductID, forwarded)
	if err != nil {
		return fmt.Errorf("failed to hydrate working memory: %w", err)
	}

	// Scope-gated tools (Phase 7): inject only the tools the caller's scopes
	// permit. A forbidden tool never appears in the payload, so the model cannot
	// know it exists (docs/architecture/granular-control.md). The scopes are the
	// AUTHORITATIVE ones (effectiveScopes): under a real authenticator a client
	// cannot escalate via body scopes; under NoAuth the body scopes are the
	// fallback.
	if d.ToolReg != nil {
		if frag := d.ToolReg.PromptFragment(effectiveScopes(ctx, in.Body.Scopes)); frag != "" {
			system += "\n\n" + frag
		}
	}

	// Persist the user's turn AND its consolidation intent in a single BadgerDB
	// transaction (Transactional Outbox, Phase 5): the message lands in working
	// memory and a pending event is queued for the async worker to distil into
	// the facts + semantic tiers (internal/consolidation). The two writes are
	// atomic — a crash cannot keep one without the other — and this happens
	// before the LLM call because the user's message is real input that already
	// happened.
	ref := outboxRef(key, forwarded)
	if d.BPTSampler != nil && bitrate.ShouldSample(ref) {
		// Held-out bitrate sample (Bet 6): reserve this turn for offline
		// surprisal measurement and DO NOT enqueue it for facts/semantic
		// consolidation. The user message still enters working memory (it really
		// happened); replacing the consolidation enqueue with the reservation is
		// what keeps the scored text out of the tenant's long-term memory, so the
		// later lift is not spurious (leakage).
		if err := d.WM.Append(ctx, key, memory.Message{Role: roleUser, Content: forwarded}); err != nil {
			slog.ErrorContext(ctx, "failed to append held-out user message", "err", err)
		}
		if err := d.BPTSampler.Reserve(ctx, in.TenantID, in.UserID, in.Body.SessionID, ref, forwarded); err != nil {
			slog.ErrorContext(ctx, "failed to reserve bitrate held-out sample", "err", err)
		}
	} else {
		event := memory.NewOutboxEvent(key, ref, forwarded, time.Now().UTC())
		if err := d.WM.AppendWithOutbox(ctx, key, memory.Message{Role: roleUser, Content: forwarded}, event); err != nil {
			slog.ErrorContext(ctx, "failed to append user message + outbox event", "err", err)
		}
	}

	// Model selection through the gateway seam first (nil gw -> the passed
	// client). This is where routing policy (e.g. cost-budget shaping) picks the
	// target.
	active := selectClient(ctx, d.GW, d.Client, d.Meter, in.TenantID, in.UserID, forwarded, in.Body.ModelPreference, len(in.Body.Images) > 0)

	// Quota-pressure degradation (Phase 7) applies as an OVERRIDE on top of the
	// gateway pick: when this tenant is near its limit, route to the cheaper
	// model instead of the gateway-chosen one — graceful degradation rather than
	// a hard 429. Requires a configured cheap model (AISOTROPY_CHEAP_MODEL);
	// otherwise cheapClient is nil and the gateway pick stands.
	if d.CheapClient != nil && d.Limiter != nil {
		if pressure, perr := d.Limiter.Pressure(ctx, in.TenantID); perr == nil {
			if choice := llm.DegradeAt(pressure, llm.DefaultDegradeThreshold, active.Name(), d.CheapClient.Name()); choice.Degraded {
				active = d.CheapClient
				slog.WarnContext(ctx, "degrading to cheap model under quota pressure",
					"tenant_id", in.TenantID, "pressure", pressure, "model", d.CheapClient.Name())
			}
		}
	}

	tokens, err := active.StreamChat(ctx, system, forwarded)
	if err != nil {
		return fmt.Errorf("failed to start stream: %w", err)
	}

	// Hard output-blocking mode (opt-in, AISOTROPY_GUARDRAILS_BLOCK_OUTPUT): when
	// ON, tokens are ACCUMULATED WITHOUT being flushed to the client, so the
	// assembled response can be inspected before a single byte is emitted.
	// Tradeoff: buffering the full response sacrifices token-streaming latency
	// (TTFT) for the guarantee that unsafe (toxic/leaky) output is never sent.
	// When OFF (default), tokens flush live as they arrive (no latency change)
	// and the assembled text is inspected annotate-only afterward — a streamed
	// response cannot be un-sent.
	blocking := d.Guard.outputBlockingEnabled()

	var assistantReply strings.Builder
	for {
		select {
		case <-ctx.Done():
			// The client closed the connection (or the request expired). There
			// is nothing else to do here: the `tokens` channel is closed by its
			// own producer upon observing the same ctx.Done() (see
			// internal/llm/stub.go and anthropic.go) — token spend is cut there,
			// not here. The partial assistant reply accumulated so far is
			// deliberately NOT persisted: a cancelled/failed turn is not a
			// completed one. Returns nil (no error frame): a cancellation is not
			// a pipeline failure, and the sink's OnDone is deliberately not
			// called.
			slog.InfoContext(ctx, "chat stream cancelled", "tenant_id", in.TenantID, "user_id", in.UserID)
			return nil
		case tok, ok := <-tokens:
			if !ok {
				return nil
			}
			if tok.Err != nil {
				// Mid-stream failure: return it so the caller surfaces it as its
				// protocol's error frame and stops, so the client never mistakes
				// a truncated stream for success. The partial reply is not
				// persisted (see above). In blocking mode nothing has been
				// flushed, so no toxic partial escaped.
				return tok.Err
			}
			if tok.Done {
				// delivered is the assistant text actually handed to the wire —
				// the guardrail-safe text in blocking mode, the assembled reply in
				// live mode, "" when the model produced nothing. It becomes
				// StreamFinal.Text so the encoder's terminal frame reflects exactly
				// what the client received.
				var delivered string
				// Only a clean, complete stream gets its assistant turn persisted
				// to working memory.
				if blocking {
					// Buffered path: nothing has been flushed yet. Inspect the
					// assembled text via checkOutput, which returns it unchanged
					// when safe or the safe refusal when it tripped a guardrail.
					// ONLY that (safe) text is emitted and persisted — the toxic/
					// leaky text never reaches the wire nor memory, mirroring the
					// non-streaming route.
					if assistantReply.Len() > 0 {
						delivered = d.Guard.checkOutput(ctx, in.TenantID, in.UserID, forwarded, assistantReply.String(), system)
						if err := d.WM.Append(ctx, key, memory.Message{Role: roleAssistant, Content: delivered}); err != nil {
							slog.ErrorContext(ctx, "failed to append assistant message to working memory", "err", err)
						}
						if err := sink.OnToken(delivered); err != nil {
							// Client disconnected before the buffered reply could be
							// emitted; stop without recording cost or a done frame.
							return nil
						}
					}
				} else if assistantReply.Len() > 0 {
					delivered = assistantReply.String()
					if err := d.WM.Append(ctx, key, memory.Message{Role: roleAssistant, Content: delivered}); err != nil {
						slog.ErrorContext(ctx, "failed to append assistant message to working memory", "err", err)
					}
					// Output guardrail on the ASSEMBLED final text (annotate-only:
					// the stream was already sent token by token, so a refusal
					// can't replace it here — that is exactly what blocking mode
					// above trades latency for).
					d.Guard.annotateOutput(ctx, in.TenantID, in.UserID, forwarded, delivered, system)
				}
				// Per-tenant cost accounting (Phase 7 observability): record the
				// real provider token usage carried on the terminal token, so the
				// /costs view can show cost per token per tenant.
				recordStreamCost(d, in.TenantID, tok.Usage)
				sink.OnDone(StreamFinal{Text: delivered, TenantID: in.TenantID, Usage: tok.Usage})
				return nil
			}
			assistantReply.WriteString(tok.Text)
			if !blocking {
				// Live flush. In blocking mode this is deliberately skipped: the
				// token is buffered above and emitted (or refused) only after the
				// full response has been inspected.
				if err := sink.OnToken(tok.Text); err != nil {
					// Client disconnected mid-write: there is no way to keep
					// writing, so we stop. The partial reply is not persisted.
					return nil
				}
			}
		}
	}
}

// recordStreamCost records the streaming turn's provider token usage to the
// shared cost meter using the streaming price basis (StreamDeps.InPricePer1M/
// OutPricePer1M, each falling back to the package default when <= 0). A nil
// meter or nil usage records nothing — the terminal token carries usage only
// when the provider supplied it. Factored out so the blocking and non-blocking
// terminal branches record cost through one identical code path.
func recordStreamCost(d StreamDeps, tenantID string, usage *llm.Usage) {
	if d.Meter == nil || usage == nil {
		return
	}
	inP, outP := costInputPricePer1M, costOutputPricePer1M
	if d.InPricePer1M > 0 {
		inP = d.InPricePer1M
	}
	if d.OutPricePer1M > 0 {
		outP = d.OutPricePer1M
	}
	// Reasoning tokens (ADR-0061) are priced at the output rate and recorded on
	// their own line for visibility; 0 on a non-reasoning turn, so the estimate
	// and the meter line are unchanged when reasoning is off.
	cost := costmeter.EstimateCostUSD(usage.InputTokens, usage.OutputTokens, inP, outP) +
		costmeter.EstimateCostUSD(0, usage.ReasoningTokens, 0, outP)
	d.Meter.RecordUsage(tenantID, usage.InputTokens, usage.OutputTokens, usage.ReasoningTokens, cost)
}
