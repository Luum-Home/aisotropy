package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Luum-Home/aisotropy/internal/agentloop"
)

// AgentStep is one iteration of the agentic loop: the tool the model chose and,
// if a tool was executed, its result. Tool == "" means the model answered
// directly with no tool.
type AgentStep struct {
	Tool   string            `json:"tool"`
	Args   map[string]string `json:"args,omitempty"`
	Result string            `json:"result,omitempty"`
}

// AgentOutput is the strict output contract for the agent endpoint.
type AgentOutput struct {
	Body struct {
		TenantID string      `json:"tenant_id" doc:"Tenant that served the request"`
		Steps    []AgentStep `json:"steps" doc:"Tool decisions and results, in order"`
	}
}

// RegisterAgent mounts the agentic tool-use endpoint (Phase 7 behavior axis):
// POST /v1/tenant/{tenant_id}/user/{user_id}/chat/agent. It runs the
// structured-output tool loop (internal/agentloop) over the scope-permitted
// tools — a tool outside the request's scopes is never offered to the model and
// can never be executed (the loop enforces that invariant post-parse). It
// reuses ChatInput (the struct is the single source of truth), so scopes travel
// in the body alongside the prompt.
func RegisterAgent(api huma.API, loop *agentloop.Loop) {
	huma.Register(api, huma.Operation{
		OperationID: "chat-agent",
		Method:      "POST",
		Path:        "/v1/tenant/{tenant_id}/user/{user_id}/chat/agent",
		Summary:     "Agentic tool use over structured output (Phase 7)",
		Description: "Runs a bounded structured-output loop: the model chooses, as " +
			"JSON, which scope-permitted tool to call (or none), the tool is " +
			"executed by the host, and the result is fed back. Tools the " +
			"caller's scopes do not grant are never injected into the payload " +
			"nor executable.",
	}, func(ctx context.Context, in *ChatInput) (*AgentOutput, error) {
		ctx, err := withIdentity(ctx, in.TenantID, in.UserID, in.Body.SessionID)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid tenant/user identity", err)
		}
		if loop == nil {
			return nil, huma.Error503ServiceUnavailable("agent mode is not configured")
		}

		steps, err := loop.Run(ctx, in.Body.Scopes, in.Body.Prompt)
		if err != nil {
			return nil, huma.Error500InternalServerError("agent loop failed", err)
		}

		out := &AgentOutput{}
		out.Body.TenantID = in.TenantID
		for _, s := range steps {
			out.Body.Steps = append(out.Body.Steps, AgentStep{
				Tool:   s.Decision.Tool,
				Args:   s.Decision.Args,
				Result: s.Result,
			})
		}
		return out, nil
	})
}
