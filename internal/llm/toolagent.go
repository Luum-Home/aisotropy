// ADR-0012 follow-up (2): a native-tool Gollem agent built on top of the
// llm.ToolCaller bridge (see GollemModel.requestWithTools). RunToolAgent mounts
// a core.Agent[string] over NewGollemModel(client) with one Gollem function
// tool per offered ToolSpec; when the wrapped client implements ToolCaller the
// agent drives the provider's NATIVE tool-calling API, and each executed tool
// is recorded as a ToolStep. It reuses the exact same ADR-0010 cost tracker +
// guardrails/interceptors GollemReflect uses, and attaches ext/otel tracing.
//
// All gollem-core usage for the agent endpoint is confined here (internal/llm),
// so internal/api never imports gollem core.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/fugue-labs/gollem/core"
	"github.com/fugue-labs/gollem/ext/otel"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// maxToolStepsGuardrailName names the turn guardrail that bounds the agent to
// maxSteps model turns. It is used both to install the guardrail and to detect
// its abort (a *core.GuardrailError carrying this name), so a run that simply
// did not converge within maxSteps is treated as a graceful stop, not a failure.
const maxToolStepsGuardrailName = "max-tool-steps"

// ToolStep is one executed tool during a RunToolAgent run: the tool the model
// chose, the decoded string-keyed arguments it passed, and the executor's
// result. Only tools that actually executed produce a ToolStep — a turn where
// the model answers directly (no tool) records nothing.
type ToolStep struct {
	Tool   string
	Args   map[string]string
	Result string
}

// RunToolAgent runs a bounded native-tool agent: it builds a core.Agent[string]
// over NewGollemModel(client), registering one Gollem function tool per offered
// ToolSpec whose handler decodes the model's JSON arguments into a
// map[string]string and calls exec, recording a ToolStep. It attaches the
// shared ADR-0010 guardrails/cost tracker and ext/otel tracing (tracer may be
// nil — the tracing hook then falls back to the global OTel provider, a no-op
// unless one is configured).
//
// SCOPE GATE (structural): only the ToolSpecs passed in are registered as
// function tools, so a tool name the model returns that was NOT offered is
// routed by the Agent to unknownCalls (a retry prompt), never to exec. There is
// no path from "not offered" to "executed" — exec only ever sees an offered
// tool. Callers MUST therefore pass only scope-permitted tools.
//
// maxSteps bounds the number of model-request turns via core.MaxTurns (a turn
// guardrail) so a run cannot loop unbounded. A run that has not converged within
// maxSteps turns stops GRACEFULLY: RunToolAgent returns the ToolSteps taken so
// far with a nil error (mirroring the pre-Gollem loop), NOT a failure — so a
// non-converging agent yields partial results, not an error. Any OTHER error
// (provider failure, breaker open, a different guardrail, a tool error) is a
// real failure and is returned with the steps recorded before it. maxSteps <= 0
// leaves the run unbounded by this guardrail (the agent's own usage limits still
// apply).
func RunToolAgent(
	ctx context.Context,
	client Client,
	tracer oteltrace.TracerProvider,
	system, prompt string,
	tools []ToolSpec,
	exec func(ctx context.Context, name string, argsJSON string) (string, error),
	maxSteps int,
) ([]ToolStep, error) {
	steps, _, err := RunToolAgentWithOutput(ctx, client, tracer, system, prompt, tools, exec, maxSteps)
	return steps, err
}

// RunToolAgentWithOutput is RunToolAgent plus the agent's FINAL free-text answer
// (the value core.Agent[string].Run returns after the last turn). RunToolAgent
// delegates to it and discards that text; callers that pipe one agent's answer
// into another — the graph executor's edges (ADR-0060), where a node's Output is
// exactly what a downstream node reads — use this variant. The output is "" when
// a run stops on the max-steps guardrail (a graceful, non-converged stop yields
// the recorded steps and no final text, mirroring RunToolAgent's contract) or on
// any other error. The []ToolStep, scope-gate, guardrail, and cost/tracing
// semantics are identical to RunToolAgent.
func RunToolAgentWithOutput(
	ctx context.Context,
	client Client,
	tracer oteltrace.TracerProvider,
	system, prompt string,
	tools []ToolSpec,
	exec func(ctx context.Context, name string, argsJSON string) (string, error),
	maxSteps int,
) ([]ToolStep, string, error) {
	model := NewGollemModel(client)
	tracker := core.NewCostTracker(gollemModelPricing)

	var (
		mu    sync.Mutex
		steps []ToolStep
	)

	funcTools := make([]core.Tool, 0, len(tools))
	for _, spec := range tools {
		name := spec.Name // capture per tool for the handler closure
		var handler core.ToolHandler = func(ctx context.Context, _ *core.RunContext, argsJSON string) (any, error) {
			// exec receives the model's RAW JSON args so a typed tool can decode
			// nested/typed fields (ADR-0016 lever #1); the caller owns decoding
			// and validation. ToolStep.Args keeps a best-effort flattened view for
			// the endpoint's display contract only.
			result, eerr := exec(ctx, name, argsJSON)
			if eerr != nil {
				return nil, eerr
			}
			display, _ := decodeToolArgs(argsJSON)
			mu.Lock()
			steps = append(steps, ToolStep{Tool: name, Args: display, Result: result})
			mu.Unlock()
			return result, nil
		}
		funcTools = append(funcTools, core.Tool{
			Definition: core.ToolDefinition{
				Name:             name,
				Description:      spec.Description,
				Kind:             core.ToolKindFunction,
				ParametersSchema: toolParametersSchema(spec.Parameters),
			},
			Handler: handler,
		})
	}

	opts := []core.AgentOption[string]{
		core.WithSystemPrompt[string](system),
		core.WithTools[string](funcTools...),
		core.WithHooks[string](otelTracingHooks(tracer)),
	}
	if maxSteps > 0 {
		opts = append(opts, core.WithTurnGuardrail[string](maxToolStepsGuardrailName, core.MaxTurns(maxSteps)))
	}
	opts = append(opts, gollemGuardrailOptions[string](tracker)...)

	agent := core.NewAgent[string](model, opts...)

	result, err := agent.Run(ctx, prompt)
	if err != nil {
		mu.Lock()
		recorded := append([]ToolStep(nil), steps...)
		mu.Unlock()
		// A max-tool-steps abort means the agent did not converge within maxSteps
		// turns — that is a graceful stop (return the steps taken so far with no
		// error), mirroring the pre-Gollem loop, NOT a request failure. Any other
		// error (provider down, breaker open, guardrail rejection, tool error) is
		// a real failure and propagates. On either path the final text is "" (Run
		// returned no answer), so the graceful stop yields steps-only with no text.
		var gErr *core.GuardrailError
		if errors.As(err, &gErr) && gErr.GuardrailName == maxToolStepsGuardrailName {
			return recorded, "", nil
		}
		return recorded, "", err
	}

	mu.Lock()
	recorded := append([]ToolStep(nil), steps...)
	mu.Unlock()
	return recorded, result.Output, nil
}

// otelTracingHooks builds the ext/otel tracing hook. When tracer is non-nil it
// pins that provider; otherwise the hook uses the global OTel provider (a no-op
// when none is configured).
func otelTracingHooks(tracer oteltrace.TracerProvider) core.Hook {
	tracingOpts := []otel.TracingOption{otel.WithCaptureToolArgs(true)}
	if tracer != nil {
		tracingOpts = append(tracingOpts, otel.WithTracerProvider(tracer))
	}
	return otel.TracingHooks(tracingOpts...)
}

// toolParametersSchema turns a ToolSpec's JSON-Schema bytes into the core.Schema
// (map[string]any) a Gollem function tool advertises. When the spec carries no
// schema (untyped tool), it falls back to the permissive open object the
// ADR-0012 minimal seam used, so an untyped tool still accepts free-form args.
func toolParametersSchema(params json.RawMessage) core.Schema {
	if len(params) > 0 {
		var s core.Schema
		if err := json.Unmarshal(params, &s); err == nil && len(s) > 0 {
			return s
		}
	}
	return core.Schema{
		jsonKeyType:            jsonSchemaTypeObject,
		"additionalProperties": map[string]any{jsonKeyType: "string"},
	}
}

// decodeToolArgs turns the model's JSON tool arguments into the string-keyed
// map llm tool executors consume. Empty / "{}" / "null" args decode to an empty
// map. Non-string scalar values are stringified so a model that emits e.g. a
// number still yields a usable arg.
func decodeToolArgs(argsJSON string) (map[string]string, error) {
	args := map[string]string{}
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return args, nil
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return nil, err
	}
	for k, v := range raw {
		switch s := v.(type) {
		case string:
			args[k] = s
		default:
			args[k] = fmt.Sprintf("%v", v)
		}
	}
	return args, nil
}
