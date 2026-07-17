package main

import (
	"context"
	"strings"
	"testing"
)

// clearProviderEnv forces the serving and judge clients to the keyless stub, so
// the end-to-end test runs with zero credentials and zero network — proving the
// skip-cleanly path and the full turn->judge->report wiring.
func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"OPENROUTER_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY",
		"AISOTROPY_MODEL", "AISOTROPY_JUDGE_MODEL",
	} {
		t.Setenv(k, "")
	}
}

// TestRunEval_StubEndToEnd boots the real App, runs two questions through the
// full turn pipeline on the stub, scores them, and asserts the harness produced
// a complete, honestly-labeled result: stub serving, stub judge, both marked
// not-real, per-question results present, and measured token spend recorded.
func TestRunEval_StubEndToEnd(t *testing.T) {
	clearProviderEnv(t)

	questions := []question{
		{ID: "grounded", Prompt: "What is the capital of France?", Ideal: "Paris",
			Context: "The capital of France is Paris.",
			Docs:    []contextDoc{{ID: "fr", Text: "The capital of France is Paris."}}},
		{ID: "open", Prompt: "Explain gravity in one sentence."},
	}

	rr, err := runEval(context.Background(), questions, "unit-test-set", 0, 0, 0)
	if err != nil {
		t.Fatalf("runEval: %v", err)
	}
	if rr.ServingReal || rr.JudgeReal {
		t.Fatalf("with no key both providers must be stub: serving=%v judge=%v", rr.ServingReal, rr.JudgeReal)
	}
	if len(rr.Results) != 2 {
		t.Fatalf("got %d results; want 2", len(rr.Results))
	}
	for _, r := range rr.Results {
		if r.Err != "" {
			t.Fatalf("question %q errored: %s", r.ID, r.Err)
		}
		if r.Answer == "" {
			t.Errorf("question %q produced an empty answer", r.ID)
		}
	}
	if rr.Results[0].Scores.faithfulnessScored != true {
		t.Error("grounded question should have faithfulness scored")
	}
	if rr.Results[1].Scores.faithfulnessScored != false {
		t.Error("open question should not have faithfulness scored")
	}
	if rr.TurnInTok+rr.TurnOutTok == 0 || rr.JudgeInTok+rr.JudgeOutTok == 0 {
		t.Errorf("expected non-zero measured spend: turn=%d/%d judge=%d/%d",
			rr.TurnInTok, rr.TurnOutTok, rr.JudgeInTok, rr.JudgeOutTok)
	}

	// The rendered report must carry the stub honesty banner.
	report := render(rr)
	if !strings.Contains(report, "STUB RUN") {
		t.Error("report must loudly label a stub run")
	}
}

// TestRunEval_TokenBudgetAborts proves the runaway-spend guard: a tiny token
// budget stops the loop before all questions run.
func TestRunEval_TokenBudgetAborts(t *testing.T) {
	clearProviderEnv(t)

	questions := []question{
		{ID: "one", Prompt: "First question about anything at all."},
		{ID: "two", Prompt: "Second question."},
		{ID: "three", Prompt: "Third question."},
	}
	// A 1-token budget is crossed after the first question's spend, so the
	// loop aborts before finishing all three.
	rr, err := runEval(context.Background(), questions, "budget-test", 0, 0, 1)
	if err != nil {
		t.Fatalf("runEval: %v", err)
	}
	if !rr.Aborted {
		t.Fatal("expected the run to abort on the token budget")
	}
	if len(rr.Results) >= len(questions) {
		t.Fatalf("abort should have stopped early; ran %d of %d", len(rr.Results), len(questions))
	}
}
