package graph

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// BLOCKER (ADR-0060 plan Task 5 / constitutional gate "structured output fails
// closed"): a malformed/missing upstream payload NEVER reaches the downstream
// agent. Three cases, each asserting the downstream node issued ZERO LLM calls.

// TestFailClosed_EdgeMapErrorNoDownstreamRun: an Edge.Map that returns an error
// fails the graph and the destination node is never invoked.
func TestFailClosed_EdgeMapErrorNoDownstreamRun(t *testing.T) {
	fc := newFakeClient()
	fc.text = textReply

	writer := Node{ID: "writer", System: "sys-writer", Prompt: func(_ GraphInput, from map[string]EdgePayload) (string, error) {
		return "write: " + from["researcher"].Text, nil
	}}
	g, err := Build(NewSpec().
		AddNode(Node{ID: "researcher", System: "sys-researcher"}).
		AddNode(writer).
		AddEdge(Edge{From: "researcher", To: "writer", Map: func(NodeResult) (EdgePayload, error) {
			return EdgePayload{}, errors.New("upstream shape mismatch")
		}}).
		SetEntry("researcher"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	_, err = Execute(context.Background(), noToolDeps(fc), g, GraphInput{Prompt: "topic"})
	if err == nil || !strings.Contains(err.Error(), "map") {
		t.Fatalf("expected an edge-map failure, got %v", err)
	}
	// The downstream agent must NEVER have been invoked on a malformed input.
	if n := fc.countSystem("sys-writer"); n != 0 {
		t.Fatalf("FAIL-CLOSED VIOLATED: downstream 'writer' issued %d LLM calls, want 0", n)
	}
	// The researcher DID run — the failure is on the edge, not the source.
	if fc.countSystem("sys-researcher") == 0 {
		t.Fatal("researcher should have run before the edge failed")
	}
}

// TestFailClosed_PromptFuncRejectsEmptyUpstream: the edge hands the writer an
// EMPTY payload (a mapping that yielded nothing); the writer's PromptFunc rejects
// it. The writer node is scheduled (its in-edge resolved) but fails at prompt
// assembly — BEFORE any LLM call — so it issues zero calls.
func TestFailClosed_PromptFuncRejectsEmptyUpstream(t *testing.T) {
	fc := newFakeClient()
	fc.text = textReply // researcher produces a normal (non-empty) output

	var writerPromptCalls int
	writer := Node{ID: "writer", System: "sys-writer", Prompt: func(_ GraphInput, from map[string]EdgePayload) (string, error) {
		writerPromptCalls++
		if strings.TrimSpace(from["researcher"].Text) == "" {
			return "", errors.New("writer: missing researcher findings")
		}
		return "write", nil
	}}
	g, err := Build(NewSpec().
		AddNode(Node{ID: "researcher", System: "sys-researcher"}).
		AddNode(writer).
		// The edge maps the researcher's output to an EMPTY payload — the writer must
		// fail closed on it rather than run the agent on nothing.
		AddEdge(Edge{From: "researcher", To: "writer", Map: func(NodeResult) (EdgePayload, error) {
			return EdgePayload{Text: ""}, nil
		}}).
		SetEntry("researcher"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	_, err = Execute(context.Background(), noToolDeps(fc), g, GraphInput{Prompt: "topic"})
	if err == nil || !strings.Contains(err.Error(), "missing researcher findings") {
		t.Fatalf("expected a fail-closed PromptFunc error, got %v", err)
	}
	if writerPromptCalls != 1 {
		t.Fatalf("writer PromptFunc ran %d times, want exactly 1 (it was scheduled, then rejected)", writerPromptCalls)
	}
	// The decisive assertion: the writer agent issued ZERO LLM calls — the malformed
	// (empty) upstream never reached the model.
	if n := fc.countSystem("sys-writer"); n != 0 {
		t.Fatalf("FAIL-CLOSED VIOLATED: writer issued %d LLM calls on an empty upstream, want 0", n)
	}
}

// TestFailClosed_NodeErrorStopsDependents: a node's own provider failure fails the
// graph and its dependents never run (the malformed graph never reaches the writer).
func TestFailClosed_NodeErrorStopsDependents(t *testing.T) {
	fc := newFakeClient()
	fc.text = textReply
	// The researcher's provider call fails outright (not a reflectable tool error).
	fc.streamErr = map[string]error{"sys-researcher": errors.New("provider unreachable")}

	deps := noToolDeps(fc)
	g, err := Build(NewSpec().
		AddNode(Node{ID: "researcher", System: "sys-researcher"}).
		AddNode(Node{ID: "writer", System: "sys-writer", Prompt: func(GraphInput, map[string]EdgePayload) (string, error) { return "w", nil }}).
		AddEdge(Edge{From: "researcher", To: "writer"}).
		SetEntry("researcher"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, err := Execute(context.Background(), deps, g, GraphInput{Prompt: "x"}); err == nil {
		t.Fatal("expected the graph to fail on the node's provider error")
	}
	if n := fc.countSystem("sys-writer"); n != 0 {
		t.Fatalf("FAIL-CLOSED VIOLATED: writer ran after its upstream failed (%d calls)", n)
	}
}
