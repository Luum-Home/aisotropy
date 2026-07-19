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
	// rec: fw, reported model, wall time (ms). A leak needs BOTH the real model
	// name AND a real-call-like wall time (>= leakLatencyMs).
	rec := func(fw, model string, wallMs int64) record {
		return record{Framework: fw, WallMs: wallMs, Out: adapterOutput{Model: model}}
	}
	records := []record{
		rec("ai-sdk", "mock-llm", 300),             // clean stub
		rec("aisotropy", "agnes-2.0-flash", 20000), // LEAK: real model + real-call latency (~20s)
		rec("aisotropy", "agnes-2.0-flash", 18000), // same fw again — must dedupe
		rec("eino", "stub", 250),                   // clean stub
		rec("mastra", "", 400),                     // no model reported — ignored
		rec("langgraph", "Agnes-2.0-Flash", 49),    // NOT a leak: reports agnes but 49ms = offline stub mislabel
		rec("genkit-go", "agnes-2.0-flash", 700),   // NOT a leak: 700ms fast stub mislabel
	}

	got := laneLeakFrameworks(records, "agnes-2.0-flash")
	want := []string{"aisotropy"} // only the slow real-call, deduped; the fast mislabels are excluded
	if !reflect.DeepEqual(got, want) {
		t.Errorf("laneLeakFrameworks = %v, want %v", got, want)
	}

	// A fully-clean stub run has no leaks (all fast, mock names).
	clean := []record{rec("ai-sdk", "mock-llm", 300), rec("eino", "stub", 250), rec("mastra", "mock", 400)}
	if got := laneLeakFrameworks(clean, "agnes-2.0-flash"); len(got) != 0 {
		t.Errorf("clean stub run reported leaks: %v", got)
	}

	// A fast stub that mislabels its model as the real one is NOT a leak.
	mislabel := []record{rec("langgraph", "agnes-2.0-flash", 49), rec("genkit-go", "agnes-2.0-flash", 700)}
	if got := laneLeakFrameworks(mislabel, "agnes-2.0-flash"); len(got) != 0 {
		t.Errorf("fast mislabeled stubs should NOT be flagged as leaks, got %v", got)
	}

	// An empty real-model name never flags (defensive).
	if got := laneLeakFrameworks(records, ""); got != nil {
		t.Errorf("empty realModel should flag nothing, got %v", got)
	}
}
