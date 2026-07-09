package api

import (
	"context"
	"fmt"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Luum-Home/aisotropy/internal/agentloop"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/primitives"
	"github.com/Luum-Home/aisotropy/internal/tools"
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

// echoExecutor is the placeholder tool implementation: it echoes the call and
// never fails. Real tools replace it.
type echoExecutor struct{}

func (echoExecutor) Execute(_ context.Context, name string, args map[string]string) (string, error) {
	return fmt.Sprintf("executed %q with args %v", name, args), nil
}

// frictionExecutor wraps a ToolExecutor and, when an invocation FAILS, records
// a friction signal for that user's primitive (internal/primitives) so the
// sleep cycle can later rewrite it (Phase 6 behavioral memory). It is scoped to
// one request's (tenant, user). Recording is best-effort and never affects the
// request outcome.
type frictionExecutor struct {
	inner    agentloop.ToolExecutor
	detector *primitives.FrictionDetector
	tenant   string
	user     string
}

func (f frictionExecutor) Execute(ctx context.Context, name string, args map[string]string) (string, error) {
	res, err := f.inner.Execute(ctx, name, args)
	if err != nil && f.detector != nil {
		_, _ = f.detector.Observe(f.tenant, f.user, name, primitives.SignalFailure)
	}
	return res, err
}

// RegisterAgent mounts the agentic tool-use endpoint (Phase 7 behavior axis):
// POST /v1/tenant/{tenant_id}/user/{user_id}/chat/agent. It runs the
// structured-output tool loop (internal/agentloop) over the request's
// scope-permitted tools and returns the ordered tool decisions + results. A
// tool outside the caller's scopes is never offered to the model nor executable
// (the loop enforces that invariant post-parse). A failing tool feeds friction
// into detector (nil disables friction tracking).
func RegisterAgent(api huma.API, client llm.Client, registry *tools.Registry, detector *primitives.FrictionDetector, maxSteps int) {
	huma.Register(api, huma.Operation{
		OperationID: "chat-agent",
		Method:      "POST",
		Path:        "/v1/tenant/{tenant_id}/user/{user_id}/chat/agent",
		Summary:     "Agentic tool use over structured output (Phase 7)",
		Description: "Runs a bounded structured-output loop: the model chooses, as " +
			"JSON, which scope-permitted tool to call (or none), the tool is " +
			"executed by the host, and the result is fed back. Tools the " +
			"caller's scopes do not grant are never injected into the payload " +
			"nor executable; a failing tool records friction for later rewriting.",
	}, func(ctx context.Context, in *ChatInput) (*AgentOutput, error) {
		ctx, err := withIdentity(ctx, in.TenantID, in.UserID, in.Body.SessionID)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid tenant/user identity", err)
		}
		if client == nil || registry == nil {
			return nil, huma.Error503ServiceUnavailable("agent mode is not configured")
		}

		// Per-request executor so friction is scoped to this (tenant, user).
		var exec agentloop.ToolExecutor = echoExecutor{}
		if detector != nil {
			exec = frictionExecutor{inner: echoExecutor{}, detector: detector, tenant: in.TenantID, user: in.UserID}
		}
		loop := &agentloop.Loop{Client: client, Registry: registry, Executor: exec, MaxSteps: maxSteps}

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
