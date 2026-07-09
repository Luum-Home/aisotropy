package agentloop

import "testing"

// TestParseDecisionToleratesProseBraces locks in the review fix: a naive
// first-'{'/last-'}' slice would capture "{the tool}: {json}" and fail; the
// balanced-object scan finds the real decision.
func TestParseDecisionToleratesProseBraces(t *testing.T) {
	raw := `Sure, I will use {the tool}: {"tool":"search_docs","args":{"q":"x"}}`
	dec, err := parseDecision(raw)
	if err != nil {
		t.Fatalf("parseDecision: %v", err)
	}
	if dec.Tool != "search_docs" {
		t.Fatalf("tool = %q, want search_docs", dec.Tool)
	}
	if dec.Args["q"] != "x" {
		t.Fatalf("args = %v", dec.Args)
	}
}

// TestParseDecisionPrefersToolOverDecoy: a valid-but-toolless object before the
// real one must not shadow the concrete tool choice.
func TestParseDecisionPrefersToolOverDecoy(t *testing.T) {
	raw := `{"note":1} then {"tool":"write_tasks","args":{}}`
	dec, err := parseDecision(raw)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Tool != "write_tasks" {
		t.Fatalf("tool = %q, want write_tasks", dec.Tool)
	}
}

// TestBalancedJSONObjectsIgnoresBracesInStrings: a '}' inside a JSON string
// must not terminate the object early.
func TestBalancedJSONObjectsIgnoresBracesInStrings(t *testing.T) {
	got := balancedJSONObjects(`{"tool":"a}b","args":{}}`)
	if len(got) != 1 {
		t.Fatalf("got %d objects, want 1: %v", len(got), got)
	}
	if got[0] != `{"tool":"a}b","args":{}}` {
		t.Fatalf("captured %q", got[0])
	}
}
