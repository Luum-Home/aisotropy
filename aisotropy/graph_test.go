package aisotropy_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Luum-Home/aisotropy/aisotropy"
)

// execFunc adapts a plain func to the aisotropy.ToolExecutor interface for tests.
type execFunc func(ctx context.Context, name string, args map[string]string) (string, error)

func (f execFunc) Execute(ctx context.Context, name string, args map[string]string) (string, error) {
	return f(ctx, name, args)
}

// scriptedToolClient is a public-surface stand-in for a native tool-calling
// provider: the researcher node "calls" a tool once then answers; the writer and
// reviewer answer directly. It implements only the public surfaces a consumer
// sees (aisotropy.LLMClient + the optional tool-calling capability), proving a
// graph composes against the façade alone.
type scriptedToolClient struct {
	mu    sync.Mutex
	turns map[string]int
}

func (s *scriptedToolClient) Name() string { return "scripted" }

func (s *scriptedToolClient) StreamChat(ctx context.Context, system, _ string) (<-chan aisotropy.Token, error) {
	reply := "brief"
	if strings.Contains(system, "fact-check") {
		reply = "APPROVED"
	}
	ch := make(chan aisotropy.Token)
	go func() {
		defer close(ch)
		select {
		case <-ctx.Done():
			return
		case ch <- aisotropy.Token{Text: reply}:
		}
		select {
		case <-ctx.Done():
		case ch <- aisotropy.Token{Done: true, Usage: &aisotropy.Usage{}}:
		}
	}()
	return ch, nil
}

func (s *scriptedToolClient) CallTools(_ context.Context, system, _ string, _ []aisotropy.ToolSpec) (aisotropy.ToolCallResult, error) {
	s.mu.Lock()
	n := s.turns[system]
	if s.turns == nil {
		s.turns = map[string]int{}
	}
	s.turns[system] = n + 1
	s.mu.Unlock()
	if n == 0 {
		return aisotropy.ToolCallResult{Calls: []aisotropy.ToolCall{{Name: "web_search", Args: map[string]string{"q": "topic"}}}}, nil
	}
	return aisotropy.ToolCallResult{Message: "finding: the sky is blue [src]"}, nil
}

// TestGraph_ResearcherWriterReviewer composes the ADR's 3-node graph through the
// PUBLIC surface only (no internal import) and asserts the terminal output is the
// reviewer's and that output→input piping reached each node.
func TestGraph_ResearcherWriterReviewer(t *testing.T) {
	research := aisotropy.Node{
		ID:       "researcher",
		System:   "You research a topic and return terse findings.",
		Scopes:   []string{"tools:search"},
		Tools:    []string{"web_search"},
		MaxSteps: 4,
		Prompt: func(in aisotropy.GraphInput, _ map[string]aisotropy.EdgePayload) (string, error) {
			return "Research: " + in.Prompt, nil
		},
	}
	write := aisotropy.Node{
		ID:       "writer",
		System:   "You write a brief from findings.",
		MaxSteps: 2,
		Prompt: func(_ aisotropy.GraphInput, from map[string]aisotropy.EdgePayload) (string, error) {
			f, ok := from["researcher"]
			if !ok || strings.TrimSpace(f.Text) == "" {
				return "", fmt.Errorf("writer: missing researcher findings")
			}
			return "Write a brief from:\n" + f.Text, nil
		},
	}
	review := aisotropy.Node{
		ID:       "reviewer",
		System:   "You fact-check the brief and return APPROVED or a fix list.",
		MaxSteps: 2,
		Prompt: func(_ aisotropy.GraphInput, from map[string]aisotropy.EdgePayload) (string, error) {
			if _, ok := from["researcher"]; !ok {
				return "", fmt.Errorf("reviewer: missing researcher payload")
			}
			if _, ok := from["writer"]; !ok {
				return "", fmt.Errorf("reviewer: missing writer payload")
			}
			return "Findings:\n" + from["researcher"].Text + "\n\nBrief:\n" + from["writer"].Text, nil
		},
	}

	g, err := aisotropy.NewGraph().
		AddNode(research).AddNode(write).AddNode(review).
		AddEdge(aisotropy.Edge{From: "researcher", To: "writer"}).
		AddEdge(aisotropy.Edge{From: "researcher", To: "reviewer"}).
		AddEdge(aisotropy.Edge{From: "writer", To: "reviewer"}).
		SetEntry("researcher").
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	app := aisotropy.New(
		aisotropy.WithLLM(&scriptedToolClient{}),
		aisotropy.WithTool(aisotropy.Tool{Name: "web_search", Description: "search the web", RequiredScope: "tools:search"}),
		aisotropy.WithToolExecutor(execFunc(func(_ context.Context, name string, _ map[string]string) (string, error) {
			return "search results for " + name, nil
		})),
		aisotropy.WithGraph("brief", g),
	)
	t.Cleanup(func() { _ = app.Close() })

	res, err := app.RunGraph(context.Background(), "brief", aisotropy.GraphInput{Prompt: "impact of X on Y"})
	if err != nil {
		t.Fatalf("RunGraph: %v", err)
	}
	if strings.TrimSpace(res.Output) != "APPROVED" {
		t.Fatalf("graph Output = %q, want the reviewer's APPROVED verdict", res.Output)
	}
	if got := res.Nodes["researcher"]; len(got.Steps) != 1 || got.Steps[0].Tool != "web_search" {
		t.Fatalf("researcher steps = %+v, want one web_search step", got.Steps)
	}
	if !strings.Contains(res.Nodes["researcher"].Output, "finding:") {
		t.Fatalf("researcher output = %q, want the findings", res.Nodes["researcher"].Output)
	}
}

// TestGraph_UnknownName: RunGraph on an unmounted name errors.
func TestGraph_UnknownName(t *testing.T) {
	app := aisotropy.New(aisotropy.WithLLM(&scriptedToolClient{}))
	t.Cleanup(func() { _ = app.Close() })
	if _, err := app.RunGraph(context.Background(), "nope", aisotropy.GraphInput{Prompt: "x"}); err == nil {
		t.Fatal("expected an error for an unmounted graph name")
	}
}

// TestGraph_BuildRejectsCycle: the public builder rejects a cycle at Build.
func TestGraph_BuildRejectsCycle(t *testing.T) {
	_, err := aisotropy.NewGraph().
		AddNode(aisotropy.Node{ID: "a", System: "a"}).
		AddNode(aisotropy.Node{ID: "b", System: "b"}).
		AddEdge(aisotropy.Edge{From: "a", To: "b"}).
		AddEdge(aisotropy.Edge{From: "b", To: "a"}).
		Build()
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected a cycle rejection, got %v", err)
	}
}

// TestGraph_MultiTenantIdentity: two RunGraph calls under two different tenant
// ctxs (WithGraphIdentity) each observe ONLY their own tenant via the tool
// executor — the façade threads identity to every node.
func TestGraph_MultiTenantIdentity(t *testing.T) {
	var mu sync.Mutex
	observed := map[string]string{} // graph tenant -> tenant the tool saw

	g, err := aisotropy.NewGraph().
		AddNode(aisotropy.Node{
			ID: "n", System: "search", Scopes: []string{"tools:search"}, Tools: []string{"web_search"}, MaxSteps: 3,
			Prompt: func(in aisotropy.GraphInput, _ map[string]aisotropy.EdgePayload) (string, error) {
				return in.Prompt, nil
			},
		}).
		SetEntry("n").Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Each tenant gets its OWN app+client (a fresh scripted turn counter) so the
	// single tool-using node actually calls its tool once per run; the observed map
	// is shared so we can assert no cross-tenant leak.
	for _, tenant := range []string{"acme", "globex"} {
		app := aisotropy.New(
			aisotropy.WithLLM(&scriptedToolClient{}),
			aisotropy.WithTool(aisotropy.Tool{Name: "web_search", RequiredScope: "tools:search"}),
			aisotropy.WithToolExecutor(execFunc(func(ctx context.Context, _ string, _ map[string]string) (string, error) {
				ten, _ := aisotropy.TenantFromContext(ctx)
				mu.Lock()
				observed[ten] = ten
				mu.Unlock()
				return "ok", nil
			})),
			aisotropy.WithGraph("g", g),
		)
		ctx, err := aisotropy.WithGraphIdentity(context.Background(), tenant, "u1")
		if err != nil {
			_ = app.Close()
			t.Fatalf("WithGraphIdentity: %v", err)
		}
		if _, err := app.RunGraph(ctx, "g", aisotropy.GraphInput{Prompt: "hi"}); err != nil {
			_ = app.Close()
			t.Fatalf("RunGraph(%s): %v", tenant, err)
		}
		_ = app.Close()
	}
	if observed["acme"] != "acme" || observed["globex"] != "globex" {
		t.Fatalf("tenant identity not propagated to the tool executor: %+v", observed)
	}
}

// TestGraphSpec_SetMaxNodes covers the node-count ceiling override: a graph
// built with a lowered ceiling still builds and runs fine when its node count
// stays within it.
func TestGraphSpec_SetMaxNodes(t *testing.T) {
	g, err := aisotropy.NewGraph().
		AddNode(aisotropy.Node{
			ID: "solo", System: "s",
			Prompt: func(in aisotropy.GraphInput, _ map[string]aisotropy.EdgePayload) (string, error) {
				return in.Prompt, nil
			},
		}).
		SetEntry("solo").
		SetMaxNodes(5).
		Build()
	if err != nil {
		t.Fatalf("Build with SetMaxNodes(5): %v", err)
	}

	app := aisotropy.New(aisotropy.WithLLM(&scriptedToolClient{}), aisotropy.WithGraph("g", g))
	t.Cleanup(func() { _ = app.Close() })

	res, err := app.RunGraph(context.Background(), "g", aisotropy.GraphInput{Prompt: "hi"})
	if err != nil {
		t.Fatalf("RunGraph: %v", err)
	}
	if strings.TrimSpace(res.Output) != "brief" {
		t.Fatalf("graph Output = %q, want the stub's brief reply", res.Output)
	}
}

// TestWithGraph_EmptyNameOrNilGraphIgnored covers WithGraph's no-op guard: an
// empty name or a nil graph is silently ignored rather than mounted, so a later
// RunGraph against that name still reports "no graph named".
func TestWithGraph_EmptyNameOrNilGraphIgnored(t *testing.T) {
	g, err := aisotropy.NewGraph().
		AddNode(aisotropy.Node{ID: "n", System: "s"}).
		SetEntry("n").
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	app := aisotropy.New(
		aisotropy.WithLLM(&scriptedToolClient{}),
		aisotropy.WithGraph("", g),    // empty name: ignored
		aisotropy.WithGraph("x", nil), // nil graph: ignored
	)
	t.Cleanup(func() { _ = app.Close() })

	if _, err := app.RunGraph(context.Background(), "", aisotropy.GraphInput{Prompt: "hi"}); err == nil {
		t.Fatal("RunGraph(\"\") should not find a graph mounted under an empty name")
	}
	if _, err := app.RunGraph(context.Background(), "x", aisotropy.GraphInput{Prompt: "hi"}); err == nil {
		t.Fatal("RunGraph(\"x\") should not find a graph mounted via WithGraph(\"x\", nil)")
	}
}

// TestWithGraphIdentity_InvalidIdentityErrors covers WithGraphIdentity's error
// branch: an empty tenant fails the same identity validation the HTTP boundary
// applies, returning an error rather than a silently unscoped ctx.
func TestWithGraphIdentity_InvalidIdentityErrors(t *testing.T) {
	if _, err := aisotropy.WithGraphIdentity(context.Background(), "", "alice"); err == nil {
		t.Fatal("WithGraphIdentity with an empty tenant must return an error")
	}
	if _, err := aisotropy.WithGraphIdentity(context.Background(), "acme", "bad:user"); err == nil {
		t.Fatal("WithGraphIdentity with a reserved-character user must return an error")
	}
}

// TestRunGraph_RuntimeBuildFailureSurfaces covers RunGraph's ensureRuntime-error
// branch: a data dir that is actually a file makes runtime construction fail,
// and RunGraph surfaces that error rather than panicking or running anyway.
func TestRunGraph_RuntimeBuildFailureSurfaces(t *testing.T) {
	f := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	t.Setenv("AISOTROPY_DATA_DIR", f)

	g, err := aisotropy.NewGraph().
		AddNode(aisotropy.Node{ID: "n", System: "s"}).
		SetEntry("n").
		Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	app := aisotropy.New(aisotropy.WithLLM(&scriptedToolClient{}), aisotropy.WithGraph("g", g))
	t.Cleanup(func() { _ = app.Close() })

	if _, err := app.RunGraph(context.Background(), "g", aisotropy.GraphInput{Prompt: "hi"}); err == nil {
		t.Fatal("RunGraph must surface the runtime build failure from a bad data dir")
	}
}

// TestGraph_EdgeMapTransformsAndFailsClosed covers toInternalEdge's Map
// adaptation: a non-nil Edge.Map is invoked (its transformed payload reaches the
// destination node), and a Map that returns an error fails that edge closed
// (the destination never runs on a malformed input) rather than being ignored.
func TestGraph_EdgeMapTransformsAndFailsClosed(t *testing.T) {
	t.Run("Map transforms the payload", func(t *testing.T) {
		src := aisotropy.Node{
			ID: "src", System: "s",
			Prompt: func(in aisotropy.GraphInput, _ map[string]aisotropy.EdgePayload) (string, error) {
				return in.Prompt, nil
			},
		}
		dst := aisotropy.Node{
			ID: "dst", System: "fact-check", // scriptedToolClient replies APPROVED for "fact-check" systems
			Prompt: func(_ aisotropy.GraphInput, from map[string]aisotropy.EdgePayload) (string, error) {
				p, ok := from["src"]
				if !ok || p.Text != "MAPPED" {
					return "", fmt.Errorf("dst: expected mapped payload \"MAPPED\", got %+v (ok=%v)", p, ok)
				}
				return p.Text, nil
			},
		}
		g, err := aisotropy.NewGraph().
			AddNode(src).AddNode(dst).
			AddEdge(aisotropy.Edge{From: "src", To: "dst", Map: func(nr aisotropy.NodeResult) (aisotropy.EdgePayload, error) {
				return aisotropy.EdgePayload{Text: "MAPPED"}, nil
			}}).
			SetEntry("src").
			Build()
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		app := aisotropy.New(aisotropy.WithLLM(&scriptedToolClient{}), aisotropy.WithGraph("g", g))
		t.Cleanup(func() { _ = app.Close() })

		res, err := app.RunGraph(context.Background(), "g", aisotropy.GraphInput{Prompt: "hi"})
		if err != nil {
			t.Fatalf("RunGraph: %v", err)
		}
		if strings.TrimSpace(res.Output) != "APPROVED" {
			t.Fatalf("graph Output = %q, want APPROVED (dst only replies APPROVED when it received the mapped payload)", res.Output)
		}
	})

	t.Run("Map error fails the edge closed", func(t *testing.T) {
		src := aisotropy.Node{
			ID: "src", System: "s",
			Prompt: func(in aisotropy.GraphInput, _ map[string]aisotropy.EdgePayload) (string, error) {
				return in.Prompt, nil
			},
		}
		dst := aisotropy.Node{
			ID: "dst", System: "d",
			Prompt: func(_ aisotropy.GraphInput, from map[string]aisotropy.EdgePayload) (string, error) {
				return "should never run", nil
			},
		}
		g, err := aisotropy.NewGraph().
			AddNode(src).AddNode(dst).
			AddEdge(aisotropy.Edge{From: "src", To: "dst", Map: func(nr aisotropy.NodeResult) (aisotropy.EdgePayload, error) {
				return aisotropy.EdgePayload{}, fmt.Errorf("map: deliberately refuses to transform")
			}}).
			SetEntry("src").
			Build()
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		app := aisotropy.New(aisotropy.WithLLM(&scriptedToolClient{}), aisotropy.WithGraph("g", g))
		t.Cleanup(func() { _ = app.Close() })

		if _, err := app.RunGraph(context.Background(), "g", aisotropy.GraphInput{Prompt: "hi"}); err == nil {
			t.Fatal("RunGraph must fail closed when an Edge.Map returns an error")
		}
	})
}
