package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// criterionFailingClient wraps a real llm.Client (normally the deterministic
// stub) and forces a hard StreamChat failure only for requests whose system
// prompt names one specific criterion (LLMJudge.systemPrompt always embeds
// "Criterion: <name>"), delegating to inner for everything else. This is
// deterministic regardless of how many attempts internal/llm's reflection
// loop makes for any ONE judge (it can retry a judge whose first attempt
// fails validation, so counting raw StreamChat calls is not reliable — an
// earlier version of this test tried that and was flaky) — targeting by
// criterion name lets a test fail exactly one of the scorer's three judges
// (correctness, helpfulness, faithfulness) while the other two succeed for
// real through the stub's schema-satisfying response.
type criterionFailingClient struct {
	inner         llm.Client
	failCriterion string
}

func (c criterionFailingClient) Name() string { return c.inner.Name() }

func (c criterionFailingClient) StreamChat(ctx context.Context, system, prompt string) (<-chan llm.Token, error) {
	if strings.Contains(system, "Criterion: "+c.failCriterion) {
		return nil, fmt.Errorf("criterionFailingClient: forced failure for criterion %q", c.failCriterion)
	}
	return c.inner.StreamChat(ctx, system, prompt)
}

func TestLooksLikeRefusal(t *testing.T) {
	cases := map[string]bool{
		"":                                true,
		"   ":                             true,
		"The capital of France is Paris.": false,
		"I can't help with that request.": true,
		"I'm unable to answer this.":      true,
		"I do not have enough information to answer.": true,
		"Here is a helpful, complete answer.":         false,
	}
	for answer, want := range cases {
		if got := looksLikeRefusal(answer); got != want {
			t.Errorf("looksLikeRefusal(%q) = %v; want %v", answer, got, want)
		}
	}
}

// TestScorer_StubEndToEnd exercises the three-judge scorer over the stub: it
// returns without error, runs faithfulness ONLY when the question carried
// context (3 vs 2 LLM calls), and marks the derived flags. Scores themselves are
// stub artifacts; the test asserts the WIRING, not quality.
func TestScorer_StubEndToEnd(t *testing.T) {
	sc, err := newScorer(llm.NewStubClient())
	if err != nil {
		t.Fatalf("newScorer: %v", err)
	}
	ctx := context.Background()

	t.Run("with-context-scores-faithfulness", func(t *testing.T) {
		q := question{ID: "g", Prompt: "What is the capital of France?", Ideal: "Paris", Context: "The capital of France is Paris."}
		got, err := sc.score(ctx, q, "The capital of France is Paris.")
		if err != nil {
			t.Fatalf("score: %v", err)
		}
		if !got.faithfulnessScored {
			t.Error("faithfulness should be scored when context is present")
		}
		if got.llmCalls != 3 {
			t.Errorf("llmCalls = %d; want 3 (correctness + helpfulness + faithfulness)", got.llmCalls)
		}
	})

	t.Run("without-context-skips-faithfulness", func(t *testing.T) {
		q := question{ID: "g2", Prompt: "Explain gravity briefly."}
		got, err := sc.score(ctx, q, "Gravity is the attraction between masses.")
		if err != nil {
			t.Fatalf("score: %v", err)
		}
		if got.faithfulnessScored {
			t.Error("faithfulness should be skipped with no context")
		}
		if got.llmCalls != 2 {
			t.Errorf("llmCalls = %d; want 2 (correctness + helpfulness)", got.llmCalls)
		}
	})

	t.Run("refusal-flagged", func(t *testing.T) {
		q := question{ID: "g3", Prompt: "Anything?"}
		got, err := sc.score(ctx, q, "I can't help with that.")
		if err != nil {
			t.Fatalf("score: %v", err)
		}
		if !got.refused {
			t.Error("a refusal answer should set refused=true")
		}
	})
}

// TestNewScorerRejectsNilClient exercises newScorer's error-propagation
// branch for eval.NewLLMJudge failing on the correctness judge (a nil client
// fails LLMJudge construction fail-closed, per eval.NewLLMJudge's doc
// comment). The helpfulness/faithfulness construction-error branches are not
// independently reachable: newScorer passes the SAME client to all three
// NewLLMJudge calls and both later criteria have fixed, always-valid names,
// so their own construction can only fail for the same reason correctness
// already did — meaning newScorer returns on the very first one.
func TestNewScorerRejectsNilClient(t *testing.T) {
	if _, err := newScorer(nil); err == nil {
		t.Fatal("expected an error constructing judges around a nil client")
	}
}

// TestScore_CorrectnessJudgeErrorPropagates forces the correctness judge to
// fail and asserts score() returns the wrapped error without ever reaching
// helpfulness/faithfulness.
func TestScore_CorrectnessJudgeErrorPropagates(t *testing.T) {
	fc := criterionFailingClient{inner: llm.NewStubClient(), failCriterion: "correctness"}
	sc, err := newScorer(fc)
	if err != nil {
		t.Fatalf("newScorer: %v", err)
	}

	_, err = sc.score(context.Background(), question{ID: "q", Prompt: "p"}, "answer")
	if err == nil {
		t.Fatal("expected an error when the correctness judge call fails")
	}
	if !strings.Contains(err.Error(), "score correctness") {
		t.Fatalf("err = %v, want it to mention \"score correctness\"", err)
	}
}

// TestScore_HelpfulnessJudgeErrorPropagates lets correctness succeed for real
// (via the stub) and forces helpfulness to fail.
func TestScore_HelpfulnessJudgeErrorPropagates(t *testing.T) {
	fc := criterionFailingClient{inner: llm.NewStubClient(), failCriterion: "helpfulness"}
	sc, err := newScorer(fc)
	if err != nil {
		t.Fatalf("newScorer: %v", err)
	}

	_, err = sc.score(context.Background(), question{ID: "q", Prompt: "p"}, "answer")
	if err == nil {
		t.Fatal("expected an error when the helpfulness judge call fails")
	}
	if !strings.Contains(err.Error(), "score helpfulness") {
		t.Fatalf("err = %v, want it to mention \"score helpfulness\"", err)
	}
}

// TestScore_FaithfulnessJudgeErrorPropagates lets correctness and
// helpfulness succeed for real and forces faithfulness (which only runs when
// the question carries grounding context) to fail.
func TestScore_FaithfulnessJudgeErrorPropagates(t *testing.T) {
	fc := criterionFailingClient{inner: llm.NewStubClient(), failCriterion: "faithfulness"}
	sc, err := newScorer(fc)
	if err != nil {
		t.Fatalf("newScorer: %v", err)
	}

	q := question{ID: "q", Prompt: "p", Context: "grounding context so faithfulness runs"}
	_, err = sc.score(context.Background(), q, "answer")
	if err == nil {
		t.Fatal("expected an error when the faithfulness judge call fails")
	}
	if !strings.Contains(err.Error(), "score faithfulness") {
		t.Fatalf("err = %v, want it to mention \"score faithfulness\"", err)
	}
}

func TestLLMCriteriaFor(t *testing.T) {
	if n := llmCriteriaFor(question{Prompt: "q"}); n != 2 {
		t.Errorf("no-context criteria = %d; want 2", n)
	}
	if n := llmCriteriaFor(question{Prompt: "q", Context: "grounding"}); n != 3 {
		t.Errorf("with-context criteria = %d; want 3", n)
	}
}
