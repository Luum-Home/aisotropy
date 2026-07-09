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
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// chatMaxReflectionRetries bounds the reflection loop's retries (ADR-0008 /
// the constitutional "fail closed" gate): a persistent schema violation
// returns an error after this many attempts, never a silent unstructured
// fallback.
const chatMaxReflectionRetries = 3

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
		return chatStructuredReply{}, fmt.Errorf(`response JSON is valid but "reply" is empty`)
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

// Register mounts the non-streaming chat endpoint on the Huma API. It
// receives the already-built llm.Client (llm.FromEnv() in main.go) and the
// shared *memory.WorkingMemory instead of creating them here — no global
// state, consistent with isolation via ctx (see docs/vision/principles.md).
func Register(api huma.API, client llm.Client, wm *memory.WorkingMemory, store facts.Store, limiter *memory.Limiter, toolReg *tools.Registry) {
	huma.Register(api, huma.Operation{
		OperationID: "chat",
		Method:      "POST",
		Path:        "/v1/tenant/{tenant_id}/user/{user_id}/chat",
		Summary:     "Chat with the agent (structured output via the reflection loop)",
		Description: "Validates the contract, isolates by tenant/user via ctx, hydrates the " +
			"session's working memory into the system prompt, and enforces the reply " +
			"against a JSON schema via the reflection loop (ReflectAuto: Gollem by " +
			"default, with a homegrown fallback backend per ADR-0008). A persistent " +
			"schema violation fails closed with an HTTP error — never a silent " +
			"unstructured fallback (see .claude/rules/constitutional-gates.md).",
	}, func(ctx context.Context, in *ChatInput) (*ChatOutput, error) {
		// Isolation via ctx: tenant_id and user_id from the path travel
		// natively downward (see docs/architecture/multi-tenancy.md).
		ctx, err := withIdentity(ctx, in.TenantID, in.UserID, in.Body.SessionID)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid tenant/user identity", err)
		}
		// Mirror chat_stream.go: also propagate identity through internal/llm's
		// own ctx carrier so any OTel instrumentation on this path can attribute
		// spans with tenant_id/user_id without internal/llm importing internal/api.
		ctx = llm.WithTenant(ctx, in.TenantID)
		ctx = llm.WithUser(ctx, in.UserID)
		key := memory.Key{Tenant: in.TenantID, User: in.UserID, Session: in.Body.SessionID}
		scope, err := facts.NewScope(in.TenantID, in.UserID)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid tenant/user identity", err)
		}

		// Quota gate (Phase 7 / constitutional gate): reject BEFORE the LLM call
		// with a clean 429 so a rate-limited tenant never spends tokens.
		if limiter != nil {
			allowed, err := limiter.Allow(ctx, in.TenantID)
			if err != nil {
				return nil, huma.Error500InternalServerError("quota check failed", err)
			}
			if !allowed {
				return nil, huma.Error429TooManyRequests("rate limit exceeded for tenant " + in.TenantID)
			}
		}

		slog.InfoContext(ctx, "chat request",
			"tenant_id", in.TenantID,
			"user_id", in.UserID,
			"session_id", in.Body.SessionID,
			"product_id", in.Body.ProductID,
			"prompt_len", len(in.Body.Prompt),
			"llm", client.Name(),
		)

		// The non-streaming endpoint hydrates working memory + facts; semantic
		// RAG (semStore/embedder) is wired on the streaming path, so nil is
		// passed for those two here.
		system, err := hydrateSystemPrompt(ctx, wm, store, nil, nil, key, scope, in.Body.ProductID, in.Body.Prompt)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to hydrate working memory", err)
		}
		// Scope-gated tools (Phase 7): inject only the scope-permitted tools —
		// a forbidden tool never enters the payload (granular-control.md).
		if toolReg != nil {
			if frag := toolReg.PromptFragment(in.Body.Scopes); frag != "" {
				system += "\n\n" + frag
			}
		}

		// Persist the user's turn AND its consolidation intent in one BadgerDB
		// transaction (Transactional Outbox), mirroring the streaming path — so
		// the non-streaming endpoint also feeds the facts + semantic tiers via
		// the outbox worker, not only the streaming one. Done before the LLM
		// call because the user's message is real input that already happened.
		event := memory.NewOutboxEvent(key, outboxRef(key, in.Body.Prompt), in.Body.Prompt, time.Now().UTC())
		if err := wm.AppendWithOutbox(ctx, key, memory.Message{Role: "user", Content: in.Body.Prompt}, event); err != nil {
			slog.ErrorContext(ctx, "failed to append user message + outbox event", "err", err)
		}

		reply, attempts, backend, err := llm.ReflectAuto[chatStructuredReply](
			ctx, client, system, in.Body.Prompt, decodeChatReply, validateChatReply, chatMaxReflectionRetries,
		)
		if err != nil {
			// Fail closed: a persistent schema/business violation is an error
			// response, never a silent fallback to unstructured text (the
			// constitutional "Structured-output / reflection loop" gate).
			slog.ErrorContext(ctx, "chat reflection loop failed closed",
				"tenant_id", in.TenantID, "user_id", in.UserID, "backend", backend, "attempts", attempts, "err", err)
			return nil, huma.Error502BadGateway(
				fmt.Sprintf("agent failed to produce a valid structured reply after %d attempt(s)", attempts), err)
		}

		// Only a successfully validated turn gets persisted — a failed/rejected
		// attempt (handled above) never reaches this line.
		if err := wm.Append(ctx, key, memory.Message{Role: "assistant", Content: reply.Reply}); err != nil {
			slog.ErrorContext(ctx, "failed to append assistant message to working memory", "err", err)
		}

		slog.InfoContext(ctx, "chat request completed",
			"tenant_id", in.TenantID, "user_id", in.UserID, "backend", backend, "attempts", attempts)

		out := &ChatOutput{}
		out.Body.Reply = reply.Reply
		out.Body.TenantID = in.TenantID
		out.Body.Stub = client.Name() == "stub"
		return out, nil
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
