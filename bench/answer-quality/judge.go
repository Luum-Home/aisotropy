package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/Luum-Home/aisotropy/internal/eval"
	"github.com/Luum-Home/aisotropy/internal/llm"
)

// hallucinationThreshold is the normalized ([0,1]) faithfulness score below
// which an answer that HAD grounding context is flagged as hallucinating —
// asserting facts the retrieved context does not license. It is deliberately
// only applied when context was present: with no grounding there is nothing to
// be unfaithful to, so no hallucination flag is raised (the correctness judge
// still grades those).
const hallucinationThreshold = 0.5

// scorer bundles the three LLM-as-a-judge graders an answer is scored on. All
// three run on the SAME provider-agnostic client (eval.JudgeClientFromEnv), so
// with no key they exercise the stub deterministically and with any provider
// key they judge for real. Correctness and Helpfulness are always scored;
// Faithfulness is scored only when the question carried grounding context.
type scorer struct {
	correctness  *eval.LLMJudge
	helpfulness  *eval.LLMJudge
	faithfulness *eval.LLMJudge
}

// newScorer builds the three judges around client (never nil — the caller
// resolves a real-or-stub client first).
func newScorer(client llm.Client) (*scorer, error) {
	c, err := eval.NewLLMJudge(client, Correctness, CorrectnessRubric, 0)
	if err != nil {
		return nil, fmt.Errorf("answer-quality: correctness judge: %w", err)
	}
	h, err := eval.NewLLMJudge(client, Helpfulness, HelpfulnessRubric, 0)
	if err != nil {
		return nil, fmt.Errorf("answer-quality: helpfulness judge: %w", err)
	}
	f, err := eval.NewLLMJudge(client, eval.Faithfulness, eval.FaithfulnessRubric, 0)
	if err != nil {
		return nil, fmt.Errorf("answer-quality: faithfulness judge: %w", err)
	}
	return &scorer{correctness: c, helpfulness: h, faithfulness: f}, nil
}

// answerScores holds one answer's normalized ([0,1]) judge scores plus the
// derived flags. faithfulnessScored is false when the question had no grounding
// context (the faithfulness axis is then N/A, not zero).
type answerScores struct {
	correctness        float64
	helpfulness        float64
	faithfulness       float64
	faithfulnessScored bool
	refused            bool
	hallucinated       bool
	llmCalls           int
}

// score grades one answer. It runs correctness and helpfulness always, and
// faithfulness only when q carried context. Refusal is the model-free heuristic
// (criteria.go); hallucination is derived from a low faithfulness score on a
// grounded question. An error is returned only for an operational judge failure
// (model unreachable, schema never satisfied) — a low-quality answer is a low
// score, never an error (the fail-closed contract of internal/eval).
func (s *scorer) score(ctx context.Context, q question, answer string) (answerScores, error) {
	sample := eval.EvalSample{
		Query:     q.Prompt,
		Context:   q.Context,
		Response:  answer,
		Reference: q.Ideal,
	}

	cs, err := s.correctness.Score(ctx, sample)
	if err != nil {
		return answerScores{}, fmt.Errorf("answer-quality: score correctness for %q: %w", q.ID, err)
	}
	hs, err := s.helpfulness.Score(ctx, sample)
	if err != nil {
		return answerScores{}, fmt.Errorf("answer-quality: score helpfulness for %q: %w", q.ID, err)
	}

	out := answerScores{
		correctness: cs.Value,
		helpfulness: hs.Value,
		refused:     looksLikeRefusal(answer),
		llmCalls:    2,
	}

	if strings.TrimSpace(q.Context) != "" {
		fs, ferr := s.faithfulness.Score(ctx, sample)
		if ferr != nil {
			return answerScores{}, fmt.Errorf("answer-quality: score faithfulness for %q: %w", q.ID, ferr)
		}
		out.faithfulness = fs.Value
		out.faithfulnessScored = true
		out.llmCalls++
		out.hallucinated = fs.Value < hallucinationThreshold
	}

	return out, nil
}

// llmCriteriaFor returns how many LLM judge calls one question incurs: 2
// (correctness + helpfulness) plus 1 when it carries grounding context
// (faithfulness). Used by the pre-run spend estimate.
func llmCriteriaFor(q question) int {
	if strings.TrimSpace(q.Context) != "" {
		return 3
	}
	return 2
}
