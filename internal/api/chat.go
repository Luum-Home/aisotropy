// Package api defines Aisotropy's HTTP contracts and handlers.
//
// Phase 0 of the roadmap (docs/roadmap/milestones.md): the Go struct is the
// single source of truth. A single type governs both HTTP input validation
// and the OpenAPI 3.1 schema that Huma autogenerates. Still no LLM or memory
// — the handler returns a stub to prove the contract works end to end (see
// docs/vision/thesis.md -> "The struct as the single source of truth").
package api

import (
	"context"
	"log/slog"

	"github.com/danielgtaylor/huma/v2"
)

// ChatRequest is the input contract (Input Guardrail via Huma).
// The tags govern both HTTP validation and the published JSON Schema.
type ChatRequest struct {
	Prompt    string `json:"prompt" maxLength:"2000" doc:"User message or instruction"`
	ProductID string `json:"product_id" doc:"Product or module ID" example:"analytics-v1"`
}

// ChatInput combines the tenant-scoped path param with the validated body.
// TenantID travels natively through the handler signature and is injected into ctx.
type ChatInput struct {
	TenantID string `path:"tenant_id" doc:"Unique customer/tenant ID" example:"acme"`
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

// Register mounts the chat endpoint on the Huma API.
func Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "chat",
		Method:      "POST",
		Path:        "/v1/tenant/{tenant_id}/chat",
		Summary:     "Chat with the agent (Phase 0: stub, no LLM)",
		Description: "Validates the contract, isolates by tenant via ctx, and returns a stub response.",
	}, func(ctx context.Context, in *ChatInput) (*ChatOutput, error) {
		// Isolation via ctx: the tenant_id from the path travels natively
		// downward (see docs/architecture/multi-tenancy.md).
		ctx = context.WithValue(ctx, tenantCtxKey{}, in.TenantID)
		slog.InfoContext(ctx, "chat request",
			"tenant_id", in.TenantID,
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
