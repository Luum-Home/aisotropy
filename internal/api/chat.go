// Package api defines Aisotropy's HTTP contracts and handlers.
//
// Phase 0 of the roadmap (docs/roadmap/milestones.md): the Go struct is the
// single source of truth. A single type governs both HTTP input validation
// and the OpenAPI 3.1 schema that Huma autogenerates. Still no LLM or memory
// — the handler returns a stub to prove the contract works end to end (see
// docs/vision/thesis.md -> "The struct as the single source of truth").
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
	"log/slog"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Luum-Home/aisotropy/internal/memory"
)

// ChatRequest is the input contract (Input Guardrail via Huma).
// The tags govern both HTTP validation and the published JSON Schema.
type ChatRequest struct {
	Prompt    string `json:"prompt" maxLength:"2000" doc:"User message or instruction"`
	ProductID string `json:"product_id" doc:"Product or module ID" example:"analytics-v1"`
	SessionID string `json:"session_id" doc:"Client-chosen conversation identifier (working-memory session key)" example:"session-42"`
}

// ChatInput combines the tenant/user-scoped path params with the validated
// body. TenantID and UserID travel natively through the handler signature
// and are injected into ctx.
type ChatInput struct {
	TenantID string `path:"tenant_id" doc:"Unique customer/tenant ID" example:"acme"`
	UserID   string `path:"user_id" doc:"Unique user ID within the tenant" example:"alice"`
	Body     ChatRequest
}

// ChatOutput is the strict output contract. In future phases this same
// struct is what Gollem will enforce on the LLM via structured output.
type ChatOutput struct {
	Body struct {
		Reply    string `json:"reply" doc:"Agent response (stub in Phase 0)"`
		TenantID string `json:"tenant_id" doc:"Tenant that served the request (isolation echo)"`
		Stub     bool   `json:"stub" doc:"true while no LLM is connected"`
	}
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

// Register mounts the chat endpoint on the Huma API.
func Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "chat",
		Method:      "POST",
		Path:        "/v1/tenant/{tenant_id}/user/{user_id}/chat",
		Summary:     "Chat with the agent (Phase 0: stub, no LLM)",
		Description: "Validates the contract, isolates by tenant/user via ctx, and returns a stub response.",
	}, func(ctx context.Context, in *ChatInput) (*ChatOutput, error) {
		// Isolation via ctx: tenant_id and user_id from the path travel
		// natively downward (see docs/architecture/multi-tenancy.md).
		ctx, err := withIdentity(ctx, in.TenantID, in.UserID, in.Body.SessionID)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid tenant/user identity", err)
		}
		slog.InfoContext(ctx, "chat request",
			"tenant_id", in.TenantID,
			"user_id", in.UserID,
			"product_id", in.Body.ProductID,
			"prompt_len", len(in.Body.Prompt),
		)

		out := &ChatOutput{}
		out.Body.Reply = "stub: contract validated; LLM connects in Phase 1"
		out.Body.TenantID = in.TenantID
		out.Body.Stub = true
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
