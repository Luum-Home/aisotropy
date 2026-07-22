package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fugue-labs/gollem/core"
)

// gollemTestOutput is the output struct used in these tests — analogous to
// ChatOutput.Body (internal/api/chat.go) but minimal, without pulling in the
// api package here.
type gollemTestOutput struct {
	Reply string `json:"reply"`
}

// fakeClient implements llm.Client with canned responses in sequence, no
// network or credentials needed — allows exercising GollemModel/GollemReflect
// (our real integration code) end-to-end and deterministically.
type fakeClient struct {
	responses []string
	calls     int
}

func (f *fakeClient) Name() string { return "fake" }

func (f *fakeClient) StreamChat(_ context.Context, _, _ string) (<-chan Token, error) {
	idx := f.calls
	f.calls++
	text := f.responses[len(f.responses)-1]
	if idx < len(f.responses) {
		text = f.responses[idx]
	}

	out := make(chan Token, 2)
	out <- Token{Text: text}
	out <- Token{Done: true}
	close(out)
	return out, nil
}

// TestGollemReflectRepairsSchemaViaAgentInternalRetry verifies, through our
// real adapter (GollemModel + GollemReflect, gollem.go), that core.Agent[T]
// repairs an invalid JSON on its own: the Client's first response is not
// JSON, Agent[T] retries internally with the exact typing error
// (RetryPromptPart) without going through the homegrown Reflect[T] loop or
// RunWithReflection — this is the mechanism described in
// docs/components/gollem-orchestrator.md and deferred to Phase 2 by ADR-0008.
func TestGollemReflectRepairsSchemaViaAgentInternalRetry(t *testing.T) {
	client := &fakeClient{responses: []string{
		"this is not JSON at all",
		`{"reply":"ok"}`,
	}}

	noopValidate := func(context.Context, gollemTestOutput) (string, error) { return "", nil }

	result, err := GollemReflect[gollemTestOutput](context.Background(), client, "system prompt", "hello", noopValidate, 3)
	if err != nil {
		t.Fatalf("GollemReflect returned unexpected error: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected Accepted=true")
	}
	if result.Output.Reply != "ok" {
		t.Fatalf("Reply = %q, expected %q", result.Output.Reply, "ok")
	}
	if result.Iterations != 1 {
		t.Fatalf("Iterations = %d, expected 1 (schema repair is internal to Agent.Run, not a RunWithReflection cycle)", result.Iterations)
	}
	if client.calls != 2 {
		t.Fatalf("client.calls = %d, expected 2 (1 invalid attempt + 1 repaired, via Agent[T]'s internal retry)", client.calls)
	}
}

// TestGollemReflectRunsBusinessValidationLoop verifies the outer
// RunWithReflection cycle: the first response is mechanically valid JSON but
// invalid by business rule (empty reply); the validator rejects it with
// feedback, Gollem retries with that feedback, and the second response
// corrects it.
func TestGollemReflectRunsBusinessValidationLoop(t *testing.T) {
	client := &fakeClient{responses: []string{
		`{"reply":""}`,
		`{"reply":"ok"}`,
	}}

	businessValidate := func(_ context.Context, out gollemTestOutput) (string, error) {
		if out.Reply == "" {
			return "reply cannot be empty", nil
		}
		return "", nil
	}

	result, err := GollemReflect[gollemTestOutput](context.Background(), client, "system prompt", "hello", businessValidate, 3)
	if err != nil {
		t.Fatalf("GollemReflect returned unexpected error: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected Accepted=true after the second iteration")
	}
	if result.Output.Reply != "ok" {
		t.Fatalf("Reply = %q, expected %q", result.Output.Reply, "ok")
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d, expected 2 (1 business rejection + 1 correction)", result.Iterations)
	}
	if client.calls != 2 {
		t.Fatalf("client.calls = %d, expected 2", client.calls)
	}
}

// TestGollemAgentReflectionWithCoreTestModel exercises Gollem's reflection
// flow against core.TestModel — the official mock from
// github.com/fugue-labs/gollem/core (no network, no API key, not going
// through our GollemModel adapter) — to independently verify that the real
// Agent[T] API behaves as ADR-0008 describes: by default (OutputModeTool,
// struct T) Agent itself repairs invalid JSON by retrying with the exact
// error before RunWithReflection comes into play.
func TestGollemAgentReflectionWithCoreTestModel(t *testing.T) {
	model := core.NewTestModel(
		core.ToolCallResponse(core.DefaultOutputToolName, `{not-json`),
		core.ToolCallResponse(core.DefaultOutputToolName, `{"reply":"ok"}`),
	)

	agent := core.NewAgent[gollemTestOutput](model, core.WithMaxRetries[gollemTestOutput](3))

	result, err := agent.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("agent.Run returned unexpected error: %v", err)
	}
	if result.Output.Reply != "ok" {
		t.Fatalf("Reply = %q, expected %q", result.Output.Reply, "ok")
	}
	if len(model.Calls()) != 2 {
		t.Fatalf("len(model.Calls()) = %d, expected 2 (Agent[T]'s internal retry after invalid JSON)", len(model.Calls()))
	}
}

// TestExtractJSONObject verifies that the adapter tolerates surrounding
// prose/markdown backticks around the JSON despite the explicit instruction
// not to add them — necessary because our Client has no native tool-calling
// and we depend on the model respecting the prompt.
func TestExtractJSONObject(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"clean", `{"reply":"ok"}`, `{"reply":"ok"}`},
		{"with surrounding prose", "Sure, here it is: {\"reply\":\"ok\"} hope that helps", `{"reply":"ok"}`},
		{"with markdown fences", "```json\n{\"reply\":\"ok\"}\n```", `{"reply":"ok"}`},
		{"with nested braces", `{"a":{"b":1}}`, `{"a":{"b":1}}`},
		{"with braces inside a string", `{"reply":"has } a brace"}`, `{"reply":"has } a brace"}`},
		{"no json", "there is nothing here", "there is nothing here"},
		{"unterminated object", `{"reply":"unterminated`, `{"reply":"unterminated`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractJSONObject(tc.in)
			if got != tc.want {
				t.Fatalf("extractJSONObject(%q) = %q, expected %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestReflectAutoFallsBackToOwnOnGollemFailure verifies ADR-0008's fallback
// contract: if the "own" backend is forced via AISOTROPY_REFLECTION_BACKEND,
// ReflectAuto uses Reflect[T] (reflect.go) instead of Gollem, without
// touching the GollemModel path at all.
func TestReflectAutoFallsBackToOwnOnGollemFailure(t *testing.T) {
	t.Setenv("AISOTROPY_REFLECTION_BACKEND", "own")

	client := &fakeClient{responses: []string{`{"reply":"ok"}`}}
	decode := func(raw string) (gollemTestOutput, error) {
		var out gollemTestOutput
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return gollemTestOutput{}, err
		}
		return out, nil
	}

	out, attempts, backend, _, err := ReflectAuto[gollemTestOutput](context.Background(), client, "system", "hello", decode, nil, 3)
	if err != nil {
		t.Fatalf("ReflectAuto returned unexpected error: %v", err)
	}
	if backend != ReflectionBackendOwn {
		t.Fatalf("backend = %q, expected %q", backend, ReflectionBackendOwn)
	}
	if out.Reply != "ok" {
		t.Fatalf("Reply = %q, expected %q", out.Reply, "ok")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, expected 1", attempts)
	}
}

// TestGollemReflectStillProducesValidOutputWithGuardrailsAttached verifies
// that attaching the ADR-0010 "ADOPT NOW" cost tracker + guardrail +
// interceptor AgentOptions (gollemGuardrailOptions) to the Agent[T]
// GollemReflect builds does not interfere with the existing schema-repair /
// business-validation pipeline — the behavior-level check called for when
// asserting an individual option in isolation is impractical through the
// real GollemModel adapter (see the gollemModelPricing comment in
// gollem.go).
func TestGollemReflectStillProducesValidOutputWithGuardrailsAttached(t *testing.T) {
	client := &fakeClient{responses: []string{`{"reply":"ok"}`}}
	noopValidate := func(context.Context, gollemTestOutput) (string, error) { return "", nil }

	result, err := GollemReflect[gollemTestOutput](context.Background(), client, "system prompt", "hello", noopValidate, 3)
	if err != nil {
		t.Fatalf("GollemReflect returned unexpected error: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected Accepted=true")
	}
	if result.Output.Reply != "ok" {
		t.Fatalf("Reply = %q, expected %q", result.Output.Reply, "ok")
	}
}

// TestGollemCostTrackerRecordsUsageWhenPresent proves the real
// core.WithCostTracker wiring — via gollemGuardrailOptions, the exact
// options GollemReflect attaches — records cost once a response carries
// token usage, mirroring Gollem's own core.TestCostTracker_AgentIntegration
// pattern (cost_test.go). This builds the agent directly over
// core.NewTestModel (not through GollemReflect/fakeClient) because
// GollemModel does not yet populate ModelResponse.Usage — a separate,
// pre-existing gap documented on gollemModelPricing — and this test's job is
// to isolate and prove the AgentOption wiring itself, not that gap.
func TestGollemCostTrackerRecordsUsageWhenPresent(t *testing.T) {
	resp := core.ToolCallResponse(core.DefaultOutputToolName, `{"reply":"ok"}`)
	resp.Usage = core.Usage{InputTokens: 100, OutputTokens: 50}
	model := core.NewTestModel(resp)

	pricing := map[string]core.ModelPricing{
		model.ModelName(): {InputTokenCost: 0.000003, OutputTokenCost: 0.000015},
	}
	tracker := core.NewCostTracker(pricing)

	opts := append([]core.AgentOption[gollemTestOutput]{
		core.WithMaxRetries[gollemTestOutput](3),
	}, gollemGuardrailOptions[gollemTestOutput](tracker)...)
	agent := core.NewAgent[gollemTestOutput](model, opts...)

	result, err := agent.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("agent.Run returned unexpected error: %v", err)
	}
	if result.Output.Reply != "ok" {
		t.Fatalf("Reply = %q, expected %q", result.Output.Reply, "ok")
	}
	if tracker.TotalCost() <= 0 {
		t.Fatalf("expected positive TotalCost after a run with usage set, got %f", tracker.TotalCost())
	}
	if result.Cost == nil || result.Cost.TotalCost <= 0 {
		t.Fatalf("expected RunResult.Cost to be populated by the attached tracker, got %+v", result.Cost)
	}
}

// TestGollemModelStubProducesRealUsageAndCost closes the end-to-end gap the
// manual-injection test above leaves open. Instead of a TestModel with a
// hand-set Usage, it drives the REAL GollemModel over the StubClient and proves
// the provider usage captured from the stub's terminal Token flows all the way
// into a priced CostTracker as non-zero cost — the full
// stub -> generateFull -> toModelResponse -> ModelResponse.Usage -> CostTracker
// chain. Since pricing is fixed and positive, TotalCost > 0 holds if and only if
// non-zero token counts reached the tracker: this is the logical proof that the
// "usage is always 0" defect is fixed on the real adapter path.
//
// gollemModelPricing is intentionally empty in production (an accounting degrade
// documented on that var: real usage counts flow, but dollar cost stays 0 until
// a per-model price table is wired), so this test supplies its own pricing for
// the stub model name to exercise the wiring end to end.
func TestGollemModelStubProducesRealUsageAndCost(t *testing.T) {
	model := NewGollemModel(NewStubClient())

	pricing := map[string]core.ModelPricing{
		model.ModelName(): {InputTokenCost: 0.000003, OutputTokenCost: 0.000015},
	}
	tracker := core.NewCostTracker(pricing)

	opts := append([]core.AgentOption[gollemTestOutput]{
		core.WithMaxRetries[gollemTestOutput](3),
	}, gollemGuardrailOptions[gollemTestOutput](tracker)...)
	agent := core.NewAgent[gollemTestOutput](model, opts...)

	result, err := agent.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("agent.Run over GollemModel(stub): %v", err)
	}
	if result.Output.Reply == "" {
		t.Fatalf("expected a non-empty structured reply from the stub, got empty")
	}
	if tracker.TotalCost() <= 0 {
		t.Fatalf("expected positive TotalCost from real GollemModel(stub) usage, got %f "+
			"(non-zero token counts did not reach the tracker — usage plumbing broken)", tracker.TotalCost())
	}
}

// TestGollemRedactPIIInterceptorScrubsEmailBeforeModelSeesIt verifies that
// the core.RedactPII message interceptor (ADR-0010's PII-redaction wiring)
// actually runs on the message pipeline of an Agent[T] built with
// gollemGuardrailOptions, checked against core.TestModel.Calls() — which
// records the exact messages the model received, i.e. after interceptors
// ran.
func TestGollemRedactPIIInterceptorScrubsEmailBeforeModelSeesIt(t *testing.T) {
	model := core.NewTestModel(core.ToolCallResponse(core.DefaultOutputToolName, `{"reply":"ok"}`))
	tracker := core.NewCostTracker(nil)

	agent := core.NewAgent[gollemTestOutput](model, gollemGuardrailOptions[gollemTestOutput](tracker)...)

	_, err := agent.Run(context.Background(), "contact me at user@example.com please")
	if err != nil {
		t.Fatalf("agent.Run returned unexpected error: %v", err)
	}

	calls := model.Calls()
	if len(calls) == 0 {
		t.Fatal("expected at least one recorded model call")
	}

	var sawRedactionMarker bool
	for _, msg := range calls[0].Messages {
		req, ok := msg.(core.ModelRequest)
		if !ok {
			continue
		}
		for _, part := range req.Parts {
			up, ok := part.(core.UserPromptPart)
			if !ok {
				continue
			}
			if strings.Contains(up.Content, "user@example.com") {
				t.Fatalf("expected the email to be redacted before reaching the model, got raw content: %q", up.Content)
			}
			if strings.Contains(up.Content, "[REDACTED]") {
				sawRedactionMarker = true
			}
		}
	}
	if !sawRedactionMarker {
		t.Fatal("expected to find a [REDACTED] marker in the user prompt the model received")
	}
}

// TestReflectAutoDefaultsToGollem verifies that, without forcing
// AISOTROPY_REFLECTION_BACKEND, ReflectAuto uses Gollem by default (ADR-0008).
func TestReflectAutoDefaultsToGollem(t *testing.T) {
	client := &fakeClient{responses: []string{`{"reply":"ok"}`}}
	decode := func(raw string) (gollemTestOutput, error) {
		var out gollemTestOutput
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return gollemTestOutput{}, err
		}
		return out, nil
	}

	out, _, backend, _, err := ReflectAuto[gollemTestOutput](context.Background(), client, "system", "hello", decode, nil, 3)
	if err != nil {
		t.Fatalf("ReflectAuto returned unexpected error: %v", err)
	}
	if backend != ReflectionBackendGollem {
		t.Fatalf("backend = %q, expected %q", backend, ReflectionBackendGollem)
	}
	if out.Reply != "ok" {
		t.Fatalf("Reply = %q, expected %q", out.Reply, "ok")
	}
}

// TestReflectAutoFallsBackToOwnAfterGollemGuardrailFailure exercises the
// branch reflect_auto.go's ReflectAuto documents but no test previously
// drove: Gollem is used (the default backend), GollemReflect fails for a
// reason UNRELATED to ctx cancellation (here: the ADR-0010 max-prompt-length
// input guardrail rejecting an oversized prompt before any model call), and
// ReflectAuto falls back to the homegrown Reflect[T] loop — which has no such
// guardrail — and succeeds. This proves the fallback is real: gerr != nil AND
// ctx.Err() == nil.
func TestReflectAutoFallsBackToOwnAfterGollemGuardrailFailure(t *testing.T) {
	// Longer than gollemMaxPromptChars (100_000): trips Gollem's input
	// guardrail deterministically, with zero model calls spent on the Gollem
	// attempt (the guardrail runs before the agent loop).
	prompt := strings.Repeat("a", 100_001)

	client := &fakeClient{responses: []string{`{"reply":"fallback-ok"}`}}
	decode := func(raw string) (gollemTestOutput, error) {
		var out gollemTestOutput
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return gollemTestOutput{}, err
		}
		return out, nil
	}

	out, attempts, backend, _, err := ReflectAuto[gollemTestOutput](context.Background(), client, "system", prompt, decode, nil, 3)
	if err != nil {
		t.Fatalf("ReflectAuto returned unexpected error: %v", err)
	}
	if backend != ReflectionBackendOwn {
		t.Fatalf("backend = %q, want %q (fallback must have engaged after the Gollem guardrail rejection)", backend, ReflectionBackendOwn)
	}
	if out.Reply != "fallback-ok" {
		t.Fatalf("Reply = %q, want %q", out.Reply, "fallback-ok")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (the homegrown loop succeeded on its first try)", attempts)
	}
	// The guardrail rejection spent zero Gollem model calls; the single
	// StreamChat call recorded is the homegrown fallback's own attempt.
	if client.calls != 1 {
		t.Fatalf("client.calls = %d, want 1 (guardrail must reject before any model call, and the fallback makes exactly one)", client.calls)
	}
}

// TestReflectAutoDoesNotFallBackOnCtxCancellation proves the OTHER half of
// ReflectAuto's fallback branch: when GollemReflect fails because ctx was
// already cancelled, ReflectAuto returns that error immediately WITHOUT
// retrying via the homegrown backend (retrying would just cut at the same
// point, per the function's own doc) — the backend reported is still Gollem,
// and the client is never invoked via the fallback path.
func TestReflectAutoDoesNotFallBackOnCtxCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before ReflectAuto ever runs

	client := &fakeClient{responses: []string{`{"reply":"should not be reached"}`}}
	decode := func(raw string) (gollemTestOutput, error) {
		var out gollemTestOutput
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return gollemTestOutput{}, err
		}
		return out, nil
	}

	_, _, backend, _, err := ReflectAuto[gollemTestOutput](ctx, client, "system", "hello", decode, nil, 3)
	if err == nil {
		t.Fatal("expected ReflectAuto to return an error for an already-cancelled ctx")
	}
	if backend != ReflectionBackendGollem {
		t.Fatalf("backend = %q, want %q (no fallback attempt on ctx cancellation)", backend, ReflectionBackendGollem)
	}
}

// usageFakeClient is fakeClient (above) extended to also carry a distinct
// llm.Usage per canned response, so a test can force >1 StreamChat call and
// assert that the per-call Usage values are actually SUMMED, not just the
// last call's value forwarded. fakeClient itself never sets Token.Usage, so
// it cannot exercise aggregation — that gap is what this type closes.
type usageFakeClient struct {
	responses []string
	usages    []Usage
	calls     int
}

func (f *usageFakeClient) Name() string { return "usage-fake" }

func (f *usageFakeClient) StreamChat(_ context.Context, _, _ string) (<-chan Token, error) {
	idx := f.calls
	f.calls++

	text := f.responses[len(f.responses)-1]
	if idx < len(f.responses) {
		text = f.responses[idx]
	}
	usage := f.usages[len(f.usages)-1]
	if idx < len(f.usages) {
		usage = f.usages[idx]
	}

	out := make(chan Token, 2)
	out <- Token{Text: text}
	// Forward the scripted Usage WHOLE (a per-call copy), not rebuilt
	// field-by-field — the field-list rebuild silently dropped
	// ReasoningTokens, the very drop class this fake exists to catch.
	u := usage
	out <- Token{Done: true, Usage: &u}
	close(out)
	return out, nil
}

// TestReflectAuto_AggregatesUsageAcrossRetries proves ReflectAuto's usage
// return value is the SUM of per-iteration token counts across a
// multi-request Gollem run, not just the last request's — i.e. that
// core.ReflectResult[T].Usage ("the combined usage across all iterations",
// core/reflect.go) really does propagate through GollemReflect/ReflectAuto
// end to end.
//
// The multiple requests are driven the same way
// TestGollemReflectRepairsSchemaViaAgentInternalRetry (above) proves Agent[T]
// retries internally: the first response is not valid JSON, so Agent[T]
// re-issues a second Model.Request (and therefore a second
// usageFakeClient.StreamChat call) with the exact schema error attached —
// this is a real 2-request run through our own GollemModel adapter, not a
// hand-constructed core.ReflectResult. Verified against the vendored
// dependency source (core/run_engine.go:397 `e.state.usage.IncrRequest(resp.Usage)`,
// called on every Model.Request — including this internal retry — and
// core/reflect.go:83 `totalUsage.IncrRun(result.Usage)`), and independently
// confirmed by the upstream test core/reflect_test.go
// TestRunWithReflection_UsageAggregation.
func TestReflectAuto_AggregatesUsageAcrossRetries(t *testing.T) {
	client := &usageFakeClient{
		responses: []string{
			"this is not JSON at all", // triggers Agent[T]'s internal schema retry
			`{"reply":"ok"}`,
		},
		usages: []Usage{
			{InputTokens: 100, OutputTokens: 20, ReasoningTokens: 7},
			{InputTokens: 50, OutputTokens: 10, ReasoningTokens: 3},
		},
	}
	decode := func(raw string) (gollemTestOutput, error) {
		var out gollemTestOutput
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return gollemTestOutput{}, err
		}
		return out, nil
	}

	out, attempts, backend, usage, err := ReflectAuto[gollemTestOutput](
		context.Background(), client, "system", "hello", decode, nil, 3,
	)
	if err != nil {
		t.Fatalf("ReflectAuto returned unexpected error: %v", err)
	}
	if backend != ReflectionBackendGollem {
		t.Fatalf("backend = %q, expected %q", backend, ReflectionBackendGollem)
	}
	if out.Reply != "ok" {
		t.Fatalf("Reply = %q, expected %q", out.Reply, "ok")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, expected 1 (schema repair is internal to Agent.Run, "+
			"not a RunWithReflection cycle — mirrors TestGollemReflectRepairsSchemaViaAgentInternalRetry)", attempts)
	}
	if client.calls != 2 {
		t.Fatalf("client.calls = %d, expected 2 (1 invalid attempt + 1 repaired)", client.calls)
	}

	const wantInput, wantOutput = 100 + 50, 20 + 10
	if usage.InputTokens != wantInput {
		t.Fatalf("usage.InputTokens = %d, expected %d (sum of both iterations' usage, not just the last)",
			usage.InputTokens, wantInput)
	}
	if usage.OutputTokens != wantOutput {
		t.Fatalf("usage.OutputTokens = %d, expected %d (sum of both iterations' usage, not just the last)",
			usage.OutputTokens, wantOutput)
	}
	// Reasoning tokens round-trip through gollem's Details["reasoning_tokens"]
	// convention (toModelResponse writes it, core's Usage.Incr sums it,
	// ReflectAuto reads it back) — the regression guard for the be945de-flagged
	// drop at reflect_auto.go's Usage mapping.
	const wantReasoning = 7 + 3
	if usage.ReasoningTokens != wantReasoning {
		t.Fatalf("usage.ReasoningTokens = %d, expected %d (sum of both iterations, carried via core.Usage.Details)",
			usage.ReasoningTokens, wantReasoning)
	}
}

// TestGollemModelRequest_NoTools_GenerateFullErrorPropagates proves Request's
// plain (non-tool) path wraps a failing generateFull call with the model
// name, rather than swallowing the error or panicking on a nil response.
func TestGollemModelRequest_NoTools_GenerateFullErrorPropagates(t *testing.T) {
	m := NewGollemModel(&flakyClient{fail: true})
	_, err := m.Request(context.Background(), nil, nil, nil)
	if err == nil {
		t.Fatal("expected Request to propagate the generateFull error")
	}
	if !strings.Contains(err.Error(), "gollem model") {
		t.Fatalf("err = %v, want it to identify the gollem model", err)
	}
}

// TestGollemModelRequestWithTools_CallToolsErrorPropagates proves
// requestWithTools wraps a failing ToolCaller.CallTools call with the model
// name and "(tool calling)" marker, distinct from the plain-path wrapping
// above.
func TestGollemModelRequestWithTools_CallToolsErrorPropagates(t *testing.T) {
	tc := &fakeToolCaller{Client: NewStubClient(), err: errors.New("provider tool-call failure")}
	m := NewGollemModel(tc)
	params := &core.ModelRequestParameters{
		FunctionTools: []core.ToolDefinition{{Name: "greet", Description: "greet someone"}},
	}
	_, err := m.Request(context.Background(), nil, nil, params)
	if err == nil {
		t.Fatal("expected Request to propagate the CallTools error")
	}
	if !strings.Contains(err.Error(), "tool calling") {
		t.Fatalf("err = %v, want it to identify the tool-calling path", err)
	}
}

// TestGollemModelRequestWithTools_PrefersRawArgsOverFlattenedArgs proves a
// ToolCall carrying RawArgs (the model's exact JSON, ADR-0016 lever #1) wins
// over the flattened string-map Args — the branch that preserves nested/typed
// argument structure a flattened map would drop.
func TestGollemModelRequestWithTools_PrefersRawArgsOverFlattenedArgs(t *testing.T) {
	tc := &fakeToolCaller{
		Client: NewStubClient(),
		script: []ToolCallResult{{Calls: []ToolCall{{
			Name:    "greet",
			Args:    map[string]string{"who": "flattened"}, // must be ignored
			RawArgs: json.RawMessage(`{"who":{"nested":"value"}}`),
		}}}},
	}
	m := NewGollemModel(tc)
	params := &core.ModelRequestParameters{
		FunctionTools: []core.ToolDefinition{{Name: "greet"}},
	}
	resp, err := m.Request(context.Background(), nil, nil, params)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if calls[0].ArgsJSON != `{"who":{"nested":"value"}}` {
		t.Fatalf("ArgsJSON = %q, want the RawArgs JSON preferred over the flattened Args map", calls[0].ArgsJSON)
	}
}

// errReasoningToolCaller is a ReasoningToolCaller whose CallToolsReasoning
// always fails, so callToolsMaybeReasoning's error-propagation branch (the
// reasoning-carry path, distinct from plain CallTools' error path already
// covered by TestGollemModelRequestWithTools_CallToolsErrorPropagates) can be
// driven directly.
type errReasoningToolCaller struct {
	Client
}

func (e *errReasoningToolCaller) CallTools(ctx context.Context, system, prompt string, tools []ToolSpec) (ToolCallResult, error) {
	res, _, err := e.CallToolsReasoning(ctx, system, prompt, tools, ReasoningState{})
	return res, err
}

func (e *errReasoningToolCaller) CallToolsReasoning(context.Context, string, string, []ToolSpec, ReasoningState) (ToolCallResult, ReasoningState, error) {
	return ToolCallResult{}, ReasoningState{}, errors.New("reasoning tool-call failure")
}

var _ ReasoningToolCaller = (*errReasoningToolCaller)(nil)

// TestCallToolsMaybeReasoning_PropagatesReasoningError proves that when a
// request-local reasoning accumulator is present AND the client implements
// ReasoningToolCaller, an error from CallToolsReasoning propagates (and the
// accumulator is left untouched) rather than being masked or causing a panic
// on the *m.reasoningAcc = next assignment.
func TestCallToolsMaybeReasoning_PropagatesReasoningError(t *testing.T) {
	var acc ReasoningState
	m := NewGollemModel(&errReasoningToolCaller{Client: NewStubClient()}).withReasoningAccumulator(&acc)

	_, err := m.callToolsMaybeReasoning(context.Background(), &errReasoningToolCaller{Client: NewStubClient()}, "sys", "prompt", nil)
	if err == nil {
		t.Fatal("expected callToolsMaybeReasoning to propagate the CallToolsReasoning error")
	}
	if !acc.IsEmpty() {
		t.Fatalf("accumulator = %+v, want untouched on error", acc)
	}
}

// --- generateFull direct unit tests -----------------------------------------

// TestGenerateFull_StreamChatError proves generateFull propagates an error
// from the initial Client.StreamChat call unchanged (the earliest possible
// failure point — before any token is ever read).
func TestGenerateFull_StreamChatError(t *testing.T) {
	fc := &flakyClient{fail: true}
	_, usage, err := generateFull(context.Background(), fc, "sys", "prompt")
	if err == nil {
		t.Fatal("expected an error from a failing StreamChat")
	}
	if usage != nil {
		t.Fatalf("usage = %+v, want nil on a StreamChat error", usage)
	}
}

// TestGenerateFull_TokenErrorMidStream proves a mid-stream Token.Err aborts
// generateFull immediately with that error, discarding any partial text
// already accumulated (the caller cannot trust a partial structured-output
// response).
func TestGenerateFull_TokenErrorMidStream(t *testing.T) {
	client := &erroringClient{}
	_, _, err := generateFull(context.Background(), client, "sys", "prompt")
	if !errors.Is(err, errStreamFailed) {
		t.Fatalf("err = %v, want %v", err, errStreamFailed)
	}
}

// blockingNoTokenClient's StreamChat returns an open channel that never sends
// anything and is never closed within the test's lifetime — used to force
// generateFull's ctx.Done() select branch to be the ONLY ready case,
// deterministically (no race against a competing token arriving at the same
// time).
type blockingNoTokenClient struct{}

func (blockingNoTokenClient) Name() string { return "blocking-no-token" }

func (blockingNoTokenClient) StreamChat(context.Context, string, string) (<-chan Token, error) {
	return make(chan Token), nil // never sent to, never closed
}

// TestGenerateFull_CtxCancelledMidStream proves generateFull returns
// ctx.Err() (not a hang, not a Token-shaped error) when ctx is cancelled
// while waiting on the token channel with nothing yet received.
func TestGenerateFull_CtxCancelledMidStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _, err := generateFull(ctx, blockingNoTokenClient{}, "sys", "prompt")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

// --- buildGollemPrompt / writeLine / outputInstruction direct unit tests ---

// TestBuildGollemPrompt_IncludesPreviousModelResponseText proves a prior
// core.ModelResponse's text content (a previous turn's answer) is folded into
// the flattened prompt body — the "[previous model response]" branch
// buildGollemPrompt's ModelResponse case handles, distinct from the
// ModelRequest parts branches every other buildGollemPrompt test exercises.
func TestBuildGollemPrompt_IncludesPreviousModelResponseText(t *testing.T) {
	messages := []core.ModelMessage{
		core.ModelRequest{Parts: []core.ModelRequestPart{
			core.UserPromptPart{Content: "what's the weather"},
		}},
		*core.TextResponse("it is sunny"),
	}
	_, prompt := buildGollemPrompt(messages, nil)
	if !strings.Contains(prompt, "[previous model response] it is sunny") {
		t.Fatalf("prompt = %q, want it to include the previous model response text", prompt)
	}
}

// TestWriteLine_EmptyStringIsNoOp proves writeLine leaves the builder
// untouched (no stray leading newline) when handed an empty string — the
// short-circuit every call site relies on to skip absent message parts.
func TestWriteLine_EmptyStringIsNoOp(t *testing.T) {
	var b strings.Builder
	writeLine(&b, "first")
	writeLine(&b, "") // must be a no-op, not an empty extra line
	writeLine(&b, "second")
	if got, want := b.String(), "first\nsecond"; got != want {
		t.Fatalf("writeLine sequence = %q, want %q", got, want)
	}
}

// TestOutputInstruction_NilParamsAndOutputObject covers outputInstruction's
// two branches no other test drives directly: nil params (no instruction at
// all — the plain-text/no-structured-output case) and the OutputObject path
// (native/prompted structured output with no synthetic output tool).
func TestOutputInstruction_NilParamsAndOutputObject(t *testing.T) {
	if got := outputInstruction(nil); got != "" {
		t.Fatalf("outputInstruction(nil) = %q, want \"\"", got)
	}

	params := &core.ModelRequestParameters{
		OutputObject: &core.OutputObjectDefinition{
			Name:       "answer",
			JSONSchema: core.Schema{"type": "object"},
		},
	}
	got := outputInstruction(params)
	if !strings.Contains(got, `"type": "object"`) {
		t.Fatalf("outputInstruction(OutputObject) = %q, want it to embed the OutputObject's JSONSchema", got)
	}
}

// --- toModelResponse direct unit test ---------------------------------------

// TestToModelResponse_CarriesReasoningTokens proves a non-zero
// Usage.ReasoningTokens (ADR-0061) is folded into
// core.ModelResponse.Usage.Details["reasoning_tokens"], so a CostTracker
// reading ModelResponse.Usage does not silently drop reasoning-token spend.
func TestToModelResponse_CarriesReasoningTokens(t *testing.T) {
	usage := &Usage{InputTokens: 10, OutputTokens: 5, ReasoningTokens: 7}
	resp := toModelResponse(`{"reply":"ok"}`, usage, nil)
	if resp.Usage.Details == nil || resp.Usage.Details["reasoning_tokens"] != 7 {
		t.Fatalf("Usage.Details = %+v, want reasoning_tokens=7", resp.Usage.Details)
	}
}
