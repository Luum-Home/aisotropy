package api

import (
	"context"
	"fmt"
	"strings"

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
//
// FinalAnswer is ADDITIVE (ADR / constitutional backward-compat gate): a new
// OPTIONAL field with a stable JSON tag and `omitempty`, so a run that produces
// no natural-language answer (only tool steps, or a non-converged stop) serializes
// exactly as before this field existed — no existing field was renamed or removed.
// It carries the model's final free-text reply, which the agent endpoint previously
// discarded: when the model answers WITHOUT calling a tool (e.g. "what's the capital
// of Australia" → "Canberra" with the calculator tool unused), Steps is empty and the
// answer now lands here instead of being lost. It is also populated when the model
// synthesizes a closing answer AFTER tool calls; Steps still records those calls
// unchanged. Empty when the run only executed tools without a closing answer, or
// stopped on the max-steps guardrail without converging.
type AgentOutput struct {
	Body struct {
		TenantID    string      `json:"tenant_id" doc:"Tenant that served the request"`
		Steps       []AgentStep `json:"steps" doc:"Tool decisions and results, in order"`
		FinalAnswer string      `json:"final_answer,omitempty" doc:"Model's final natural-language answer, when it produced one (e.g. it answered directly without calling a tool, or summarized after tool calls). Empty when the run only executed tools or did not converge."`
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
//
// guard applies the INPUT guardrail (redact + detect ALWAYS; block opt-in): the
// prompt is redacted before it reaches the tool loop or the model, so a secret
// never reaches the provider, and a suspicious injection verdict is annotated
// (or rejected when BlockInjection is enabled). The agent route does not run the
// output guardrail — its response is a structured list of tool decisions, not
// free-form model prose. A nil guard disables guardrails entirely.
//
// store is the OPTIONAL per-(tenant, user) primitives.Store that CLOSES THE READ
// SIDE of the behavioral self-improvement loop (Phase 6). It is variadic purely
// so the wiring in aisotropy/app.go is an additive one-argument handoff: at most
// one store is expected, and omitting it (or passing nil) is nil-safe — the tool
// guidance the model sees is then byte-for-byte the base registry metadata,
// exactly as before this seam existed. When a store IS supplied, the friction-
// rewritten primitive for each permitted tool (its improved Description and
// accumulated corrective Examples) is overlaid on the base tool metadata for THIS
// request's (tenant, user) before the tools are presented to the model — so the
// LLM finally sees the improved guidance the sleep cycle produced. See
// overlayRegistry for the literal (non-templated) overlay mechanism and its
// strict (tenant, user) isolation.
func RegisterAgent(api huma.API, client llm.Client, registry *tools.Registry, executor agentloop.ToolExecutor, typed *TypedToolExecutor, detector *primitives.FrictionDetector, tracer oteltrace.TracerProvider, maxSteps int, guard *Guard, store ...*primitives.Store) {
	// Resolve the optional store: at most one is meaningful; a nil or omitted
	// store leaves the read side of the loop closed-off (current behavior).
	var primStore *primitives.Store
	if len(store) > 0 {
		primStore = store[len(store)-1]
	}
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

		// Input guardrail (redact + detect ALWAYS; block opt-in). forwarded is
		// the REDACTED prompt: it — never the raw prompt — drives the tool loop
		// and reaches the model, so a secret never leaves the process. A
		// suspicious injection verdict is annotated by default and only rejected
		// when BlockInjection is enabled.
		forwarded, blocked := guard.checkInput(ctx, in.TenantID, in.UserID, in.Body.Prompt)
		if blocked {
			return nil, huma.Error400BadRequest("request blocked by input guardrail (possible prompt injection)")
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

		// CLOSE THE READ SIDE of the self-improvement loop: overlay the
		// friction-rewritten primitive guidance for THIS (tenant, user) onto the
		// tool metadata the model is about to see. Scope gating is untouched — the
		// overlay copies each permitted tool (RequiredScope included), so both
		// dispatch paths below gate exactly as before; only the Description text is
		// augmented. With no store this is the original registry object, so the
		// model sees byte-for-byte the same guidance as before this seam.
		activeRegistry := registry
		if primStore != nil {
			activeRegistry = overlayRegistry(primStore, in.TenantID, in.UserID, registry, scopes)
		}

		var (
			steps  []AgentStep
			answer string
		)
		if tc, ok := client.(llm.ToolCaller); ok {
			steps, answer, err = runNativeToolAgent(ctx, tc, tracer, activeRegistry, exec, typed, scopes, forwarded, maxSteps)
		} else {
			// Fallback (no native tool-calling): the combined executor routes
			// typed tools through the typed dispatcher (best-effort over the
			// string map) and untyped tools through the friction-wrapped executor.
			loopExec := agentloop.ToolExecutor(combinedMapExecutor{typed: typed, fallback: exec})
			loop := &agentloop.Loop{Client: client, Registry: activeRegistry, Executor: loopExec, MaxSteps: maxSteps}
			var loopSteps []agentloop.StepResult
			loopSteps, err = loop.Run(ctx, scopes, forwarded)
			steps = agentStepsFromLoop(loopSteps)
			// The fallback loop conveys its final free-text answer via the
			// terminating no-tool Decision's Answer (agentloop.FinalAnswer), the
			// StreamChat-only analog of the native path's result.Output.
			answer = agentloop.FinalAnswer(loopSteps)
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("agent loop failed", err)
		}

		out := &AgentOutput{}
		out.Body.TenantID = in.TenantID
		out.Body.Steps = steps
		out.Body.FinalAnswer = answer
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
// function tools and delegates the run to llm.RunToolAgentWithOutput (which keeps
// all gollem-core usage inside internal/llm), then maps the recorded llm.ToolSteps
// into the endpoint's AgentStep contract AND returns the agent's final free-text
// answer (the second return value gollem already computes at
// internal/llm/toolagent.go:106-210 as core.Agent[string].Run's result.Output —
// this is wiring an existing gollem output through, not a new capability; see
// .claude/rules/dependency-reuse-first.md). The answer is "" when the model only
// executed tools without a closing answer, or the run stopped on the max-steps
// guardrail without converging.
//
// The double scope-gate is preserved: only Permitted(scopes) tools become
// ToolSpecs/function-tools (offer-only-permitted), and because only those
// permitted tools are registered on the agent, an unregistered/hallucinated
// tool name lands in the agent's unknownCalls (a retry) and can never reach the
// executor — the re-check half is structural, not a runtime re-validation. There
// is no path from "not offered" to "executed".
func runNativeToolAgent(ctx context.Context, tc llm.ToolCaller, tracer oteltrace.TracerProvider, registry *tools.Registry, executor agentloop.ToolExecutor, typed *TypedToolExecutor, scopes []string, prompt string, maxSteps int) ([]AgentStep, string, error) {
	permitted := registry.Permitted(scopes)
	specs := make([]llm.ToolSpec, 0, len(permitted))
	for _, t := range permitted {
		// Carry the tool's argument schema (set by WithTypedTool) so the provider
		// advertises the real shape; empty for untyped tools (ADR-0016 lever #1).
		specs = append(specs, llm.ToolSpec{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}

	// run receives the model's RAW JSON args (see NewAgentToolExec): a typed tool
	// decodes+validates against its arg struct — a *ToolArgsError is reflected back
	// AS THE TOOL RESULT so the model self-corrects within the step budget (never a
	// coerced call, SDD R4) — and an untyped tool flattens to the string map its
	// ToolExecutor consumes. The double scope-gate is unchanged: only permitted
	// tools are offered, so run only ever sees an offered tool name.
	run := NewAgentToolExec(typed, executor)

	toolSteps, answer, err := llm.RunToolAgentWithOutput(ctx, tc, tracer, nativeToolSystemPrompt, prompt, specs, run, maxSteps)
	if err != nil {
		return nil, "", err
	}

	steps := make([]AgentStep, 0, len(toolSteps))
	for _, ts := range toolSteps {
		steps = append(steps, AgentStep{Tool: ts.Tool, Args: ts.Args, Result: ts.Result})
	}
	return steps, answer, nil
}

// overlayRegistry returns a registry whose tools carry the per-(tenant, user)
// friction-rewritten primitive guidance overlaid on top of the base tool
// metadata — the read side of the self-improvement loop that internal/primitives
// writes and the sleep cycle rewrites. It starts from base.Permitted(scopes) (so
// scope gating is applied exactly once, upstream) and, for each permitted tool,
// reads store.Get(tenant, user, tool.Name); when a primitive exists, its improved
// Description and accumulated Examples are appended to the tool's Description via
// overlayDescription. Every returned tool keeps its Name, RequiredScope and
// Parameters, so a subsequent Permitted(scopes)/PromptFragment(scopes) on the
// returned registry gates and renders identically to the base — only the
// Description text differs.
//
// Isolation (constitutional gate): the ONLY store read is
// store.Get(tenant, user, ·) for the request's own identity. A primitive
// rewritten for a different (tenant, user) is never keyed here, so user A's
// guidance can never surface in user B's tool metadata.
func overlayRegistry(store *primitives.Store, tenant, user string, base *tools.Registry, scopes []string) *tools.Registry {
	permitted := base.Permitted(scopes)
	overlaid := make([]tools.Tool, len(permitted))
	for i, t := range permitted {
		overlaid[i] = t
		p, ok := store.Get(tenant, user, t.Name)
		if !ok {
			continue
		}
		overlaid[i].Description = overlayDescription(t.Description, p)
	}
	return tools.NewRegistry(overlaid...)
}

// overlayDescription composes the guidance string the model sees for one tool:
// the base Description, then the primitive's improved Description (if any, and if
// it is not already exactly the base), then each accumulated corrective Example,
// joined by newlines. It is a LITERAL append — no templating, no placeholder
// substitution — so the text the sleep cycle wrote is exactly the text the model
// reads. An empty primitive (friction recorded but not yet consolidated: empty
// Description, no Examples) contributes nothing, so the result equals the base.
func overlayDescription(base string, p primitives.Primitive) string {
	parts := make([]string, 0, 2+len(p.Examples))
	if base != "" {
		parts = append(parts, base)
	}
	if p.Description != "" && p.Description != base {
		parts = append(parts, p.Description)
	}
	for _, ex := range p.Examples {
		if ex != "" {
			parts = append(parts, ex)
		}
	}
	return strings.Join(parts, "\n")
}
