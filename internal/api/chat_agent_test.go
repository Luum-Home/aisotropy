package api

import (
	"context"
	"errors"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/primitives"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// fakeToolCaller is a scripted llm.ToolCaller for exercising the native
// tool-calling path (ADR-0012) without a real provider: it returns the next
// scripted ToolCallResult per CallTools turn, then empty (answer directly). The
// embedded llm.Client (a stub) satisfies the Client half of ToolCaller; the
// native loop never calls StreamChat/Name, only CallTools.
type fakeToolCaller struct {
	llm.Client
	script []llm.ToolCallResult
	turn   int
}

func (f *fakeToolCaller) CallTools(_ context.Context, _, _ string, _ []llm.ToolSpec) (llm.ToolCallResult, error) {
	if f.turn < len(f.script) {
		r := f.script[f.turn]
		f.turn++
		return r, nil
	}
	return llm.ToolCallResult{}, nil
}

// compile-time contract checks for ADR-0012's optional capability seam.
var (
	_ llm.ToolCaller = (*fakeToolCaller)(nil)
	_ llm.Client     = (*llm.StubClient)(nil)
)

// mustNotRunExecutor fails the test if Execute is ever called — used to prove a
// non-permitted tool is rejected BEFORE execution.
type mustNotRunExecutor struct{ t *testing.T }

func (e mustNotRunExecutor) Execute(context.Context, string, map[string]string) (string, error) {
	e.t.Fatal("executor ran for a tool the caller's scopes did not permit")
	return "", nil
}

type failingExecutor struct{}

func (failingExecutor) Execute(context.Context, string, map[string]string) (string, error) {
	return "", errors.New("tool boom")
}

// TestFrictionExecutorRecordsOnFailure: a failed tool invocation must accrue
// friction for that user's primitive so the sleep cycle can rewrite it.
func TestFrictionExecutorRecordsOnFailure(t *testing.T) {
	store := primitives.NewStore()
	fe := frictionExecutor{
		inner:    failingExecutor{},
		detector: primitives.NewFrictionDetector(store),
		tenant:   "acme",
		user:     "alice",
	}
	if _, err := fe.Execute(context.Background(), "write_tasks", nil); err == nil {
		t.Fatal("expected the inner tool error to propagate")
	}
	p, ok := store.Get("acme", "alice", "write_tasks")
	if !ok || p.FrictionScore <= 0 {
		t.Fatalf("friction not recorded on failure: primitive=%+v ok=%v", p, ok)
	}
}

// TestFrictionExecutorNoFrictionOnSuccess: a successful invocation records no
// friction (the common case).
func TestFrictionExecutorNoFrictionOnSuccess(t *testing.T) {
	store := primitives.NewStore()
	fe := frictionExecutor{
		inner:    DemoExecutor{},
		detector: primitives.NewFrictionDetector(store),
		tenant:   "acme",
		user:     "alice",
	}
	res, err := fe.Execute(context.Background(), "search_docs", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res == "" {
		t.Fatal("expected an echo result")
	}
	if _, ok := store.Get("acme", "alice", "search_docs"); ok {
		t.Fatal("friction should not be recorded on a successful invocation")
	}
}

// TestNativeToolLoop_ExecutesPermittedTool: when a ToolCaller selects a
// scope-permitted tool, the native loop (ADR-0012) executes it and records the
// result, then terminates when the model stops calling tools.
func TestNativeToolLoop_ExecutesPermittedTool(t *testing.T) {
	reg := tools.NewRegistry(tools.Tool{Name: "search_docs", Description: "search the kb"})
	tc := &fakeToolCaller{
		Client: llm.NewStubClient(),
		script: []llm.ToolCallResult{
			{Calls: []llm.ToolCall{{Name: "search_docs", Args: map[string]string{"q": "billing"}}}},
		},
	}

	steps, err := runNativeToolLoop(context.Background(), tc, reg, DemoExecutor{}, nil, "find billing docs", 3)
	if err != nil {
		t.Fatalf("runNativeToolLoop: %v", err)
	}
	var ran bool
	for _, s := range steps {
		if s.Decision.Tool == "search_docs" && s.Result != "" {
			ran = true
		}
	}
	if !ran {
		t.Fatalf("expected search_docs to execute with a result, got steps=%+v", steps)
	}
}

// TestNativeToolLoop_RejectsNonPermittedTool: the double scope-gate — if the
// ToolCaller returns a tool the caller's scopes did NOT grant, the native loop
// must treat it as "no tool" and NEVER execute it (mustNotRunExecutor asserts
// Execute is never reached).
func TestNativeToolLoop_RejectsNonPermittedTool(t *testing.T) {
	// read_financials requires a scope the caller does not hold, so it is not in
	// Permitted(nil) and must never be offered nor executed.
	reg := tools.NewRegistry(tools.Tool{Name: "read_financials", RequiredScope: "read:financial-data"})
	tc := &fakeToolCaller{
		Client: llm.NewStubClient(),
		script: []llm.ToolCallResult{
			{Calls: []llm.ToolCall{{Name: "read_financials"}}}, // model tries anyway
		},
	}

	steps, err := runNativeToolLoop(context.Background(), tc, reg, mustNotRunExecutor{t: t}, nil, "read the financials", 3)
	if err != nil {
		t.Fatalf("runNativeToolLoop: %v", err)
	}
	for _, s := range steps {
		if s.Decision.Tool != "" {
			t.Fatalf("a non-permitted tool leaked into the steps: %+v", s.Decision)
		}
	}
}

// TestStubClientStaysPlainClient: per ADR-0012, StubClient must NOT implement
// ToolCaller, so the default runtime keeps using the agentloop fallback path.
func TestStubClientStaysPlainClient(t *testing.T) {
	var c llm.Client = llm.NewStubClient()
	if _, ok := c.(llm.ToolCaller); ok {
		t.Fatal("StubClient must not implement ToolCaller (stays a plain Client)")
	}
}
