package eval

import (
	"context"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

func TestLLMJudgeTypedVerdictWithStub(t *testing.T) {
	ctx := context.Background()
	// The stub client returns a deterministic schema-satisfying verdict, so the
	// whole LLM-as-judge path (prompt -> typed reflection -> normalized Score)
	// runs with no ANTHROPIC_API_KEY.
	client := llm.NewStubClient()

	judge, err := NewLLMJudge(client, Relevance, RelevanceRubric, 2)
	if err != nil {
		t.Fatalf("NewLLMJudge: %v", err)
	}
	if judge.Criterion().Name != "relevance" {
		t.Fatalf("Criterion().Name = %q, want relevance", judge.Criterion().Name)
	}

	score, err := judge.Score(ctx, EvalSample{
		Query:    "What is the capital of France?",
		Context:  "France's capital is Paris.",
		Response: "The capital of France is Paris.",
	})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if score.Criterion != "relevance" {
		t.Errorf("score.Criterion = %q, want relevance", score.Criterion)
	}
	// The stub emits value=0 for a numeric field; the point of the test is that
	// the typed verdict decoded, validated against the scale, and normalized —
	// not the specific number (a real model supplies that).
	if score.Value < 0 || score.Value > 1 {
		t.Errorf("score.Value = %v, want within [0,1]", score.Value)
	}
	if score.Reasoning == "" {
		t.Error("expected a non-empty reasoning string from the typed verdict")
	}
}

func TestNewLLMJudgeRejectsNilClient(t *testing.T) {
	if _, err := NewLLMJudge(nil, Relevance, nil, 0); err == nil {
		t.Error("expected error for nil client")
	}
	if _, err := NewLLMJudge(llm.NewStubClient(), Criterion{Name: ""}, nil, 0); err == nil {
		t.Error("expected error for unnamed criterion")
	}
}

func TestNormalizeAndScale(t *testing.T) {
	tests := []struct {
		name  string
		v     float64
		scale Scale
		want  float64
	}{
		{"unit passthrough", 0.5, UnitScale, 0.5},
		{"five point mid", 3, Scale{Min: 1, Max: 5}, 0.5},
		{"five point max", 5, Scale{Min: 1, Max: 5}, 1.0},
		{"five point min", 1, Scale{Min: 1, Max: 5}, 0.0},
		{"clamp over", 9, Scale{Min: 1, Max: 5}, 1.0},
		{"clamp under", -2, UnitScale, 0.0},
		{"degenerate scale clamps raw", 0.3, Scale{Min: 2, Max: 2}, 0.3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalize(tt.v, tt.scale); !almostEqual(got, tt.want) {
				t.Fatalf("normalize(%v,%v) = %v, want %v", tt.v, tt.scale, got, tt.want)
			}
		})
	}
}

func TestCodeJudgeJSONValidity(t *testing.T) {
	ctx := context.Background()
	judge := NewJSONValidityJudge()

	valid, err := judge.Score(ctx, EvalSample{Response: `{"capital":"Paris"}`})
	if err != nil {
		t.Fatalf("Score valid: %v", err)
	}
	if !almostEqual(valid.Value, 1.0) {
		t.Errorf("valid JSON score = %v, want 1.0", valid.Value)
	}
	if valid.Criterion != "json_validity" {
		t.Errorf("criterion = %q, want json_validity", valid.Criterion)
	}

	invalid, err := judge.Score(ctx, EvalSample{Response: "not json at all"})
	if err != nil {
		t.Fatalf("Score invalid: %v", err)
	}
	if !almostEqual(invalid.Value, 0.0) {
		t.Errorf("invalid JSON score = %v, want 0.0", invalid.Value)
	}
}

func TestCodeJudgeKeyword(t *testing.T) {
	ctx := context.Background()
	crit := Criterion{Name: "has_disclaimer", Description: "must mention the disclaimer", Scale: UnitScale}
	judge := NewKeywordJudge(crit, "not financial advice")

	present, _ := judge.Score(ctx, EvalSample{Response: "Buy low. (This is Not Financial Advice.)"})
	if !almostEqual(present.Value, 1.0) {
		t.Errorf("present keyword score = %v, want 1.0", present.Value)
	}

	missing, _ := judge.Score(ctx, EvalSample{Response: "Buy low, sell high."})
	if !almostEqual(missing.Value, 0.0) {
		t.Errorf("missing keyword score = %v, want 0.0", missing.Value)
	}
}

func TestNewCodeJudgeRejectsNilFunc(t *testing.T) {
	if _, err := NewCodeJudge(JSONValidityCriterion, nil); err == nil {
		t.Error("expected error for nil scoring func")
	}
}
