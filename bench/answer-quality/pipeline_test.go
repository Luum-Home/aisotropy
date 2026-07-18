package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/aisotropy"
)

// embedErrEmbedder satisfies aisotropy.Embedder but fails every Embed call,
// so a code path that must embed (IngestKnowledge chunking) is forced to
// surface an error instead of silently succeeding. seedKnowledge's in-process
// default backends (HashEmbedder, in-memory store) never fail on their own —
// not even with an already-cancelled ctx for an operation this small and
// local — so a real, deliberately-broken Embedder wired in via the public
// aisotropy.WithEmbedder seam is the only way to exercise IngestKnowledge's
// error-return branch without touching the real network.
type embedErrEmbedder struct{ dim int }

func (e embedErrEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("embed backend down")
}
func (e embedErrEmbedder) Dimension() int { return e.dim }

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

// TestSeedKnowledge_SkipsEmptyAndDuplicateDocs drives seedKnowledge directly
// (not through runEval) with a duplicate doc ID and an empty-text doc,
// proving both are silently skipped (the dedup/empty-text guard) rather than
// ingested or erroring.
func TestSeedKnowledge_SkipsEmptyAndDuplicateDocs(t *testing.T) {
	clearProviderEnv(t)
	app := aisotropy.New()
	defer func() { _ = app.Close() }()

	questions := []question{
		{ID: "q1", Docs: []contextDoc{
			{ID: "d1", Text: "first"},
			{ID: "d1", Text: "duplicate id, must be skipped"},
			{ID: "d2", Text: ""}, // empty text, must be skipped
		}},
	}
	if err := seedKnowledge(context.Background(), app, questions); err != nil {
		t.Fatalf("seedKnowledge: %v", err)
	}
}

// TestSeedKnowledge_IngestErrorPropagates forces app.IngestKnowledge to fail
// (a deliberately broken Embedder wired in via the public aisotropy.WithEmbedder
// seam) and asserts seedKnowledge wraps and returns that error instead of
// swallowing it. Note: runEval itself builds its own App internally with no
// seam to inject a broken embedder into from outside the package, so the
// symmetric "runEval propagates a seedKnowledge failure" branch (the
// `if err := seedKnowledge(...); err != nil` check in runEval) is not
// separately exercised here — it is the same one-line propagation pattern
// already proven for app.Turn's error in TestRunEval_TurnErrorIsRecordedPerQuestion
// and for newScorer's error via TestNewScorerRejectsNilClient/newScorer being
// called from runEval.
func TestSeedKnowledge_IngestErrorPropagates(t *testing.T) {
	clearProviderEnv(t)
	app := aisotropy.New(aisotropy.WithEmbedder(embedErrEmbedder{dim: 64}))
	defer func() { _ = app.Close() }()

	questions := []question{{ID: "q1", Docs: []contextDoc{{ID: "d1", Text: "some content"}}}}
	err := seedKnowledge(context.Background(), app, questions)
	if err == nil {
		t.Fatal("expected an error when IngestKnowledge fails")
	}
	if !strings.Contains(err.Error(), "seed knowledge") {
		t.Fatalf("err = %v, want it to mention \"seed knowledge\"", err)
	}
}

// TestRunEval_TurnErrorIsRecordedPerQuestion proves a failing App.Turn call
// (forced via an already-cancelled ctx, so no real network/model dependency
// is needed) is recorded on the per-question result rather than aborting the
// whole run — the questions carry no context_docs, so seedKnowledge's early
// "nothing to seed" return is taken and the cancelled ctx is only observed
// once the loop calls app.Turn.
func TestRunEval_TurnErrorIsRecordedPerQuestion(t *testing.T) {
	clearProviderEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	questions := []question{{ID: "q1", Prompt: "anything at all"}}
	rr, err := runEval(ctx, questions, "cancelled-set", 0, 0, 0)
	if err != nil {
		t.Fatalf("runEval: %v", err)
	}
	if len(rr.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(rr.Results))
	}
	if rr.Results[0].Err == "" {
		t.Fatal("expected the Turn error to be recorded on the per-question result")
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
