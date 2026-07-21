package main

import (
	"reflect"
	"testing"
)

// This file tests the -tasks subset filter (parseTaskFilter/filterTasks/
// filterRagQuestions in tasks.go), added so a bounded calibration run (e.g.
// "hard tier only, 3 frameworks") no longer needs a hand-built scratch -live
// directory — the exact mechanism that broke the mastra (.env) and aisotropy
// (_shared/corpus) adapters, since both resolve auxiliary paths relative to
// -live. -tasks filters the in-memory slices loaded from the REAL -live
// directory instead, so adapter path resolution never changes.

// TestTaskFilter_FiltersBothSuiteTasksAndRagQuestions proves a single -tasks
// value filters BOTH input collections by exact ID, as required: suite tasks
// (arenas/live/tasks/suite.jsonl) and rag questions (arenas/live/_shared/
// rag_questions.jsonl).
func TestTaskFilter_FiltersBothSuiteTasksAndRagQuestions(t *testing.T) {
	tasks := loadRealSuite(t)
	rag := loadRealRag(t)

	tf := parseTaskFilter("qa-h01-bones,mem-h01-overwrite,h1,h2,h4")
	if tf == nil {
		t.Fatal("parseTaskFilter returned nil for a non-empty flag value")
	}

	gotTasks := filterTasks(tasks, tf)
	gotRag := filterRagQuestions(rag, tf)

	wantTaskIDs := map[string]bool{"qa-h01-bones": true, "mem-h01-overwrite": true}
	if len(gotTasks) != len(wantTaskIDs) {
		t.Fatalf("filtered suite tasks: got %d, want %d (%v)", len(gotTasks), len(wantTaskIDs), gotTasks)
	}
	for _, ts := range gotTasks {
		if !wantTaskIDs[ts.ID] {
			t.Errorf("unexpected suite task %q survived the filter", ts.ID)
		}
	}

	wantRagIDs := map[string]bool{"h1": true, "h2": true, "h4": true}
	if len(gotRag) != len(wantRagIDs) {
		t.Fatalf("filtered rag questions: got %d, want %d (%v)", len(gotRag), len(wantRagIDs), gotRag)
	}
	for _, q := range gotRag {
		if !wantRagIDs[q.ID] {
			t.Errorf("unexpected rag question %q survived the filter", q.ID)
		}
	}

	// buildWork downstream of the filtered inputs must ALSO shrink to exactly
	// the selected items — the filter is not just a count check, it must
	// actually reach the work-item builder unchanged.
	// The filter selected one single_shot_qa task (qa-h01-bones) and one
	// multi_turn_memory task (mem-h01-overwrite), so qa mode must produce
	// exactly the former.
	qaItems := buildWork("fake-fw", "qa", gotTasks, gotRag, 0)
	if len(qaItems) != 1 || qaItems[0].ID != "qa-h01-bones" {
		t.Errorf("qa mode over the filtered set: got %+v, want exactly [qa-h01-bones]", qaItems)
	}
	memItems := buildWork("fake-fw", "memory", gotTasks, gotRag, 0)
	if len(memItems) != 1 || memItems[0].ID != "mem-h01-overwrite" {
		t.Errorf("memory mode over the filtered set: got %+v, want exactly [mem-h01-overwrite]", memItems)
	}
	ragItems := buildWork("fake-fw", "rag", gotTasks, gotRag, 0)
	if len(ragItems) != 3 {
		t.Errorf("rag mode over the filtered set: got %d items, want 3", len(ragItems))
	}
}

// TestTaskFilter_EmptyIsByteIdenticalToUnset proves an empty/unset -tasks
// value is a true no-op: parseTaskFilter("") is nil, and filterTasks/
// filterRagQuestions return the EXACT input slice unchanged (same length,
// same elements, same order) — the exact pre-flag behavior.
func TestTaskFilter_EmptyIsByteIdenticalToUnset(t *testing.T) {
	tasks := loadRealSuite(t)
	rag := loadRealRag(t)

	if tf := parseTaskFilter(""); tf != nil {
		t.Fatalf("parseTaskFilter(\"\") = %+v, want nil", tf)
	}
	if tf := parseTaskFilter("   "); tf != nil {
		t.Fatalf("parseTaskFilter(whitespace) = %+v, want nil", tf)
	}

	gotTasks := filterTasks(tasks, parseTaskFilter(""))
	gotRag := filterRagQuestions(rag, parseTaskFilter(""))

	if !reflect.DeepEqual(gotTasks, tasks) {
		t.Errorf("filterTasks with unset -tasks mutated the suite task set: got %d tasks, want %d unchanged", len(gotTasks), len(tasks))
	}
	if !reflect.DeepEqual(gotRag, rag) {
		t.Errorf("filterRagQuestions with unset -tasks mutated the rag question set: got %d, want %d unchanged", len(gotRag), len(rag))
	}

	// And the resulting work items must match exactly what buildWork produces
	// with the raw, unfiltered inputs (today's behavior) for every mode.
	for _, mode := range allModes {
		want := buildWork("fake-fw", mode, tasks, rag, 0)
		got := buildWork("fake-fw", mode, gotTasks, gotRag, 0)
		if len(got) != len(want) {
			t.Errorf("mode %s: unset -tasks changed the work item count: got %d, want %d", mode, len(got), len(want))
		}
	}
}

// TestTaskFilter_PrefixFormSelectsWholeHardTier proves the trailing-"*"
// prefix syntax expresses "all hard QA" (or any ID family) without listing
// every member — the syntax called out in the -tasks flag help text.
func TestTaskFilter_PrefixFormSelectsWholeHardTier(t *testing.T) {
	tasks := loadRealSuite(t)

	tf := parseTaskFilter("qa-h*")
	got := filterTasks(tasks, tf)

	wantIDs := []string{
		"qa-h01-bones", "qa-h02-canberra", "qa-h03-iron",
		"qa-h04-berlin", "qa-h05-boiling", "qa-h06-minutes",
	}
	if len(got) != len(wantIDs) {
		t.Fatalf("qa-h* matched %d tasks, want %d: %v", len(got), len(wantIDs), got)
	}
	gotSet := map[string]bool{}
	for _, ts := range got {
		gotSet[ts.ID] = true
		if ts.Kind != "single_shot_qa" {
			t.Errorf("qa-h* matched non-QA task %s (kind %s)", ts.ID, ts.Kind)
		}
	}
	for _, id := range wantIDs {
		if !gotSet[id] {
			t.Errorf("qa-h* did not match expected hard-tier task %s", id)
		}
	}
	// It must NOT pull in the easy tier (qa-01-capital, etc.) or the mem-h*
	// family — the prefix is matched literally, not fuzzily.
	for _, ts := range got {
		if ts.ID == "qa-01-capital" || ts.ID == "mem-h01-overwrite" {
			t.Errorf("qa-h* incorrectly matched %s", ts.ID)
		}
	}

	// A mix of prefix and exact forms in the same flag value works together.
	mixed := parseTaskFilter("qa-h*,mem-h01-overwrite")
	gotMixed := filterTasks(tasks, mixed)
	if len(gotMixed) != len(wantIDs)+1 {
		t.Errorf("mixed prefix+exact filter matched %d tasks, want %d", len(gotMixed), len(wantIDs)+1)
	}
}

// TestTaskFilter_BareIDIsExactOnly guards the chosen syntax: an ID WITHOUT a
// trailing "*" is an exact match only, never an implicit prefix — "qa-h01"
// must not also match "qa-h01-bones".
func TestTaskFilter_BareIDIsExactOnly(t *testing.T) {
	tasks := loadRealSuite(t)
	tf := parseTaskFilter("qa-h01")
	got := filterTasks(tasks, tf)
	if len(got) != 0 {
		t.Errorf("bare id %q with no trailing '*' unexpectedly prefix-matched %v", "qa-h01", got)
	}
}
