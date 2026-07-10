package llm

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/fugue-labs/gollem/core"
	"go.opentelemetry.io/otel/trace/noop"
)

// fakeToolCaller is a scripted llm.ToolCaller for exercising the native
// tool-calling bridge (GollemModel.requestWithTools) and RunToolAgent without a
// real provider: it returns the next scripted ToolCallResult per CallTools turn,
// then empty (answer directly). The embedded Client (a stub) satisfies the
// Client half of ToolCaller; the tool path never calls StreamChat.
type fakeToolCaller struct {
	Client
	script    []ToolCallResult
	turn      int
	lastSpecs []ToolSpec
}

func (f *fakeToolCaller) CallTools(_ context.Context, _, _ string, specs []ToolSpec) (ToolCallResult, error) {
	f.lastSpecs = specs
	if f.turn < len(f.script) {
		r := f.script[f.turn]
		f.turn++
		return r, nil
	}
	return ToolCallResult{}, nil
}

var _ ToolCaller = (*fakeToolCaller)(nil)

// TestGollemModelRequest_ToolCallBridge: when params offers function tools AND
// the wrapped client is a ToolCaller that returns a call, Request must emit a
// ModelResponse carrying one ToolCallPart per call (with the REAL provider name,
// not "test-model") and FinishReason=ToolCall.
func TestGollemModelRequest_ToolCallBridge(t *testing.T) {
	tc := &fakeToolCaller{
		Client: NewStubClient(),
		script: []ToolCallResult{
			{Calls: []ToolCall{{Name: "greet", Args: map[string]string{"who": "world"}}}},
		},
	}
	m := NewGollemModel(tc)
	params := &core.ModelRequestParameters{
		FunctionTools: []core.ToolDefinition{{Name: "greet", Description: "greet someone"}},
	}

	resp, err := m.Request(context.Background(), nil, nil, params)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if resp.FinishReason != core.FinishReasonToolCall {
		t.Fatalf("FinishReason = %q, want %q", resp.FinishReason, core.FinishReasonToolCall)
	}
	if resp.ModelName != "stub" {
		t.Fatalf("ModelName = %q, want the real provider name %q (not test-model)", resp.ModelName, "stub")
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1: %+v", len(calls), resp.Parts)
	}
	if calls[0].ToolName != "greet" {
		t.Fatalf("tool name = %q, want greet", calls[0].ToolName)
	}
	if calls[0].ArgsJSON != `{"who":"world"}` {
		t.Fatalf("args JSON = %q, want %q", calls[0].ArgsJSON, `{"who":"world"}`)
	}
	// The bridge must forward the offered specs (Name/Description only).
	if len(tc.lastSpecs) != 1 || tc.lastSpecs[0].Name != "greet" {
		t.Fatalf("CallTools received specs %+v, want a single greet spec", tc.lastSpecs)
	}
}

// TestGollemModelRequest_ToolCallBridge_NoCalls: when the ToolCaller returns no
// calls (answer directly), Request must return a plain TextResponse carrying the
// free-text message.
func TestGollemModelRequest_ToolCallBridge_NoCalls(t *testing.T) {
	tc := &fakeToolCaller{
		Client: NewStubClient(),
		script: []ToolCallResult{{Message: "no tool needed"}},
	}
	m := NewGollemModel(tc)
	params := &core.ModelRequestParameters{
		FunctionTools: []core.ToolDefinition{{Name: "greet", Description: "greet someone"}},
	}

	resp, err := m.Request(context.Background(), nil, nil, params)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if len(resp.ToolCalls()) != 0 {
		t.Fatalf("expected no tool calls, got %+v", resp.ToolCalls())
	}
	if got := resp.TextContent(); got != "no tool needed" {
		t.Fatalf("text content = %q, want %q", got, "no tool needed")
	}
}

// TestGollemModelRequest_NoFunctionTools_FallsThrough: with no function tools
// offered, Request must NOT take the tool branch even when the client is a
// ToolCaller — it falls through to the unchanged generateFull/text path, so
// CallTools is never invoked.
func TestGollemModelRequest_NoFunctionTools_FallsThrough(t *testing.T) {
	tc := &fakeToolCaller{Client: NewStubClient()}
	m := NewGollemModel(tc)

	if _, err := m.Request(context.Background(), nil, nil, &core.ModelRequestParameters{}); err != nil {
		t.Fatalf("Request: %v", err)
	}
	if tc.turn != 0 || tc.lastSpecs != nil {
		t.Fatalf("CallTools must not be invoked without function tools (turn=%d specs=%+v)", tc.turn, tc.lastSpecs)
	}
}

// TestRunToolAgent_RecordsToolStep: a single offered tool the model chooses is
// executed via exec and recorded as a ToolStep with the decoded args and result.
func TestRunToolAgent_RecordsToolStep(t *testing.T) {
	tc := &fakeToolCaller{
		Client: NewStubClient(),
		script: []ToolCallResult{
			{Calls: []ToolCall{{Name: "greet", Args: map[string]string{"who": "world"}}}},
			{Message: "greeted the world"}, // terminal free-text answer after the tool result
		},
	}
	var execCalls int
	exec := func(_ context.Context, name string, argsJSON string) (string, error) {
		execCalls++
		var a struct {
			Who string `json:"who"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &a)
		return "hello " + a.Who, nil
	}

	steps, err := RunToolAgent(context.Background(), tc, noop.NewTracerProvider(),
		"You may call tools.", "say hi to the world",
		[]ToolSpec{{Name: "greet", Description: "greet someone"}}, exec, 3)
	if err != nil {
		t.Fatalf("RunToolAgent: %v", err)
	}
	if execCalls != 1 {
		t.Fatalf("exec called %d times, want 1", execCalls)
	}
	if len(steps) != 1 {
		t.Fatalf("got %d steps, want 1: %+v", len(steps), steps)
	}
	if steps[0].Tool != "greet" {
		t.Fatalf("step tool = %q, want greet", steps[0].Tool)
	}
	if steps[0].Args["who"] != "world" {
		t.Fatalf("step args = %+v, want who=world", steps[0].Args)
	}
	if steps[0].Result != "hello world" {
		t.Fatalf("step result = %q, want %q", steps[0].Result, "hello world")
	}
}

// TestRunToolAgent_UnofferedToolNeverExecutes: a tool name the model returns
// that was NOT offered (not in the specs) is never registered on the agent, so
// it lands in unknownCalls (a retry) and can never reach exec. exec must not run
// and no ToolStep is recorded.
func TestRunToolAgent_UnofferedToolNeverExecutes(t *testing.T) {
	tc := &fakeToolCaller{
		Client: NewStubClient(),
		script: []ToolCallResult{
			{Calls: []ToolCall{{Name: "read_financials"}}}, // never offered -> unknown -> retry
			{Message: "I cannot do that"},                  // terminal answer on the retry turn
		},
	}
	exec := func(_ context.Context, name string, _ string) (string, error) {
		t.Fatalf("exec ran for a tool that was never offered: %q", name)
		return "", nil
	}

	steps, err := RunToolAgent(context.Background(), tc, nil,
		"You may call tools.", "read the financials",
		[]ToolSpec{{Name: "greet", Description: "greet someone"}}, exec, 3)
	if err != nil {
		t.Fatalf("RunToolAgent: %v", err)
	}
	if len(steps) != 0 {
		t.Fatalf("expected no recorded steps, got %+v", steps)
	}
}

// TestRunToolAgent_MaxStepsGraceful: a provider that always calls a tool never
// converges; the max-tool-steps guardrail must stop it GRACEFULLY — RunToolAgent
// returns the steps taken so far with a NIL error (so the endpoint yields partial
// results / HTTP 200), not a failure. Regression guard for the ADR-0012 follow-up
// fix where the guardrail abort was surfaced as an error and lost the steps.
func TestRunToolAgent_MaxStepsGraceful(t *testing.T) {
	loop := make([]ToolCallResult, 10)
	for i := range loop {
		loop[i] = ToolCallResult{Calls: []ToolCall{{Name: "loop", Args: map[string]string{"n": "1"}}}}
	}
	tc := &fakeToolCaller{Client: NewStubClient(), script: loop}
	specs := []ToolSpec{{Name: "loop", Description: "never converges"}}
	exec := func(context.Context, string, string) (string, error) { return "did", nil }

	steps, err := RunToolAgent(context.Background(), tc, nil, "sys", "go", specs, exec, 2)
	if err != nil {
		t.Fatalf("max-steps exhaustion must be graceful (nil error), got: %v", err)
	}
	if len(steps) == 0 {
		t.Fatal("expected partial steps recorded before the max-steps stop")
	}
}
