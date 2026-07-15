package api

// eval_output_check.go is the adapter that lets an LLM-as-a-judge (internal/eval)
// attach to the output guardrails WITHOUT internal/guardrails ever importing
// internal/eval. It is the whole point of guardrails.OutputCheck being an
// interface: the guardrails package defines the seam (Name + Check), and this
// api-layer adapter wires a concrete eval.Judge into it. The import direction is
// api -> {eval, guardrails}; neither of those two depends on the other, so the
// evaluation tier and the guardrails tier stay decoupled (see
// internal/guardrails/output.go's OutputCheck doc and ADR-0002's anti-magic
// seam rationale).

import (
	"context"
	"fmt"

	"github.com/Luum-Home/aisotropy/internal/eval"
	"github.com/Luum-Home/aisotropy/internal/guardrails"
)

// EvalOutputCheck adapts an eval.Judge (typically an eval.LLMJudge over the
// Safety criterion) to the guardrails.OutputCheck seam. Check embeds the model
// output as the EvalSample.Response, scores it, and reports the output
// Suspicious when the normalized score falls below Threshold — for the Safety
// criterion, a low score means unsafe (1.0 = safe, 0.0 = unsafe), so a
// below-threshold score is exactly a toxic/unsafe response.
//
// It carries no state beyond its judge and thresholds and performs no I/O of its
// own (the judge owns the model call), so it is safe for concurrent use.
type EvalOutputCheck struct {
	// judge scores one criterion (its Criterion().Scale is normalized to [0,1]
	// by eval.Score, so Threshold is always a unit-interval comparison).
	judge eval.Judge
	// threshold is the safety floor in [0,1]: a normalized score STRICTLY below
	// it is Suspicious. For eval.Safety, below-threshold == unsafe.
	threshold float64
	// failClosed decides how a judge OPERATIONAL failure (model unreachable,
	// schema never satisfied) is treated. The judge never returns an error for a
	// merely-low-quality sample (that is a low score), so an error here is always
	// infrastructural. When false (BLOCK_OUTPUT off) a failure is treated as
	// NOT-suspicious (fail-open: annotate only — a flaky judge must not silently
	// start refusing every response). When true (BLOCK_OUTPUT on) a failure is
	// treated as Suspicious (fail-closed: with hard blocking on, an un-vetted
	// response must not slip through). This makes the judge's error posture
	// track the operator's chosen blocking posture rather than picking one
	// globally.
	failClosed bool
}

// NewEvalOutputCheck builds an EvalOutputCheck. A nil judge is rejected
// (fail-closed at construction: an adapter with no judge could only ever be a
// no-op or a panic). threshold is clamped to [0,1]; a value <= 0 would make the
// check inert (nothing scores below 0) and a value > 1 would flag everything, so
// callers should pass a real safety floor (e.g. 0.5).
func NewEvalOutputCheck(judge eval.Judge, threshold float64, failClosed bool) (*EvalOutputCheck, error) {
	if judge == nil {
		return nil, fmt.Errorf("api: EvalOutputCheck requires a non-nil eval.Judge")
	}
	if threshold < 0 {
		threshold = 0
	}
	if threshold > 1 {
		threshold = 1
	}
	return &EvalOutputCheck{judge: judge, threshold: threshold, failClosed: failClosed}, nil
}

// Name identifies the check in aggregated guardrail results, namespaced by the
// judged criterion (e.g. "eval:safety").
func (c *EvalOutputCheck) Name() string {
	return "eval:" + c.judge.Criterion().Name
}

// Check scores the model output with the wrapped judge and returns an advisory
// Verdict. The user input becomes the sample Query and the system prompt becomes
// the sample Context, so a faithfulness-style judge (if wired) has its grounding;
// a standalone Safety judge simply ignores them. A judge operational error is
// resolved per failClosed (see the field doc): fail-open -> not Suspicious,
// fail-closed -> Suspicious. It never panics and never propagates the error,
// honoring the OutputCheck contract.
func (c *EvalOutputCheck) Check(ctx context.Context, in guardrails.OutputCheckInput) guardrails.Verdict {
	sample := eval.EvalSample{
		Query:    in.Input,
		Context:  in.SystemPrompt,
		Response: in.Output,
	}
	score, err := c.judge.Score(ctx, sample)
	if err != nil {
		// Operational failure: honor the operator's blocking posture. Score is
		// set to the confidence that there IS a problem (1.0 when we fail closed,
		// 0.0 when we fail open) so it reads consistently with the success path.
		signal := c.Name() + ":error(fail-open)"
		conf := 0.0
		if c.failClosed {
			signal = c.Name() + ":error(fail-closed)"
			conf = 1.0
		}
		return guardrails.Verdict{Suspicious: c.failClosed, Signals: []string{signal}, Score: conf}
	}

	suspicious := score.Value < c.threshold
	verdict := guardrails.Verdict{
		Suspicious: suspicious,
		// Verdict.Score is 0..1 confidence that there is a PROBLEM (higher = worse),
		// which is the INVERSE of a safety score (higher = safer): a safety of 0.1
		// is a 0.9 problem-confidence.
		Score: 1 - score.Value,
	}
	if suspicious {
		verdict.Signals = []string{c.Name() + ":below_threshold"}
	}
	return verdict
}
