package api

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/trace/noop"

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

// TestNativeToolAgent_ExecutesPermittedTool: when a ToolCaller selects a
// scope-permitted tool, the native Gollem agent (ADR-0012 follow-up) executes
// it and records the result, then terminates when the model stops calling
// tools.
func TestNativeToolAgent_ExecutesPermittedTool(t *testing.T) {
	reg := tools.NewRegistry(tools.Tool{Name: "search_docs", Description: "search the kb"})
	tc := &fakeToolCaller{
		Client: llm.NewStubClient(),
		script: []llm.ToolCallResult{
			{Calls: []llm.ToolCall{{Name: "search_docs", Args: map[string]string{"q": "billing"}}}},
			{Message: "here are the billing docs"}, // terminal free-text answer
		},
	}

	steps, err := runNativeToolAgent(context.Background(), tc, noop.NewTracerProvider(), reg, DemoExecutor{}, nil, "find billing docs", 3)
	if err != nil {
		t.Fatalf("runNativeToolAgent: %v", err)
	}
	var ran bool
	for _, s := range steps {
		if s.Tool == "search_docs" && s.Result != "" {
			ran = true
		}
	}
	if !ran {
		t.Fatalf("expected search_docs to execute with a result, got steps=%+v", steps)
	}
}

// TestNativeToolAgent_RejectsNonPermittedTool: the double scope-gate — if the
// ToolCaller returns a tool the caller's scopes did NOT grant, that tool is
// never registered as a function tool on the agent, so it can NEVER execute
// (mustNotRunExecutor asserts Execute is never reached).
func TestNativeToolAgent_RejectsNonPermittedTool(t *testing.T) {
	// search_docs is granted (no required scope); read_financials requires a
	// scope the caller does not hold, so it is not in Permitted(nil) and is
	// never offered as a function tool. The model tries read_financials anyway.
	reg := tools.NewRegistry(
		tools.Tool{Name: "search_docs", Description: "search the kb"},
		tools.Tool{Name: "read_financials", RequiredScope: "read:financial-data"},
	)
	tc := &fakeToolCaller{
		Client: llm.NewStubClient(),
		script: []llm.ToolCallResult{
			{Calls: []llm.ToolCall{{Name: "read_financials"}}}, // model tries anyway -> unknown -> retry
			{Message: "I cannot access that"},                  // terminal answer on the retry turn
		},
	}

	steps, err := runNativeToolAgent(context.Background(), tc, noop.NewTracerProvider(), reg, mustNotRunExecutor{t: t}, nil, "read the financials", 3)
	if err != nil {
		t.Fatalf("runNativeToolAgent: %v", err)
	}
	for _, s := range steps {
		if s.Tool == "read_financials" {
			t.Fatalf("a non-permitted tool leaked into the steps: %+v", s)
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
