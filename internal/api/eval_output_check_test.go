package api

// eval_output_check_test.go covers the EvalOutputCheck adapter (an eval.Judge
// wired onto the guardrails.OutputCheck seam): it flags low-safety-score output,
// passes high-safety output, resolves a judge operational error per the
// operator's blocking posture, and — end-to-end — refuses a judge-flagged
// response on the streaming route when hard output-blocking is on.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/eval"
	"github.com/Luum-Home/aisotropy/internal/guardrails"
)

// stubJudge is a deterministic eval.Judge for exercising EvalOutputCheck without
// a model: it returns a fixed normalized safety score, or a fixed operational
// error. It always reports the Safety criterion.
type stubJudge struct {
	value float64
	err   error
}

func (stubJudge) Criterion() eval.Criterion { return eval.Safety }

func (s stubJudge) Score(_ context.Context, _ eval.EvalSample) (eval.Score, error) {
	if s.err != nil {
		return eval.Score{}, s.err
	}
	return eval.Score{Criterion: eval.Safety.Name, Value: s.value}, nil
}

// TestEvalOutputCheck_FlagsLowSafety proves a below-threshold safety score is
// reported Suspicious (for eval.Safety, low score == unsafe).
func TestEvalOutputCheck_FlagsLowSafety(t *testing.T) {
	check, err := NewEvalOutputCheck(stubJudge{value: 0.1}, 0.5, false)
	if err != nil {
		t.Fatalf("NewEvalOutputCheck: %v", err)
	}
	if check.Name() != "eval:safety" {
		t.Fatalf("Name = %q, want eval:safety", check.Name())
	}
	v := check.Check(context.Background(), guardrails.OutputCheckInput{
		Input:  "how do I stay safe?",
		Output: "something clearly unsafe",
	})
	if !v.Suspicious {
		t.Fatalf("expected a low-safety output to be Suspicious, got %+v", v)
	}
	if v.Score <= 0.5 {
		t.Fatalf("problem-confidence should be high for a 0.1 safety score, got %v", v.Score)
	}
}

// TestEvalOutputCheck_PassesHighSafety proves an at/above-threshold safety score
// is not flagged.
func TestEvalOutputCheck_PassesHighSafety(t *testing.T) {
	check, _ := NewEvalOutputCheck(stubJudge{value: 0.95}, 0.5, false)
	v := check.Check(context.Background(), guardrails.OutputCheckInput{Output: "a perfectly safe answer"})
	if v.Suspicious {
		t.Fatalf("expected a high-safety output to pass, got %+v", v)
	}
}

// TestEvalOutputCheck_ErrorPosture proves a judge OPERATIONAL failure is
// fail-open (not Suspicious) by default and fail-closed (Suspicious) when the
// adapter is built for hard-blocking.
func TestEvalOutputCheck_ErrorPosture(t *testing.T) {
	failing := stubJudge{err: errors.New("model unreachable")}

	open, _ := NewEvalOutputCheck(failing, 0.5, false)
	if v := open.Check(context.Background(), guardrails.OutputCheckInput{Output: "x"}); v.Suspicious {
		t.Fatalf("fail-open: a judge error must not be Suspicious, got %+v", v)
	}

	closed, _ := NewEvalOutputCheck(failing, 0.5, true)
	if v := closed.Check(context.Background(), guardrails.OutputCheckInput{Output: "x"}); !v.Suspicious {
		t.Fatalf("fail-closed: a judge error must be Suspicious, got %+v", v)
	}
}

// TestEvalOutputCheck_NilJudgeRejected proves construction fails closed on a nil
// judge.
func TestEvalOutputCheck_NilJudgeRejected(t *testing.T) {
	if _, err := NewEvalOutputCheck(nil, 0.5, false); err == nil {
		t.Fatal("expected an error for a nil judge")
	}
}

// TestEvalOutputCheck_BlocksStreamingWhenAttached is the end-to-end proof: a
// stub judge that scores everything unsafe, attached to the output inspector via
// WithChecks, causes the streaming route (BlockOutput on) to buffer and refuse a
// response the built-in heuristics would have passed — so the judge-flagged text
// never reaches the SSE stream.
func TestEvalOutputCheck_BlocksStreamingWhenAttached(t *testing.T) {
	check, err := NewEvalOutputCheck(stubJudge{value: 0.0}, 0.5, true)
	if err != nil {
		t.Fatalf("NewEvalOutputCheck: %v", err)
	}
	inspector := guardrails.NewOutputInspector(nil).WithChecks(check)
	gg := guardrails.New(guardrails.Config{Output: inspector})
	guard := &Guard{G: gg, BlockOutput: true}

	wm := newTestWorkingMemory(t)
	// A reply the built-in toxicity/leak heuristics would NOT flag on their own,
	// so a block here can only come from the attached judge.
	handler := newGuardStreamRouter(t, scriptedClient{text: "an otherwise clean but judged-unsafe answer"}, wm, guard)

	events := postChatStream(t, handler, "acme", "alice", "hi", "s1")

	var joined strings.Builder
	var sawDone bool
	for _, ev := range events {
		if ev.Event == sseEventToken {
			joined.Write(ev.Data)
		}
		if ev.Event == sseEventDone {
			sawDone = true
		}
	}
	if strings.Contains(joined.String(), "judged-unsafe answer") {
		t.Fatalf("judge-flagged output was streamed despite BlockOutput: %q", joined.String())
	}
	if !strings.Contains(joined.String(), outputGuardRefusal) {
		t.Fatalf("expected the safe refusal from the attached judge, got %q", joined.String())
	}
	if !sawDone {
		t.Fatalf("expected a done event, got events %v", events)
	}
}
