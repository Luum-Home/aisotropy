package graph

import (
	"strings"
	"testing"
)

func node(id string) Node { return Node{ID: id, System: "sys-" + id} }

// TestBuild_ValidDAG: a linear/diamond DAG builds and yields a topological order
// whose terminal is the sink.
func TestBuild_ValidDAG(t *testing.T) {
	g, err := Build(NewSpec().
		AddNode(node("a")).AddNode(node("b")).AddNode(node("c")).
		AddEdge(Edge{From: "a", To: "b"}).
		AddEdge(Edge{From: "a", To: "c"}).
		AddEdge(Edge{From: "b", To: "c"}).
		SetEntry("a"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := g.Terminal(); got != "c" {
		t.Fatalf("Terminal = %q, want c", got)
	}
	// a must precede b and c; b must precede c.
	pos := map[string]int{}
	for i, id := range g.order {
		pos[id] = i
	}
	if !(pos["a"] < pos["b"] && pos["b"] < pos["c"] && pos["a"] < pos["c"]) {
		t.Fatalf("topological order violated: %v", g.order)
	}
}

// TestBuild_CycleRejected: a cycle is rejected AT BUILD, naming the stuck nodes.
func TestBuild_CycleRejected(t *testing.T) {
	_, err := Build(NewSpec().
		AddNode(node("a")).AddNode(node("b")).AddNode(node("c")).
		AddEdge(Edge{From: "a", To: "b"}).
		AddEdge(Edge{From: "b", To: "c"}).
		AddEdge(Edge{From: "c", To: "a"}). // cycle
		SetEntry("a"))
	if err == nil {
		t.Fatal("expected a cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error %q should name the cycle", err)
	}
}

// TestBuild_SelfLoopRejected: a From==To edge is a cycle and rejected.
func TestBuild_SelfLoopRejected(t *testing.T) {
	_, err := Build(NewSpec().AddNode(node("a")).AddEdge(Edge{From: "a", To: "a"}))
	if err == nil || !strings.Contains(err.Error(), "self-loop") {
		t.Fatalf("expected self-loop error, got %v", err)
	}
}

// TestBuild_DanglingEdgeRejected: an edge to/from an unknown node is rejected.
func TestBuild_DanglingEdgeRejected(t *testing.T) {
	_, err := Build(NewSpec().AddNode(node("a")).AddEdge(Edge{From: "a", To: "ghost"}))
	if err == nil || !strings.Contains(err.Error(), "unknown node") {
		t.Fatalf("expected unknown-node error, got %v", err)
	}
}

// TestBuild_DuplicateNodeRejected: two nodes with the same ID are rejected.
func TestBuild_DuplicateNodeRejected(t *testing.T) {
	_, err := Build(NewSpec().AddNode(node("a")).AddNode(node("a")))
	if err == nil || !strings.Contains(err.Error(), "duplicate node") {
		t.Fatalf("expected duplicate-node error, got %v", err)
	}
}

// TestBuild_DuplicateEdgeRejected: two identical edges are rejected.
func TestBuild_DuplicateEdgeRejected(t *testing.T) {
	_, err := Build(NewSpec().
		AddNode(node("a")).AddNode(node("b")).
		AddEdge(Edge{From: "a", To: "b"}).
		AddEdge(Edge{From: "a", To: "b"}))
	if err == nil || !strings.Contains(err.Error(), "duplicate edge") {
		t.Fatalf("expected duplicate-edge error, got %v", err)
	}
}

// TestBuild_EmptyRejected: an empty graph is rejected.
func TestBuild_EmptyRejected(t *testing.T) {
	if _, err := Build(NewSpec()); err == nil {
		t.Fatal("expected an error for an empty graph")
	}
}

// TestBuild_NodeCeiling: exceeding the ceiling is rejected.
func TestBuild_NodeCeiling(t *testing.T) {
	s := NewSpec().SetMaxNodes(2).AddNode(node("a")).AddNode(node("b")).AddNode(node("c"))
	if _, err := Build(s); err == nil || !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("expected ceiling error, got %v", err)
	}
}

// TestBuild_EntryMustBeSource: an entry with in-edges is rejected.
func TestBuild_EntryMustBeSource(t *testing.T) {
	_, err := Build(NewSpec().
		AddNode(node("a")).AddNode(node("b")).
		AddEdge(Edge{From: "a", To: "b"}).
		SetEntry("b")) // b has an in-edge
	if err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("expected entry-not-source error, got %v", err)
	}
}

// TestBuild_UnknownEntryRejected: an entry that is not a node is rejected.
func TestBuild_UnknownEntryRejected(t *testing.T) {
	_, err := Build(NewSpec().AddNode(node("a")).SetEntry("ghost"))
	if err == nil || !strings.Contains(err.Error(), "entry") {
		t.Fatalf("expected unknown-entry error, got %v", err)
	}
}
