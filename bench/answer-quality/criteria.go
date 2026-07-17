package main

import (
	"strings"

	"github.com/Luum-Home/aisotropy/internal/eval"
)

// The answer-quality judges grade three LLM-scored axes plus two model-free
// heuristics. Correctness and Helpfulness are defined HERE on a 1..5 scale (the
// familiar human rubric range the task asks for); Faithfulness reuses the
// book's headline criterion from internal/eval (UnitScale). Every score is
// normalized to [0,1] by the judge and de-normalized back to 1..5 for display
// (see report.go's toFive), so the three axes present uniformly.

// answerScale is the 1..5 rubric range for the human-facing quality axes.
var answerScale = eval.Scale{Min: 1, Max: 5}

// Correctness grades factual correctness. When a reference (ideal) answer is
// present the judge compares substance (not wording) against it; when absent it
// judges correctness against the query and any provided context. This is the
// axis an operator most wants a number for: "did it get the answer right?"
var Correctness = eval.Criterion{
	Name:        "correctness",
	Description: "Is the response factually correct? If a Reference (ideal) answer is provided, score how well the response matches it in SUBSTANCE (not exact wording): a correct answer phrased differently still scores high. If no reference is provided, judge correctness against the query and any provided context. Score 5 for a fully correct answer, 3 for partially correct or incomplete, 1 for incorrect or fabricated.",
	Scale:       answerScale,
}

// CorrectnessRubric anchors the correctness scale.
var CorrectnessRubric = eval.Rubric{
	{Score: 5, Illustration: "fully correct; matches the reference answer in substance"},
	{Score: 3, Illustration: "partially correct or missing key facts"},
	{Score: 1, Illustration: "incorrect, contradicts the reference, or fabricated"},
}

// Helpfulness grades whether the answer actually serves the user: complete,
// clear, and appropriately concise — independent of raw factual correctness.
var Helpfulness = eval.Criterion{
	Name:        "helpfulness",
	Description: "Is the response helpful to the user — complete, clear, actionable, and appropriately concise for what was asked? Score 5 for a genuinely helpful answer, 3 for partially helpful or padded, 1 for unhelpful, evasive, or empty.",
	Scale:       answerScale,
}

// HelpfulnessRubric anchors the helpfulness scale.
var HelpfulnessRubric = eval.Rubric{
	{Score: 5, Illustration: "directly and completely helps the user"},
	{Score: 3, Illustration: "partially helpful, vague, or padded"},
	{Score: 1, Illustration: "unhelpful, evasive, or empty"},
}

// refusalMarkers are lowercase phrases that signal the model declined to answer
// substantively. The refusal rate is a first-class launch-readiness metric: a
// high refusal rate on legitimate domain questions means the agent is unhelpful
// even when it is "safe". This heuristic is deliberately model-free (no cost)
// and errs toward recall; it is a signal to inspect, not a verdict. It is NOT a
// safety judge — a genuine, appropriate refusal of an unsafe request still trips
// it, which is why the report presents it alongside, not instead of, the graded
// axes.
var refusalMarkers = []string{
	"i can't help", "i cannot help", "i can't assist", "i cannot assist",
	"i'm not able to", "i am not able to", "i'm unable to", "i am unable to",
	"i can't provide", "i cannot provide", "i can't answer", "i cannot answer",
	"i don't have enough information", "i do not have enough information",
	"i'm sorry, but i can", "i am sorry, but i can",
	"as an ai", "i cannot fulfill", "i can't fulfill",
}

// looksLikeRefusal reports whether answer contains a refusal marker. Empty or
// whitespace-only answers also count as a refusal (the model produced nothing
// usable).
func looksLikeRefusal(answer string) bool {
	a := strings.ToLower(strings.TrimSpace(answer))
	if a == "" {
		return true
	}
	for _, m := range refusalMarkers {
		if strings.Contains(a, m) {
			return true
		}
	}
	return false
}
