package api

// ask.go serves POST /v1/tenant/{tenant_id}/user/{user_id}/ask — the STATELESS,
// response-cached question endpoint that makes internal/responsecache a live
// request-path lever.
//
// It is deliberately a sibling of /chat, not a variant of it. /chat is stateful:
// it hydrates working memory, appends turns, and feeds the consolidation outbox,
// so its answers are personalized to a session's evolving context. /ask does
// NONE of that: no working-memory hydration, no session identifier, no outbox
// write. Its only context source is tenant-scoped semantic RAG. That statelessness
// is exactly what makes caching SAFE here — with no memory-hydrated personalization
// folded into the prompt, a cached answer for (tenant, user, prompt) cannot go
// stale against a user's changing working memory. Caching a /chat answer would be
// unsafe for that reason; caching an /ask answer is not.
//
// The cache is scoped to (tenant, user) in both of its layers (see
// internal/responsecache): a Get under one scope can never return an entry
// written under another, so a cross-tenant cache hit — a catastrophic data leak —
// is impossible by construction, not by a post-filter. This is the same isolation
// posture every storage tier enforces. Nil-safe throughout: a nil cache means
// "always compute" (no caching), a nil semStore/embedder means "no RAG context".
//
// It sits behind the SAME authenticated-identity middleware + must-agree bind as
// /chat because it is registered on the same Huma API after that middleware is
// installed (see aisotropy/app.go).

import (
	"context"
	"log/slog"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/responsecache"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// AskRequest is the input contract for the stateless /ask route. It carries no
// session_id (the route is stateless) and no scopes (it injects no tools) — only
// the question and an optional product id for persona selection. The Go struct
// IS the contract (ADR-0002): these tags drive both HTTP validation and the
// published OpenAPI 3.1 schema, exactly as ChatRequest does for /chat.
type AskRequest struct {
	Prompt    string `json:"prompt" maxLength:"2000" doc:"Question or instruction to answer statelessly"`
	ProductID string `json:"product_id" doc:"Product or module ID (selects the persona base prompt)" example:"analytics-v1"`
}

// AskInput combines the tenant/user-scoped path params with the validated body.
// TenantID/UserID travel natively via the handler signature into ctx and into
// the cache/RAG scopes — identity belongs in the path, never the body (see
// docs/architecture/multi-tenancy.md and the tenant-isolation constitutional
// gate). Under a real authenticator the auth middleware has already proven these
// equal the verified principal (must-agree bind) before this handler runs.
type AskInput struct {
	TenantID string `path:"tenant_id" pattern:"^[a-zA-Z0-9_-]{1,64}$" doc:"Unique customer/tenant ID" example:"acme"`
	UserID   string `path:"user_id" pattern:"^[a-zA-Z0-9_-]{1,64}$" doc:"Unique user ID within the tenant" example:"alice"`
	Body     AskRequest
}

// AskOutput is the strict output contract. Cached echoes whether the answer was
// served from the response cache (no LLM call) — useful signal for clients and
// tests. Stub mirrors ChatOutput.Stub: true when the resolved client is the
// credential-less stub.
type AskOutput struct {
	Body struct {
		Reply    string `json:"reply" doc:"The answer (freshly generated, or served from cache)"`
		TenantID string `json:"tenant_id" doc:"Tenant that served the request (isolation echo)"`
		Cached   bool   `json:"cached" doc:"true when served from the response cache without an LLM call"`
		Stub     bool   `json:"stub" doc:"true when the resolved LLM client is the credential-less stub"`
	}
}

// askIdentity validates the tenant/user identity (no session — the route is
// stateless), injects it into ctx (the api-layer tenant/user carriers plus
// internal/llm's OTel carriers, mirroring chatIdentity), and builds both the
// facts.Scope the RAG hydration needs and the responsecache.Scope the cache
// keys/partitions by. facts.NewScope validates tenant+user through
// internal/tenancy (ADR-0011); a violation returns an error the caller maps to
// 422. Named returns avoid a bare facts.Scope{} literal on the error path
// (facts.Scope is a VO constructed only via NewScope/ScopeFromContext).
func askIdentity(ctx context.Context, in *AskInput) (outCtx context.Context, scope facts.Scope, cacheScope responsecache.Scope, err error) {
	outCtx = ctx
	scope, err = facts.NewScope(in.TenantID, in.UserID) //nolint:contextcheck // facts.NewScope has no context parameter (internal/facts)
	if err != nil {
		return outCtx, scope, cacheScope, err
	}
	outCtx = context.WithValue(outCtx, tenantCtxKey{}, in.TenantID)
	outCtx = context.WithValue(outCtx, userCtxKey{}, in.UserID)
	outCtx = llm.WithTenant(outCtx, in.TenantID)
	outCtx = llm.WithUser(outCtx, in.UserID)
	// responsecache.Scope is a plain (tenant, user) value: the caller does the
	// tenancy validation (facts.NewScope above) before constructing it, per the
	// package's documented contract.
	cacheScope = responsecache.Scope{Tenant: in.TenantID, User: in.UserID}
	return outCtx, scope, cacheScope, nil
}

// collectCompletion makes ONE plain (non-structured) LLM call and assembles the
// full reply from the token stream. /ask does not need the reflection loop's
// structured-output enforcement (its answer is free text destined for a cache,
// not a typed contract), so this is the plain house-style completion: stream,
// accumulate, stop. It honors ctx cancellation (returns ctx.Err(), cutting token
// spend at the producer) and surfaces a mid-stream error rather than treating a
// truncated stream as success.
func collectCompletion(ctx context.Context, client llm.Client, system, prompt string) (string, error) {
	tokens, err := client.StreamChat(ctx, system, prompt)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case tok, ok := <-tokens:
			if !ok {
				return sb.String(), nil
			}
			if tok.Err != nil {
				return "", tok.Err
			}
			if tok.Done {
				return sb.String(), nil
			}
			sb.WriteString(tok.Text)
		}
	}
}

// askBuildOutput assembles the strict AskOutput contract.
func askBuildOutput(reply, tenantID string, cached bool, client llm.Client) *AskOutput {
	out := &AskOutput{}
	out.Body.Reply = reply
	out.Body.TenantID = tenantID
	out.Body.Cached = cached
	out.Body.Stub = client.Name() == "stub"
	return out
}

// RegisterAsk mounts the stateless cached /ask endpoint on the Huma API. It is
// registered on the SAME humaAPI as /chat, AFTER the authn middleware is
// installed, so it inherits the fail-closed auth gate + must-agree bind (see
// aisotropy/app.go).
//
// cache is the per-process response cache; nil means "always compute" (no
// caching). semStore/embedder back tenant-scoped semantic RAG; nil disables RAG
// context (the answer is then persona-only). persona supplies the base system
// prompt per product_id; nil selects the built-in default. guard bundles the
// input/output guardrails with the same posture as /chat: the prompt is redacted
// (and, if BlockInjection, rejected) before it reaches the model or the cache key,
// and the model response is inspected before it is returned/cached (or, if
// BlockOutput, replaced with a safe refusal). A nil guard disables guardrails.
func RegisterAsk(api huma.API, client llm.Client, cache *responsecache.ResponseCache, semStore semantic.Store, embedder semantic.Embedder, persona PersonaProvider, guard *Guard) {
	huma.Register(api, huma.Operation{
		OperationID: "ask",
		Method:      httpMethodPost,
		Path:        "/v1/tenant/{tenant_id}/user/{user_id}/ask",
		Summary:     "Ask a stateless, response-cached question",
		Description: "Answers a question WITHOUT any session state: no working-memory " +
			"hydration, no conversation persistence, no consolidation outbox. It first " +
			"consults a per-(tenant,user) response cache; on a hit the stored answer is " +
			"returned WITHOUT an LLM call. On a miss it builds context from tenant-scoped " +
			"semantic retrieval (RAG), makes one LLM call, runs the output guardrail, " +
			"caches the answer under the (tenant,user) scope, and returns it. The cache is " +
			"scoped so a cross-tenant hit is impossible by construction (see " +
			"internal/responsecache).",
	}, func(ctx context.Context, in *AskInput) (*AskOutput, error) {
		ctx, scope, cacheScope, err := askIdentity(ctx, in)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid tenant/user identity", err)
		}

		// Input guardrail (redact + detect ALWAYS; block opt-in), same posture as
		// /chat. forwarded is the REDACTED prompt: it — never the raw prompt — is
		// what becomes the cache key, feeds the semantic retrieval, and reaches the
		// model, so a secret in the prompt never leaks into the model, the cache, or
		// any memory tier.
		forwarded, blocked := guard.checkInput(ctx, in.TenantID, in.UserID, in.Body.Prompt)
		if blocked {
			return nil, huma.Error400BadRequest("request blocked by input guardrail (possible prompt injection)")
		}

		// Response-cache lookup, scoped to (tenant, user). A HIT returns the cached
		// answer WITHOUT an LLM call. A nil cache always misses (compute). A
		// lookup error (e.g. semantic-layer embed failure) is treated as a miss —
		// we fall through to the model, never fail the request on a cache error.
		if cache != nil {
			if cached, ok, cerr := cache.Get(ctx, cacheScope, forwarded); cerr != nil {
				slog.WarnContext(ctx, "response cache lookup failed; computing", "tenant_id", in.TenantID, "user_id", in.UserID, "err", cerr)
			} else if ok {
				slog.InfoContext(ctx, "ask served from response cache", "tenant_id", in.TenantID, "user_id", in.UserID)
				return askBuildOutput(cached, in.TenantID, true, client), nil
			}
		}

		slog.InfoContext(ctx, "ask request (cache miss; computing)",
			"tenant_id", in.TenantID,
			"user_id", in.UserID,
			"product_id", in.Body.ProductID,
			"prompt_len", len(in.Body.Prompt),
			"llm", client.Name(),
		)

		// MISS: build context from tenant-scoped semantic RAG only. Passing nil for
		// working memory and the facts store makes hydrateSystemPrompt stateless —
		// it folds in NO session summary/recent messages and NO facts, only the
		// persona base prompt plus semantic fragments (the exact statelessness this
		// route depends on for cache safety). The empty memory.Key is unused when
		// wm is nil.
		system, err := hydrateSystemPrompt(ctx, nil, nil, semStore, embedder, persona, memory.Key{}, scope, in.Body.ProductID, forwarded)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to build semantic context", err)
		}

		reply, err := collectCompletion(ctx, client, system, forwarded)
		if err != nil {
			slog.ErrorContext(ctx, "ask LLM call failed", "tenant_id", in.TenantID, "user_id", in.UserID, "err", err)
			return nil, huma.Error502BadGateway("agent failed to produce a reply", err)
		}

		// Output guardrail BEFORE returning or caching: when BlockOutput is enabled
		// and the reply tripped a guardrail, the flagged text is replaced with a
		// safe refusal — so it is the SAFE text (never the flagged text) that both
		// caches and returns. Default posture: annotate + log, reply unchanged.
		finalReply := guard.checkOutput(ctx, in.TenantID, in.UserID, forwarded, reply, system)

		// Store the (safe) answer under the (tenant, user) scope so an identical
		// future question hits. A store failure is logged, not fatal — the answer is
		// still returned; only the caching is best-effort.
		if cache != nil {
			if serr := cache.Set(ctx, cacheScope, forwarded, finalReply); serr != nil {
				slog.WarnContext(ctx, "response cache store failed; answer returned uncached", "tenant_id", in.TenantID, "user_id", in.UserID, "err", serr)
			}
		}

		return askBuildOutput(finalReply, in.TenantID, false, client), nil
	})
}
