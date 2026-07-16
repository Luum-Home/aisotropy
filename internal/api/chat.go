// Package api defines Aisotropy's HTTP contracts and handlers.
//
// Phase 0 of the roadmap (docs/roadmap/milestones.md) established the Go
// struct as the single source of truth: a single type governs both HTTP
// input validation and the OpenAPI 3.1 schema Huma autogenerates. Phase 2
// follow-up wires this endpoint to a real structured-output pipeline: the
// reflection loop (internal/llm's ReflectAuto — GollemReflect by default,
// with the homegrown Reflect[T] fallback per ADR-0008) enforces the reply
// against chatStructuredReply and fails closed (an HTTP error, never a
// silent unstructured fallback) on a persistent schema violation, per
// .claude/rules/constitutional-gates.md's "Structured-output / reflection
// loop" gate.
//
// Phase 2 follow-up "C": tenant_id and user_id both travel as path params —
// identity belongs in the path (and, from there, natively into ctx), never
// in the body (see docs/architecture/multi-tenancy.md and the constitutional
// gate on tenant isolation). In production, user_id is derived from the
// authenticated JWT via a Huma security/auth middleware that rejects the
// request before this handler ever runs; the path param here is the
// walking-skeleton stand-in for that middleware. session_id, by contrast, is
// a client-chosen conversation identifier with no authorization meaning, so
// it stays in the body (see ChatRequest).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Luum-Home/aisotropy/internal/costmeter"
	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/gateway"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// chatMaxReflectionRetries bounds the reflection loop's retries (ADR-0008 /
// the constitutional "fail closed" gate): a persistent schema violation
// returns an error after this many attempts, never a silent unstructured
// fallback.
const chatMaxReflectionRetries = 3

// httpMethodPost is the HTTP method every chat route (chat, chat/stream,
// chat/agent) registers under.
const httpMethodPost = "POST"

// roleUser / roleAssistant are the memory.Message.Role values persisted for
// each side of a chat turn, shared across the non-streaming and streaming
// handlers.
const (
	roleUser      = "user"
	roleAssistant = "assistant"
)

// ChatRequest is the input contract (Input Guardrail via Huma).
// The tags govern both HTTP validation and the published JSON Schema.
type ChatRequest struct {
	Prompt    string `json:"prompt" maxLength:"2000" doc:"User message or instruction"`
	ProductID string `json:"product_id" doc:"Product or module ID" example:"analytics-v1"`
	SessionID string `json:"session_id" doc:"Client-chosen conversation identifier (working-memory session key)" example:"session-42"`
	// Scopes are the caller's granted permissions (JWT-derived in production;
	// a request field in the walking skeleton). They gate which tools are
	// injected into the LLM payload — a tool the scopes don't cover never
	// exists for the model (see internal/tools, docs/architecture/granular-control.md).
	Scopes []string `json:"scopes,omitempty" doc:"Granted permission scopes gating tool access, e.g. [\"write:tasks\"]"`
	// ModelPreference is an OPTIONAL explicit operator/UI model override: the
	// registry NAME of a gateway target (e.g. "cheap") the caller wants to serve
	// this turn. It is a routing PREFERENCE, not authorization — the gateway's
	// OverridePolicy honors it over the cost-budget rule, but the streaming
	// path's quota-pressure DegradeAt still overrides it (the safety valve), and
	// an unknown name resolves to the default. Empty (the default) leaves routing
	// unchanged. Additive per ADR-0002 (the struct is the contract): omitempty,
	// so existing clients that never send it are byte-for-byte unaffected.
	ModelPreference string `json:"model_preference,omitempty" doc:"Optional explicit model/gateway-target override (e.g. \"cheap\"); an explicit preference wins over cost-budget routing but quota-pressure degradation still overrides it"`
	// Images is an OPTIONAL presence SIGNAL that this turn carries image content
	// (image URLs or data refs). Its ONLY effect today is routing: a non-empty
	// Images activates the gateway's capability-based ImageRule so an image-
	// bearing turn is routed to the configured vision/premium target (see
	// selectClient -> gateway.Request.HasImage and buildModelGateway's ImageRule).
	//
	// HONEST SCOPE: this wires the ROUTING SIGNAL only. Actually sending the image
	// BYTES to the model requires a multimodal llm.Client, which the current
	// text-only clients (Anthropic/OpenAI-compat/stub) are NOT — the image parts
	// are never transmitted to the model yet. That multimodal call is a named
	// follow-on. Consequently the field's contents are not inspected here beyond
	// their presence; they are neither embedded, persisted, nor forwarded.
	//
	// Additive per ADR-0002 (the struct is the contract): omitempty, so existing
	// clients that never send it are byte-for-byte unaffected, and an empty/absent
	// Images leaves routing exactly as before (inert-safe).
	Images []string `json:"images,omitempty" doc:"Optional image URLs or data refs — a presence SIGNAL that routes the turn to a vision/premium model target (ImageRule). Image bytes are not yet sent to the model; this wires routing only."`
}

// ChatInput combines the tenant/user-scoped path params with the validated
// body. TenantID and UserID travel natively through the handler signature
// and are injected into ctx.
type ChatInput struct {
	// TenantID/UserID are constrained to a safe identifier charset at the
	// contract level (Huma rejects a violation with 422 before the handler
	// runs). This is defense-in-depth for the multi-tenancy gate: the tenant
	// id can flow into physical resource names (routing.Router) and store
	// keys, so an injection/oversize surface is closed here, not only in
	// withIdentity's downstream check.
	TenantID string `path:"tenant_id" pattern:"^[a-zA-Z0-9_-]{1,64}$" doc:"Unique customer/tenant ID" example:"acme"`
	UserID   string `path:"user_id" pattern:"^[a-zA-Z0-9_-]{1,64}$" doc:"Unique user ID within the tenant" example:"alice"`
	Body     ChatRequest
}

// ChatOutput is the strict output contract.
type ChatOutput struct {
	Body struct {
		Reply    string `json:"reply" doc:"Agent response, schema-enforced via the reflection loop"`
		TenantID string `json:"tenant_id" doc:"Tenant that served the request (isolation echo)"`
		Stub     bool   `json:"stub" doc:"true when the resolved LLM client is the credential-less stub (no provider key configured)"`
	}
}

// chatStructuredReply is the schema the reflection loop enforces on the raw
// LLM output (see internal/llm's ReflectAuto/GollemReflect). The Go struct
// IS the schema: Agent[T] derives its JSON Schema from this type by
// reflection — the same "struct as single source of truth" thesis
// ChatRequest/ChatOutput apply to HTTP, applied here to the LLM's own output.
type chatStructuredReply struct {
	Reply string `json:"reply"`
}

// decodeChatReply is the ValidateFunc[T] the homegrown Reflect[T] fallback
// backend (ADR-0008) uses to detect a schema violation: invalid JSON, or
// JSON that does not carry a non-empty "reply" field.
func decodeChatReply(raw string) (chatStructuredReply, error) {
	var out chatStructuredReply
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return chatStructuredReply{}, fmt.Errorf(`response is not valid JSON matching {"reply": string}: %w`, err)
	}
	if strings.TrimSpace(out.Reply) == "" {
		return chatStructuredReply{}, errors.New(`response JSON is valid but "reply" is empty`)
	}
	return out, nil
}

// validateChatReply is the business-validation step both RunWithReflection
// (Gollem backend) and Reflect[T] (homegrown fallback) use: "" accepts a
// non-empty feedback string requests a correction.
func validateChatReply(_ context.Context, out chatStructuredReply) (string, error) {
	if strings.TrimSpace(out.Reply) == "" {
		return `the "reply" field must be a non-empty string`, nil
	}
	return "", nil
}

// tenantCtxKey is the key used to propagate the tenant through context.Context.
type tenantCtxKey struct{}

// userCtxKey is the key used to propagate the user through context.Context,
// alongside tenantCtxKey. See the package doc for why this is a path param
// today and a JWT-derived value in production.
type userCtxKey struct{}

// withIdentity injects tenant and user into ctx and validates tenant, user,
// and session together via memory.Key.Validate() — the same check working
// memory itself applies — so an invalid, empty, or reserved-character
// identity can never reach a downstream storage key or the handler body.
func withIdentity(ctx context.Context, tenant, user, session string) (context.Context, error) {
	if err := (memory.Key{Tenant: tenant, User: user, Session: session}).Validate(); err != nil {
		return ctx, err
	}
	ctx = context.WithValue(ctx, tenantCtxKey{}, tenant)
	ctx = context.WithValue(ctx, userCtxKey{}, user)
	return ctx, nil
}

// chatIdentity validates the tenant/user/session identity, injects it into
// ctx (both the local carrier and internal/llm's own ctx carrier, mirroring
// chat_stream.go so OTel instrumentation can attribute spans without
// internal/llm importing internal/api), and builds the memory.Key +
// facts.Scope the rest of the handler needs. Both failure modes below map to
// the same 422 in Register — an invalid identity, wherever it is caught, is
// always a client-input problem.
func chatIdentity(ctx context.Context, in *ChatInput) (outCtx context.Context, key memory.Key, scope facts.Scope, err error) {
	// Isolation via ctx: tenant_id and user_id from the path travel
	// natively downward (see docs/architecture/multi-tenancy.md).
	outCtx, err = withIdentity(ctx, in.TenantID, in.UserID, in.Body.SessionID)
	if err != nil {
		// Named returns: key/scope stay at their zero value here without a
		// bare facts.Scope{}/memory.Key{} literal (ADR-0011 — facts.Scope is
		// a VO constructed exclusively via facts.NewScope/ScopeFromContext).
		return outCtx, key, scope, err
	}
	outCtx = llm.WithTenant(outCtx, in.TenantID)
	outCtx = llm.WithUser(outCtx, in.UserID)
	key = memory.Key{Tenant: in.TenantID, User: in.UserID, Session: in.Body.SessionID}
	scope, err = facts.NewScope(in.TenantID, in.UserID) //nolint:contextcheck // facts.NewScope has no context parameter (internal/facts, out of scope for this cleanup)
	if err != nil {
		return outCtx, memory.Key{}, scope, err
	}
	return outCtx, key, scope, nil
}

// chatCheckQuota is the Phase 7 / constitutional quota gate: reject BEFORE
// the LLM call with a clean 429 so a rate-limited tenant never spends
// tokens. A nil limiter (as in tests) skips the check. The returned error,
// when non-nil, is already a huma.StatusError ready to return as-is.
func chatCheckQuota(ctx context.Context, limiter *memory.Limiter, tenantID string) error {
	if limiter == nil {
		return nil
	}
	allowed, err := limiter.Allow(ctx, tenantID)
	if err != nil {
		return huma.Error500InternalServerError("quota check failed", err)
	}
	if !allowed {
		return huma.Error429TooManyRequests("rate limit exceeded for tenant " + tenantID)
	}
	return nil
}

// chatBuildSystemPrompt hydrates the system prompt from working memory +
// facts (the non-streaming endpoint does not wire semantic RAG — semStore/
// embedder are nil, that's the streaming path's job) and appends the
// scope-gated tools fragment (Phase 7): a forbidden tool never enters the
// payload (granular-control.md).
func chatBuildSystemPrompt(ctx context.Context, wm *memory.WorkingMemory, store facts.Store, toolReg *tools.Registry, persona PersonaProvider, key memory.Key, scope facts.Scope, in *ChatInput, prompt string) (string, error) {
	// Non-streaming route wires no semantic RAG (nil semStore/embedder), so the
	// reranker is nil too — there are no semantic hits for it to reorder.
	system, err := hydrateSystemPrompt(ctx, wm, store, nil, nil, nil, persona, key, scope, in.Body.ProductID, prompt)
	if err != nil {
		return "", err
	}
	if toolReg != nil {
		// Gate tools by the AUTHORITATIVE scopes: under a real authenticator the
		// verified principal's scopes win, so a client cannot self-grant a tool
		// via body scopes (see effectiveScopes). Under NoAuth/no middleware the
		// body scopes are the fallback.
		if frag := toolReg.PromptFragment(effectiveScopes(ctx, in.Body.Scopes)); frag != "" {
			system += "\n\n" + frag
		}
	}
	return system, nil
}

// chatPersistUserTurn persists the user's turn AND its consolidation intent
// in one BadgerDB transaction (Transactional Outbox), mirroring the
// streaming path — so the non-streaming endpoint also feeds the facts +
// semantic tiers via the outbox worker, not only the streaming one. Called
// before the LLM call because the user's message is real input that already
// happened. A persistence failure is logged, not fatal to the request.
func chatPersistUserTurn(ctx context.Context, wm *memory.WorkingMemory, key memory.Key, prompt string) {
	event := memory.NewOutboxEvent(key, outboxRef(key, prompt), prompt, time.Now().UTC())
	if err := wm.AppendWithOutbox(ctx, key, memory.Message{Role: roleUser, Content: prompt}, event); err != nil {
		slog.ErrorContext(ctx, "failed to append user message + outbox event", "err", err)
	}
}

// chatReflect runs the reflection loop over prompt and returns the validated
// reply plus the provider-reported token usage. It does NOT persist or record
// cost — that happens after the output guardrail runs, so a blocked response is
// never persisted (see the Register handler). A persistent schema/business
// violation fails closed: the returned error is already a huma.StatusError
// (502), never a silent fallback to unstructured text (the constitutional
// "Structured-output / reflection loop" gate).
func chatReflect(ctx context.Context, client llm.Client, system string, in *ChatInput, prompt string) (chatStructuredReply, llm.Usage, error) {
	reply, attempts, backend, usage, err := llm.ReflectAuto[chatStructuredReply](
		ctx, client, system, prompt, decodeChatReply, validateChatReply, chatMaxReflectionRetries,
	)
	if err != nil {
		slog.ErrorContext(ctx, "chat reflection loop failed closed",
			"tenant_id", in.TenantID, "user_id", in.UserID, "backend", backend, "attempts", attempts, "err", err)
		return chatStructuredReply{}, llm.Usage{}, huma.Error502BadGateway(
			fmt.Sprintf("agent failed to produce a valid structured reply after %d attempt(s)", attempts), err)
	}

	slog.InfoContext(ctx, "chat request completed",
		"tenant_id", in.TenantID, "user_id", in.UserID, "backend", backend, "attempts", attempts)

	return reply, usage, nil
}

// chatRecordTurn persists the assistant's final reply (after the output
// guardrail has had its say) to working memory and records cost to the shared
// meter. finalReply is the text actually returned to the client — so when the
// output guardrail replaced a flagged response with a refusal, the refusal (not
// the flagged text) is what persists in memory.
func chatRecordTurn(ctx context.Context, wm *memory.WorkingMemory, key memory.Key, tenantID, finalReply string, usage llm.Usage, meter *costmeter.Meter) {
	if err := wm.Append(ctx, key, memory.Message{Role: roleAssistant, Content: finalReply}); err != nil {
		slog.ErrorContext(ctx, "failed to append assistant message to working memory", "err", err)
	}

	// Unify cost accounting onto the shared meter (same price basis
	// RegisterStream uses): the homegrown Reflect[T] fallback reports a
	// zero Usage (documented, see llm.ReflectResult.Usage), so this
	// records 0 cost for that path rather than silently skipping it —
	// the request count/attempt is still real signal for /costs.
	if meter != nil {
		cost := costmeter.EstimateCostUSD(usage.InputTokens, usage.OutputTokens, costInputPricePer1M, costOutputPricePer1M)
		meter.Record(tenantID, usage.InputTokens, usage.OutputTokens, cost)
	}
}

// chatBuildOutput assembles the strict ChatOutput contract from the final reply
// text and the client that served the turn.
func chatBuildOutput(replyText, tenantID string, client llm.Client) *ChatOutput {
	out := &ChatOutput{}
	out.Body.Reply = replyText
	out.Body.TenantID = tenantID
	out.Body.Stub = client.Name() == "stub"
	return out
}

// Register mounts the non-streaming chat endpoint on the Huma API. It
// receives the already-built llm.Client (llm.FromEnv() in main.go) and the
// shared *memory.WorkingMemory instead of creating them here — no global
// state, consistent with isolation via ctx (see docs/vision/principles.md).
// persona supplies the base system prompt per product_id (Phase 8 adoption
// seam); nil selects the built-in default persona. meter, when non-nil, is
// the SAME *costmeter.Meter the streaming route (RegisterStream) records
// to — unifying cost accounting onto one meter so GET /costs reflects both
// routes (previously only the streaming path recorded real cost; the
// reflection path's Gollem CostTracker has empty pricing and was never
// surfaced here). A nil meter skips recording (keeps existing tests that
// pass nil working unchanged).
//
// gw is the model-selection gateway seam (internal/gateway): when non-nil, the
// client that serves the turn is chosen by its routing policy (selectClient);
// nil selects the passed client directly, preserving pre-gateway behavior.
// guard bundles the input/output guardrails with their opt-in blocking toggles:
// when configured, the user prompt is redacted before it reaches memory or the
// model, an injection verdict is annotated (or, if BlockInjection, rejected),
// and the model response is inspected before it is returned (or, if BlockOutput,
// replaced with a safe refusal). A nil guard disables guardrails entirely.
func Register(api huma.API, client llm.Client, wm *memory.WorkingMemory, store facts.Store, limiter *memory.Limiter, toolReg *tools.Registry, persona PersonaProvider, meter *costmeter.Meter, gw *gateway.Gateway, guard *Guard) {
	huma.Register(api, huma.Operation{
		OperationID: "chat",
		Method:      httpMethodPost,
		Path:        "/v1/tenant/{tenant_id}/user/{user_id}/chat",
		Summary:     "Chat with the agent (structured output via the reflection loop)",
		Description: "Validates the contract, isolates by tenant/user via ctx, hydrates the " +
			"session's working memory into the system prompt, and enforces the reply " +
			"against a JSON schema via the reflection loop (ReflectAuto: Gollem by " +
			"default, with a homegrown fallback backend per ADR-0008). A persistent " +
			"schema violation fails closed with an HTTP error — never a silent " +
			"unstructured fallback (see .claude/rules/constitutional-gates.md).",
	}, func(ctx context.Context, in *ChatInput) (*ChatOutput, error) {
		// Thin wrapper: bundle the handler's dependencies and delegate to
		// ChatCore, the single non-streaming pipeline the in-process caller
		// (aisotropy.App.Turn) also runs. Keeping the body here to just the
		// ChatDeps assembly is what guarantees HTTP and in-process share one
		// code path — no behavior lives in this closure any more.
		return ChatCore(ctx, ChatDeps{
			Client:  client,
			WM:      wm,
			Store:   store,
			Limiter: limiter,
			ToolReg: toolReg,
			Persona: persona,
			Meter:   meter,
			GW:      gw,
			Guard:   guard,
		}, in)
	})
}

// TenantFromContext retrieves the tenant injected by the handler.
func TenantFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(tenantCtxKey{}).(string)
	return v, ok
}

// UserFromContext retrieves the user injected by the handler.
func UserFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(userCtxKey{}).(string)
	return v, ok
}
