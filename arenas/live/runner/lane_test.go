package main

import (
	"reflect"
	"testing"
)

// TestLaneLeakFrameworks is the P0 reporting-discipline guard: during a STUB
// run, any adapter that reported the REAL model name actually called the real
// provider (the exact aisotropy bug — a "stub" run hitting agnes-2.0-flash).
// The scan is deterministic and case-insensitive; stub model names never
// contain the real model string, so a match is an unambiguous leak.
func TestLaneLeakFrameworks(t *testing.T) {
	rec := func(fw, model string) record {
		return record{Framework: fw, Out: adapterOutput{Model: model}}
	}
	records := []record{
		rec("ai-sdk", "mock-llm"),           // clean stub
		rec("aisotropy", "agnes-2.0-flash"), // LEAK: real model in a stub run
		rec("aisotropy", "agnes-2.0-flash"), // same fw again — must dedupe
		rec("eino", "stub"),                 // clean stub
		rec("mastra", ""),                   // no model reported — ignored
		rec("langgraph", "Agnes-2.0-Flash"), // LEAK: case-insensitive match
	}

	got := laneLeakFrameworks(records, "agnes-2.0-flash")
	want := []string{"aisotropy", "langgraph"} // sorted, deduped
	if !reflect.DeepEqual(got, want) {
		t.Errorf("laneLeakFrameworks = %v, want %v", got, want)
	}

	// A fully-clean stub run has no leaks.
	clean := []record{rec("ai-sdk", "mock-llm"), rec("eino", "stub"), rec("mastra", "mock")}
	if got := laneLeakFrameworks(clean, "agnes-2.0-flash"); len(got) != 0 {
		t.Errorf("clean stub run reported leaks: %v", got)
	}

	// An empty real-model name never flags (defensive).
	if got := laneLeakFrameworks(records, ""); got != nil {
		t.Errorf("empty realModel should flag nothing, got %v", got)
	}
}
