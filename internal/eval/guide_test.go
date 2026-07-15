package eval

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

func TestRunSuiteAggregatesCodeJudges(t *testing.T) {
	ctx := context.Background()
	cases, err := LoadEvalCasesFile("testdata/generation_golden.json")
	if err != nil {
		t.Fatalf("load golden: %v", err)
	}
	if len(cases) != 4 {
		t.Fatalf("golden set has %d cases, want 4", len(cases))
	}

	// Deterministic, model-free judge: exactly one of the four golden responses
	// is valid JSON, so the aggregate is pinned.
	res, err := RunSuite(ctx, cases, []Judge{NewJSONValidityJudge()}, 0.5)
	if err != nil {
		t.Fatalf("RunSuite: %v", err)
	}

	if len(res.Cases) != 4 {
		t.Fatalf("got %d case results, want 4", len(res.Cases))
	}
	for _, cr := range res.Cases {
		if len(cr.Scores) != 1 {
			t.Fatalf("case %q: got %d scores, want 1", cr.Case.Name, len(cr.Scores))
		}
	}

	agg, ok := res.ByCriterion["json_validity"]
	if !ok {
		t.Fatal("missing json_validity aggregate")
	}
	if agg.Count != 4 {
		t.Errorf("Count = %d, want 4", agg.Count)
	}
	if !almostEqual(agg.Mean, 0.25) {
		t.Errorf("Mean = %v, want 0.25 (1 of 4 valid JSON)", agg.Mean)
	}
	if agg.Pass != 1 || agg.Fail != 3 {
		t.Errorf("Pass/Fail = %d/%d, want 1/3", agg.Pass, agg.Fail)
	}
}

func TestRunSuiteMultipleJudgesStableColumns(t *testing.T) {
	ctx := context.Background()
	cases, err := LoadEvalCasesFile("testdata/generation_golden.json")
	if err != nil {
		t.Fatalf("load golden: %v", err)
	}

	// Mix a stub-backed LLM judge (relevance) with a code judge (json_validity):
	// the suite ties the guide's criteria to two different evaluation methods at
	// once, with no API key.
	llmJudge, err := NewLLMJudge(llm.NewStubClient(), Relevance, RelevanceRubric, 2)
	if err != nil {
		t.Fatalf("NewLLMJudge: %v", err)
	}
	judges := []Judge{llmJudge, NewJSONValidityJudge()}

	res, err := RunSuite(ctx, cases, judges, 0.5)
	if err != nil {
		t.Fatalf("RunSuite: %v", err)
	}

	if got := res.CriterionNames(); !reflect.DeepEqual(got, []string{"json_validity", "relevance"}) {
		t.Errorf("CriterionNames = %v, want [json_validity relevance]", got)
	}
	if res.ByCriterion["relevance"].Count != 4 {
		t.Errorf("relevance Count = %d, want 4", res.ByCriterion["relevance"].Count)
	}
	// Each case carries one score per judge, in judge order.
	for _, cr := range res.Cases {
		if len(cr.Scores) != 2 {
			t.Fatalf("case %q: got %d scores, want 2", cr.Case.Name, len(cr.Scores))
		}
		if cr.Scores[0].Criterion != "relevance" || cr.Scores[1].Criterion != "json_validity" {
			t.Errorf("case %q: score order = [%s %s], want [relevance json_validity]",
				cr.Case.Name, cr.Scores[0].Criterion, cr.Scores[1].Criterion)
		}
	}
}

// erroringJudge always fails, to prove RunSuite is fail-closed.
type erroringJudge struct{}

func (erroringJudge) Criterion() Criterion { return Criterion{Name: "boom"} }
func (erroringJudge) Score(context.Context, EvalSample) (Score, error) {
	return Score{}, errors.New("judge exploded")
}

func TestRunSuiteFailsClosedOnJudgeError(t *testing.T) {
	ctx := context.Background()
	cases := []EvalCase{{Name: "c1", Sample: EvalSample{Query: "q", Response: "r"}}}

	_, err := RunSuite(ctx, cases, []Judge{erroringJudge{}}, 0.5)
	if err == nil {
		t.Fatal("expected RunSuite to fail closed when a judge errors")
	}
}

func TestScaleContains(t *testing.T) {
	if !UnitScale.Contains(0) || !UnitScale.Contains(1) || !UnitScale.Contains(0.5) {
		t.Error("UnitScale should contain 0, 0.5, 1")
	}
	if UnitScale.Contains(-0.1) || UnitScale.Contains(1.1) {
		t.Error("UnitScale should reject out-of-range values")
	}
}

func TestLoadersFailClosed(t *testing.T) {
	if _, err := LoadEvalCasesFile("testdata/does-not-exist.json"); err == nil {
		t.Error("expected error for missing generation file")
	}
	if _, err := LoadRetrievalCasesFile("testdata/does-not-exist.json"); err == nil {
		t.Error("expected error for missing retrieval file")
	}
}
