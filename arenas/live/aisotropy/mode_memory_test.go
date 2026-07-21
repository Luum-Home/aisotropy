package main

import (
	"strings"
	"testing"
)

// TestWithEstablishedContext is a pure unit test of the F1a formatting helper:
// it must reproduce every established fact verbatim (order preserved) ahead
// of the live user text, and must not silently drop or reorder anything.
func TestWithEstablishedContext(t *testing.T) {
	facts := []string{
		"Booked: Skylark Line, next Tuesday, seat 14C.",
		"Done — your seat has been changed from 14C to 22F.",
	}
	got := withEstablishedContext(facts, "Which seat number am I booked in right now?")

	for _, f := range facts {
		if !strings.Contains(got, f) {
			t.Errorf("withEstablishedContext output missing established fact %q; got:\n%s", f, got)
		}
	}
	if !strings.Contains(got, "Which seat number am I booked in right now?") {
		t.Errorf("withEstablishedContext output missing the live user text; got:\n%s", got)
	}
	// Order: the first fact must appear before the second, and both before the
	// live question — otherwise "already established" context would read as
	// if the LATER fact happened first.
	idx1 := strings.Index(got, facts[0])
	idx2 := strings.Index(got, facts[1])
	idxQ := strings.Index(got, "Which seat number")
	if !(idx1 < idx2 && idx2 < idxQ) {
		t.Errorf("withEstablishedContext output out of order; got:\n%s", got)
	}
}

// TestWithEstablishedContext_NoFacts is the degenerate single-turn case (the
// recall invocation, or the very first persist turn): with zero established
// facts the caller (runMemory) never even calls this helper, but a direct
// call should still behave sanely (echo the user text, no stray context
// block) rather than emit something malformed.
func TestWithEstablishedContext_NoFacts(t *testing.T) {
	got := withEstablishedContext(nil, "Book me on the Skylark Line.")
	if !strings.Contains(got, "Book me on the Skylark Line.") {
		t.Errorf("withEstablishedContext(nil, ...) dropped the user text; got:\n%s", got)
	}
}

// TestRunMemory_CannedAssistantLinesReachTheModel is the OFFLINE (ARENA_STUB=1,
// zero network) regression test for the F1a fix: arenas/live/aisotropy's
// runMemory used to build its per-turn Prompt from userMessages(req.Messages)
// alone, silently discarding every assistant-role line the suite supplied
// (see arenas/live/runner/tasks.go's buildMemory, which interleaves canned
// assistant turns with the user turns on the persist invocation) — the
// documented root cause of the refusal cascade in
// docs/architecture/arena-memory-forensics.md.
//
// aisotropy's native internal/llm.StubClient (selected automatically when
// ARENA_STUB=1 clears every provider key, see agnes_client.go's
// newAgnesClient) echoes the exact prompt string it receives back in its
// reply (internal/llm/stub.go's buildStubWords: `received your message
// "<prompt>"`). So if the canned assistant line for the FIRST turn now
// reaches the SECOND turn's Prompt (via withEstablishedContext), the stub's
// echoed reply on that second turn must contain it verbatim — proving the
// wiring end-to-end without ever calling a real model.
func TestRunMemory_CannedAssistantLinesReachTheModel(t *testing.T) {
	t.Setenv("ARENA_STUB", "1")
	t.Setenv("AISOTROPY_DATA_DIR", "") // in-memory working memory for this test

	const cannedBookingConfirmation = "Booked: Skylark Line, next Tuesday, seat 14C."

	req := &request{
		Mode:      modeMemory,
		System:    "You are a helpful assistant with a good memory. Answer concisely.",
		SessionID: "test-mem-established-context",
		Messages: []inMessage{
			{Role: "user", Content: "Book me on the Skylark Line next Tuesday. Put me in seat 14C."},
			{Role: "assistant", Content: cannedBookingConfirmation},
			{Role: "user", Content: "What is the end-to-end travel time on that line again?"},
		},
	}
	out := &result{Mode: modeMemory}

	if err := runMemory(req, out); err != nil {
		t.Fatalf("runMemory returned an error in the stub lane: %v", err)
	}
	if out.Error != "" {
		t.Fatalf("runMemory set out.Error: %s", out.Error)
	}
	if !strings.Contains(out.Reply, cannedBookingConfirmation) {
		t.Errorf("second turn's reply does not echo the first turn's canned assistant line %q — "+
			"the established-context fix is not reaching the model; got reply:\n%s",
			cannedBookingConfirmation, out.Reply)
	}
}

// TestEffectiveMemorySystem_F1bFallback is the regression test for the F1b
// fix: the runner's recall invocation (arenas/live/runner/tasks.go's
// buildMemory) sends no "system" field by design, so before this fix
// newApp(agnes, "") added no WithPersona at all and the terse built-in
// default served instead (internal/api/persona.go's systemPromptFor: "Respond
// briefly and directly."). Verified directly at the point the fallback
// decision is made (effectiveMemorySystem), not by scraping a stub reply:
// aisotropy's own fail-closed structured-output wrapping (ChatCore's
// JSON-schema instruction, see internal/llm/stub.go's stubStructuredReply)
// means the system prompt text never reaches the stub's echoed reply, so a
// reply-content assertion would not actually exercise this path — see
// TestRunMemory_CannedAssistantLinesReachTheModel's doc comment for how that
// was discovered.
func TestEffectiveMemorySystem_F1bFallback(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty system falls back to the memory-friendly persona", "", memoryRecallPersona},
		{"non-empty system (the persist call's real task system) passes through unchanged",
			"You are a helpful assistant with a good memory. Answer concisely.",
			"You are a helpful assistant with a good memory. Answer concisely."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := effectiveMemorySystem(c.in); got != c.want {
				t.Errorf("effectiveMemorySystem(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestRunMemory_RecallInvocationRunsCleanWithNoSystem is the OFFLINE
// (ARENA_STUB=1) end-to-end smoke check that a recall-shaped invocation (no
// System, exactly what buildMemory sends) runs successfully start to finish
// through runMemory with the F1b fallback wired in — i.e. the fallback does
// not merely compute correctly in isolation (TestEffectiveMemorySystem_F1bFallback)
// but is actually threaded into newApp without erroring.
func TestRunMemory_RecallInvocationRunsCleanWithNoSystem(t *testing.T) {
	t.Setenv("ARENA_STUB", "1")
	t.Setenv("AISOTROPY_DATA_DIR", "")

	req := &request{
		Mode:      modeMemory,
		System:    "", // exactly what buildMemory sends on the recall invocation
		SessionID: "test-mem-recall-persona",
		Messages: []inMessage{
			{Role: "user", Content: "Which seat number am I booked in right now?"},
		},
	}
	out := &result{Mode: modeMemory}

	if err := runMemory(req, out); err != nil {
		t.Fatalf("runMemory returned an error in the stub lane: %v", err)
	}
	if out.Error != "" {
		t.Fatalf("runMemory set out.Error: %s", out.Error)
	}
	if out.Reply == "" {
		t.Fatalf("runMemory produced an empty reply")
	}
}
