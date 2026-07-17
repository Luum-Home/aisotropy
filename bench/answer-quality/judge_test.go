package main

import (
	"context"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

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

func TestLLMCriteriaFor(t *testing.T) {
	if n := llmCriteriaFor(question{Prompt: "q"}); n != 2 {
		t.Errorf("no-context criteria = %d; want 2", n)
	}
	if n := llmCriteriaFor(question{Prompt: "q", Context: "grounding"}); n != 3 {
		t.Errorf("with-context criteria = %d; want 3", n)
	}
}
