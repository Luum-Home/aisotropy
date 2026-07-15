package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// Score is one judge's verdict on one sample for one criterion (step 3
// output). Value is ALWAYS normalized to [0,1] — a judge that scores on a
// wider Scale normalizes before returning — so aggregates and thresholds are
// comparable across criteria and across methods. Reasoning is a short
// human-readable justification (why this score), which an LLMJudge fills from
// the model and a CodeJudge fills from its check.
type Score struct {
	Criterion string
	Value     float64
	Reasoning string
}

// Judge scores a sample against one criterion. It is the seam that unifies the
// three evaluation methods: LLMJudge (a model), CodeJudge (a deterministic
// func), and — by carrying labeled EvalCase.Expected — a human gold set. A
// Judge is bound to exactly one Criterion (Criterion()) so RunSuite can group
// aggregates and present the right rubric.
type Judge interface {
	// Criterion returns the single criterion this judge evaluates.
	Criterion() Criterion
	// Score evaluates one sample. It returns an error only for an operational
	// failure (e.g. the model was unreachable) — a legitimately low quality
	// sample is a low Value, never an error, so the suite stays fail-closed on
	// infrastructure but never on content.
	Score(ctx context.Context, sample EvalSample) (Score, error)
}

// --- LLM-as-a-judge -------------------------------------------------------

// LLMJudge scores an open-ended criterion by asking an llm.Client for a TYPED
// verdict. It reuses aisotropy's struct-as-contract reflection loop
// (llm.GollemReflect): the model is instructed to emit JSON matching the
// verdict struct, and Gollem's Agent retries with the exact schema error until
// it validates — so a judge run yields a parsed, range-checked struct, not
// free text the caller must hope parses (the same "struct is the contract"
// thesis the chat handler applies to its own output, internal/api/chat.go).
//
// It is the right method for criteria a code check cannot capture (relevance,
// faithfulness, nuanced safety). Tests drive it with llm.NewStubClient, which
// returns a deterministic schema-satisfying object, so LLMJudge is exercised
// end-to-end with no API key.
type LLMJudge struct {
	client     llm.Client
	criterion  Criterion
	rubric     Rubric
	maxRetries int
}

// defaultJudgeRetries bounds the reflection loop when a caller does not set
// one. Small: a judge that cannot produce a valid verdict in a few tries is an
// operational failure worth surfacing, not something to grind on.
const defaultJudgeRetries = 3

// NewLLMJudge builds an LLMJudge for a criterion, with the rubric embedded in
// every prompt for calibration. A nil client is rejected (fail-closed: a judge
// with no model can only ever error, so construction is the honest place to
// stop). maxRetries <= 0 uses defaultJudgeRetries.
func NewLLMJudge(client llm.Client, criterion Criterion, rubric Rubric, maxRetries int) (*LLMJudge, error) {
	if client == nil {
		return nil, fmt.Errorf("eval: LLMJudge for %q requires a non-nil llm.Client", criterion.Name)
	}
	if strings.TrimSpace(criterion.Name) == "" {
		return nil, fmt.Errorf("eval: LLMJudge requires a named criterion")
	}
	if maxRetries <= 0 {
		maxRetries = defaultJudgeRetries
	}
	return &LLMJudge{client: client, criterion: criterion, rubric: rubric, maxRetries: maxRetries}, nil
}

// Criterion returns the criterion this judge scores.
func (j *LLMJudge) Criterion() Criterion { return j.criterion }

// verdict is the typed contract the judge model must satisfy — the Go struct
// IS the schema llm.GollemReflect shows the model (via reflection) and
// validates the reply against. Value is the raw score on the criterion's
// Scale; Reasoning is the justification.
type verdict struct {
	Value     float64 `json:"value"`
	Reasoning string  `json:"reasoning"`
}

// Score prompts the model for a typed verdict, validates the raw Value against
// the criterion's Scale via the reflection loop's business-validation hook,
// and returns a Score with Value normalized to [0,1]. An exhausted/failed
// reflection loop (schema never satisfied, model unreachable) is returned as
// an error — the fail-closed contract: a judge that cannot produce a valid
// verdict must not silently emit 0.
func (j *LLMJudge) Score(ctx context.Context, sample EvalSample) (Score, error) {
	system := j.systemPrompt()
	prompt := j.userPrompt(sample)

	res, err := llm.GollemReflect[verdict](ctx, j.client, system, prompt, j.validate, j.maxRetries)
	if err != nil {
		return Score{}, fmt.Errorf("eval: LLMJudge %q reflection failed: %w", j.criterion.Name, err)
	}

	return Score{
		Criterion: j.criterion.Name,
		Value:     normalize(res.Output.Value, j.criterion.Scale),
		Reasoning: res.Output.Reasoning,
	}, nil
}

// validate is the business-validation step llm.GollemReflect calls on the
// already-decoded verdict: an out-of-Scale Value returns feedback asking the
// model to correct it (non-empty => retry), an in-range value accepts (""). It
// never returns a Go error — a bad number is a correction request, not a fatal.
func (j *LLMJudge) validate(_ context.Context, v verdict) (string, error) {
	if !j.criterion.Scale.Contains(v.Value) {
		return fmt.Sprintf("the \"value\" field must be within the scale [%.2f, %.2f]; you returned %v",
			j.criterion.Scale.Min, j.criterion.Scale.Max, v.Value), nil
	}
	return "", nil
}

// systemPrompt frames the model as an impartial grader for this one criterion,
// embedding the criterion description, the scale, and the rubric.
func (j *LLMJudge) systemPrompt() string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are an impartial evaluation judge. Score exactly one criterion.\n")
	fmt.Fprintf(&b, "Criterion: %s\n", j.criterion.Name)
	fmt.Fprintf(&b, "Definition: %s\n", j.criterion.Description)
	fmt.Fprintf(&b, "Scale: a number from %.2f (worst) to %.2f (best).\n", j.criterion.Scale.Min, j.criterion.Scale.Max)
	if rendered := j.rubric.render(); rendered != "" {
		fmt.Fprintf(&b, "Rubric:\n%s\n", rendered)
	}
	b.WriteString("Return your score in the \"value\" field and a one-sentence justification in \"reasoning\".")
	return b.String()
}

// userPrompt lays out the sample under evaluation. Context is included only
// when present (a standalone criterion may not need it).
func (j *LLMJudge) userPrompt(sample EvalSample) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Query:\n%s\n\n", sample.Query)
	if strings.TrimSpace(sample.Context) != "" {
		fmt.Fprintf(&b, "Context provided to the model:\n%s\n\n", sample.Context)
	}
	fmt.Fprintf(&b, "Response to evaluate:\n%s", sample.Response)
	return b.String()
}

// normalize maps a raw score on scale to [0,1]. A degenerate scale (Max<=Min)
// or an out-of-range value is clamped, so a downstream aggregate always sees a
// unit-interval number.
func normalize(v float64, scale Scale) float64 {
	if scale.Max <= scale.Min {
		return clamp01(v)
	}
	return clamp01((v - scale.Min) / (scale.Max - scale.Min))
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}

// --- Code / exact-function judge ------------------------------------------

// CodeJudge scores a criterion with a deterministic Go func — no model, no
// network, no cost. It is the right method for a mechanical criterion (valid
// JSON, contains a required token, matches a format): exact and free. The func
// returns a normalized value in [0,1] and a short reason; CodeJudge stamps the
// criterion name onto the Score.
type CodeJudge struct {
	criterion Criterion
	fn        func(sample EvalSample) (value float64, reasoning string)
}

// NewCodeJudge wraps a scoring func as a Judge. A nil fn is rejected.
func NewCodeJudge(criterion Criterion, fn func(sample EvalSample) (float64, string)) (*CodeJudge, error) {
	if fn == nil {
		return nil, fmt.Errorf("eval: CodeJudge for %q requires a non-nil scoring func", criterion.Name)
	}
	return &CodeJudge{criterion: criterion, fn: fn}, nil
}

// Criterion returns the criterion this judge scores.
func (j *CodeJudge) Criterion() Criterion { return j.criterion }

// Score runs the deterministic func and clamps its value into [0,1]. It never
// errors: a code check is total over its input.
func (j *CodeJudge) Score(_ context.Context, sample EvalSample) (Score, error) {
	v, reason := j.fn(sample)
	return Score{Criterion: j.criterion.Name, Value: clamp01(v), Reasoning: reason}, nil
}

// JSONValidityCriterion is the built-in criterion the JSON-validity code judge
// scores: does the response parse as a JSON value at all? A common structural
// gate before any semantic judging of a structured-output agent.
var JSONValidityCriterion = Criterion{
	Name:        "json_validity",
	Description: "Does the response parse as syntactically valid JSON?",
	Scale:       UnitScale,
}

// NewJSONValidityJudge returns a CodeJudge that scores 1.0 when the response is
// valid JSON and 0.0 otherwise — a deterministic, model-free structural check.
func NewJSONValidityJudge() *CodeJudge {
	j, _ := NewCodeJudge(JSONValidityCriterion, func(s EvalSample) (float64, string) {
		if json.Valid([]byte(s.Response)) {
			return 1, "response is valid JSON"
		}
		return 0, "response is not valid JSON"
	})
	return j
}

// NewKeywordJudge returns a CodeJudge for `criterion` that scores 1.0 when the
// response contains all of `required` (case-insensitive substring match) and
// 0.0 when any is missing — the mechanical "must mention X" check, e.g. a
// required disclaimer or citation marker. Empty `required` always scores 1.0.
func NewKeywordJudge(criterion Criterion, required ...string) *CodeJudge {
	j, _ := NewCodeJudge(criterion, func(s EvalSample) (float64, string) {
		resp := strings.ToLower(s.Response)
		for _, kw := range required {
			if !strings.Contains(resp, strings.ToLower(kw)) {
				return 0, fmt.Sprintf("response is missing required keyword %q", kw)
			}
		}
		return 1, "response contains all required keywords"
	})
	return j
}
