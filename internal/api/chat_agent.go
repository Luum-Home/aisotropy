package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/danielgtaylor/huma/v2"
	oteltrace "go.opentelemetry.io/otel/trace"

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

// DemoExecutor is the placeholder tool implementation: it echoes the call and
// never fails. It is the zero-configuration default the runtime falls back to
// (and what cmd/aisotropyd wires explicitly) so the agent endpoint works out of
// the box; a real consumer replaces it with its own ToolExecutor via the
// builder's WithToolExecutor option (see the aisotropy façade package).
type DemoExecutor struct{}

// Execute echoes the tool name and args back as its result and never fails.
func (DemoExecutor) Execute(_ context.Context, name string, args map[string]string) (string, error) {
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
//
// executor is the host-supplied ToolExecutor that actually runs a selected tool
// (Phase 8 adoption seam): the runtime injects it rather than hard-wiring a demo
// implementation, so a consumer can plug in real tool logic without editing this
// package. When detector is non-nil, the injected executor is wrapped per request
// in a frictionExecutor so a failing tool accrues friction for that (tenant, user)
// primitive. A nil executor disables the endpoint (503), same as a nil client.
//
// tracer instruments the native-tool Gollem agent (ADR-0012 follow-up) with
// hierarchical OTel spans; it may be nil (the tracing hook then falls back to
// the global OTel provider, a no-op unless one is configured).
func RegisterAgent(api huma.API, client llm.Client, registry *tools.Registry, executor agentloop.ToolExecutor, typed *TypedToolExecutor, detector *primitives.FrictionDetector, tracer oteltrace.TracerProvider, maxSteps int) {
	huma.Register(api, huma.Operation{
		OperationID: "chat-agent",
		Method:      httpMethodPost,
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
		if client == nil || registry == nil || executor == nil {
			return nil, huma.Error503ServiceUnavailable("agent mode is not configured")
		}

		// Per-request executor so friction is scoped to this (tenant, user): the
		// injected host executor runs the tool, wrapped in a friction recorder
		// when a detector is configured.
		exec := executor
		if detector != nil {
			exec = frictionExecutor{inner: executor, detector: detector, tenant: in.TenantID, user: in.UserID}
		}

		// ADR-0012: prefer the provider's NATIVE tool-calling when the client
		// implements the optional llm.ToolCaller capability; otherwise drive the
		// homegrown structured-output loop over the plain streaming Client. Both
		// branches enforce the identical double scope-gate (only scope-permitted
		// tools are offered, and an unregistered/hallucinated tool name can never
		// execute) and run tools through the same scope-/friction-wrapped
		// executor — so switching paths never changes the isolation guarantee.
		// Gate tools by the AUTHORITATIVE scopes (effectiveScopes): under a real
		// authenticator the verified principal's scopes win, so a client cannot
		// self-grant a tool via body scopes; under NoAuth/no middleware the body
		// scopes are the fallback. Both dispatch paths below share this authority.
		scopes := effectiveScopes(ctx, in.Body.Scopes)

		var steps []AgentStep
		if tc, ok := client.(llm.ToolCaller); ok {
			steps, err = runNativeToolAgent(ctx, tc, tracer, registry, exec, typed, scopes, in.Body.Prompt, maxSteps)
		} else {
			// Fallback (no native tool-calling): the combined executor routes
			// typed tools through the typed dispatcher (best-effort over the
			// string map) and untyped tools through the friction-wrapped executor.
			loopExec := agentloop.ToolExecutor(combinedMapExecutor{typed: typed, fallback: exec})
			loop := &agentloop.Loop{Client: client, Registry: registry, Executor: loopExec, MaxSteps: maxSteps}
			var loopSteps []agentloop.StepResult
			loopSteps, err = loop.Run(ctx, scopes, in.Body.Prompt)
			steps = agentStepsFromLoop(loopSteps)
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("agent loop failed", err)
		}

		out := &AgentOutput{}
		out.Body.TenantID = in.TenantID
		out.Body.Steps = steps
		return out, nil
	})
}

// agentStepsFromLoop maps the homegrown agentloop's StepResult shape into the
// endpoint's AgentStep output contract.
func agentStepsFromLoop(loopSteps []agentloop.StepResult) []AgentStep {
	steps := make([]AgentStep, 0, len(loopSteps))
	for _, s := range loopSteps {
		steps = append(steps, AgentStep{
			Tool:   s.Decision.Tool,
			Args:   s.Decision.Args,
			Result: s.Result,
		})
	}
	return steps
}

// nativeToolSystemPrompt is the base instruction handed to a ToolCaller. Unlike
// the agentloop path, the tools are conveyed structurally (as []llm.ToolSpec via
// the provider's native tool-calling API), not embedded in the prompt, so this
// only frames the task.
const nativeToolSystemPrompt = "You are an assistant that may call the provided tools to help answer the user. " +
	"Call a tool only when it helps; otherwise answer directly."

// runNativeToolAgent drives the provider's native tool-calling through a Gollem
// agent (ADR-0012 follow-up), restricting the model to the tools the caller's
// scopes permit. It offers only Registry.Permitted(scopes) tools as Gollem
// function tools and delegates the run to llm.RunToolAgent (which keeps all
// gollem-core usage inside internal/llm), then maps the recorded llm.ToolSteps
// into the endpoint's AgentStep contract.
//
// The double scope-gate is preserved: only Permitted(scopes) tools become
// ToolSpecs/function-tools (offer-only-permitted), and because only those
// permitted tools are registered on the agent, an unregistered/hallucinated
// tool name lands in the agent's unknownCalls (a retry) and can never reach the
// executor — the re-check half is structural, not a runtime re-validation. There
// is no path from "not offered" to "executed".
func runNativeToolAgent(ctx context.Context, tc llm.ToolCaller, tracer oteltrace.TracerProvider, registry *tools.Registry, executor agentloop.ToolExecutor, typed *TypedToolExecutor, scopes []string, prompt string, maxSteps int) ([]AgentStep, error) {
	permitted := registry.Permitted(scopes)
	specs := make([]llm.ToolSpec, 0, len(permitted))
	for _, t := range permitted {
		// Carry the tool's argument schema (set by WithTypedTool) so the provider
		// advertises the real shape; empty for untyped tools (ADR-0016 lever #1).
		specs = append(specs, llm.ToolSpec{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}

	// run receives the model's RAW JSON args. A typed tool decodes+validates them
	// against its arg struct; a decode/validation failure (*ToolArgsError) is
	// reflected back to the model AS THE TOOL RESULT so it self-corrects within
	// the step budget (never a coerced call, SDD R4). An untyped tool flattens the
	// args to the string map its ToolExecutor consumes. The double scope-gate is
	// unchanged: only permitted tools are offered, so run only ever sees an
	// offered tool name.
	run := func(ctx context.Context, name, argsJSON string) (string, error) {
		if typed.Has(name) {
			res, rerr := typed.Run(ctx, name, json.RawMessage(argsJSON))
			var ae *ToolArgsError
			if errors.As(rerr, &ae) {
				return "invalid arguments: " + ae.Err.Error() + " — please correct and retry.", nil
			}
			return res, rerr
		}
		return executor.Execute(ctx, name, decodeStringArgs(argsJSON))
	}

	toolSteps, err := llm.RunToolAgent(ctx, tc, tracer, nativeToolSystemPrompt, prompt, specs, run, maxSteps)
	if err != nil {
		return nil, err
	}

	steps := make([]AgentStep, 0, len(toolSteps))
	for _, ts := range toolSteps {
		steps = append(steps, AgentStep{Tool: ts.Tool, Args: ts.Args, Result: ts.Result})
	}
	return steps, nil
}
