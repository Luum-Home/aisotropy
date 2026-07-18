package graph

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// textReply gives a no-tool node a single-word deterministic answer derived from
// its system prompt, so an edge downstream sees a non-empty Output.
func textReply(system, _ string) string {
	return "out-" + strings.TrimPrefix(system, "sys-")
}

func noToolDeps(fc *fakeClient) Deps {
	return Deps{
		Client:      fc,
		Registry:    tools.NewRegistry(),
		Exec:        func(context.Context, string, string) (string, error) { return "", nil },
		Concurrency: 4,
	}
}

// TestExecute_LinearPipesOutputToInput: a→b, b's PromptFunc reads a's mapped
// payload; the graph's Output is b's Output.
func TestExecute_LinearPipesOutputToInput(t *testing.T) {
	fc := newFakeClient()
	fc.text = textReply

	var writerSaw string
	a := Node{ID: "a", System: "sys-a"}
	b := Node{ID: "b", System: "sys-b", Prompt: func(_ GraphInput, from map[string]EdgePayload) (string, error) {
		writerSaw = from["a"].Text
		return "write from: " + from["a"].Text, nil
	}}
	g, err := Build(NewSpec().AddNode(a).AddNode(b).AddEdge(Edge{From: "a", To: "b"}).SetEntry("a"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	res, err := Execute(context.Background(), noToolDeps(fc), g, GraphInput{Prompt: "seed"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.TrimSpace(writerSaw) != "out-a" {
		t.Fatalf("writer saw %q, want the researcher's output out-a", writerSaw)
	}
	if strings.TrimSpace(res.Output) != "out-b" {
		t.Fatalf("graph Output = %q, want out-b (terminal node's output)", res.Output)
	}
}

// TestExecute_DiamondWaitsForAllParents: a→b, a→c, b→c. c must see BOTH a and b.
func TestExecute_DiamondWaitsForAllParents(t *testing.T) {
	fc := newFakeClient()
	fc.text = textReply

	var got map[string]EdgePayload
	c := Node{ID: "c", System: "sys-c", Prompt: func(_ GraphInput, from map[string]EdgePayload) (string, error) {
		got = from
		if _, ok := from["a"]; !ok {
			return "", errors.New("missing a")
		}
		if _, ok := from["b"]; !ok {
			return "", errors.New("missing b")
		}
		return "reviewed", nil
	}}
	g, err := Build(NewSpec().
		AddNode(Node{ID: "a", System: "sys-a"}).
		AddNode(Node{ID: "b", System: "sys-b", Prompt: func(GraphInput, map[string]EdgePayload) (string, error) { return "b", nil }}).
		AddNode(c).
		AddEdge(Edge{From: "a", To: "b"}).
		AddEdge(Edge{From: "a", To: "c"}).
		AddEdge(Edge{From: "b", To: "c"}).
		SetEntry("a"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, err := Execute(context.Background(), noToolDeps(fc), g, GraphInput{Prompt: "x"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("reviewer saw %d in-edge payloads, want 2 (diamond join): %+v", len(got), got)
	}
}

// TestExecute_EdgeMapTransforms: a custom Edge.Map rewrites the payload the
// destination receives.
func TestExecute_EdgeMapTransforms(t *testing.T) {
	fc := newFakeClient()
	fc.text = textReply

	var saw string
	b := Node{ID: "b", System: "sys-b", Prompt: func(_ GraphInput, from map[string]EdgePayload) (string, error) {
		saw = from["a"].Text
		return "b", nil
	}}
	g, _ := Build(NewSpec().
		AddNode(Node{ID: "a", System: "sys-a"}).AddNode(b).
		AddEdge(Edge{From: "a", To: "b", Map: func(nr NodeResult) (EdgePayload, error) {
			return EdgePayload{Text: "MAPPED(" + strings.TrimSpace(nr.Output) + ")"}, nil
		}}).
		SetEntry("a"))
	if _, err := Execute(context.Background(), noToolDeps(fc), g, GraphInput{Prompt: "x"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if saw != "MAPPED(out-a)" {
		t.Fatalf("edge Map not applied: destination saw %q", saw)
	}
}

// TestExecute_IndependentBranchesConcurrent: two independent source nodes must
// run at the same time. Each blocks on entry until BOTH have entered; a sequential
// executor would deadlock (and the test times out), proving concurrency.
func TestExecute_IndependentBranchesConcurrent(t *testing.T) {
	entered := make(chan string, 2)
	proceed := make(chan struct{})
	fc := newFakeClient()
	fc.text = func(system, _ string) string {
		entered <- system
		<-proceed
		return "ok"
	}

	g, _ := Build(NewSpec().
		AddNode(Node{ID: "x", System: "sys-x"}).
		AddNode(Node{ID: "y", System: "sys-y"}))

	deps := noToolDeps(fc)
	deps.Concurrency = 2
	done := make(chan error, 1)
	go func() {
		_, err := Execute(context.Background(), deps, g, GraphInput{Prompt: "x"})
		done <- err
	}()

	// Both nodes must ENTER before either is allowed to proceed.
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("only one node entered within the deadline — nodes did not run concurrently")
		}
	}
	close(proceed)
	if err := <-done; err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

// TestExecute_CancellationStopsGraph: cancelling the ctx stops in-flight nodes and
// Execute returns ctx.Err().
func TestExecute_CancellationStopsGraph(t *testing.T) {
	bc := &blockUntilCancelClient{started: make(chan struct{}, 1)}
	g, _ := Build(NewSpec().AddNode(Node{ID: "a", System: "sys-a"}))
	deps := Deps{Client: bc, Registry: tools.NewRegistry(), Exec: func(context.Context, string, string) (string, error) { return "", nil }}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Execute(ctx, deps, g, GraphInput{Prompt: "x"})
		done <- err
	}()
	<-bc.started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Execute error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Execute did not return after cancellation")
	}
}

// TestExecute_ScopeGateRejectsUnpermittedTool: a node listing a tool its scopes do
// not grant fails the graph BEFORE any node runs (0 LLM calls).
func TestExecute_ScopeGateRejectsUnpermittedTool(t *testing.T) {
	fc := newFakeClient()
	reg := tools.NewRegistry(tools.Tool{Name: "web_search", RequiredScope: "tools:search"})
	// Node declares the tool but NOT the scope that grants it.
	g, _ := Build(NewSpec().AddNode(Node{ID: "a", System: "sys-a", Tools: []string{"web_search"}, Scopes: []string{"tools:other"}}))
	deps := Deps{Client: fc, Registry: reg, Exec: func(context.Context, string, string) (string, error) { return "", nil }}

	_, err := Execute(context.Background(), deps, g, GraphInput{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("expected a scope-gate rejection, got %v", err)
	}
	if fc.countSystem("sys-a") != 0 {
		t.Fatal("node ran despite the scope-gate rejection (should be 0 LLM calls)")
	}
}

// TestExecute_ToolNodeRunsScopedTool: a node whose scopes grant its tool executes
// it and records the step; the tool executor sees the offered tool.
func TestExecute_ToolNodeRunsScopedTool(t *testing.T) {
	fc := newFakeClient()
	fc.tools["sys-a"] = []llm.ToolCallResult{
		{Calls: []llm.ToolCall{{Name: "web_search", Args: map[string]string{"q": "go"}}}},
		{Message: "findings ready"},
	}
	reg := tools.NewRegistry(tools.Tool{Name: "web_search", RequiredScope: "tools:search"})

	var execCalls int
	deps := Deps{
		Client:   fc,
		Registry: reg,
		Exec: func(_ context.Context, name, _ string) (string, error) {
			execCalls++
			if name != "web_search" {
				t.Fatalf("exec saw unexpected tool %q", name)
			}
			return "result", nil
		},
	}
	g, _ := Build(NewSpec().AddNode(Node{ID: "a", System: "sys-a", Scopes: []string{"tools:search"}, Tools: []string{"web_search"}, MaxSteps: 4}))

	res, err := Execute(context.Background(), deps, g, GraphInput{Prompt: "research go"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if execCalls != 1 {
		t.Fatalf("tool executor called %d times, want 1", execCalls)
	}
	nr := res.Nodes["a"]
	if len(nr.Steps) != 1 || nr.Steps[0].Tool != "web_search" {
		t.Fatalf("expected one recorded web_search step, got %+v", nr.Steps)
	}
	if !strings.Contains(nr.Output, "findings ready") {
		t.Fatalf("node Output = %q, want the final message", nr.Output)
	}
}

// blockUntilCancelClient's StreamChat blocks until ctx is cancelled, then closes
// the stream — used to prove cancellation stops an in-flight node.
type blockUntilCancelClient struct {
	started chan struct{}
	once    sync.Once
}

func (b *blockUntilCancelClient) Name() string { return "block" }

func (b *blockUntilCancelClient) StreamChat(ctx context.Context, _, _ string) (<-chan llm.Token, error) {
	b.once.Do(func() { close(b.started) })
	ch := make(chan llm.Token)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}
