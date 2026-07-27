package eval_test

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/eval"
)

// TestGoldenGate_FrozenQABaseline is the quality-regression gate the readiness
// audit (docs/architecture/ai-product-readiness.md, top gap #1) asked for: a
// FROZEN golden set scored through the real internal/eval judge seam, asserting
// every case still scores exactly what it scored when the baseline was frozen.
//
// Why it is worth having on top of the per-package unit tests: those assert
// that each scoring primitive behaves; this asserts that the ASSEMBLED
// evaluation path — load a case, pick a judge, score a sample — keeps producing
// identical numbers as the code around it changes. A refactor that silently
// shifts a threshold, normalizes differently, or trims a response before
// judging shows up here as a numeric diff instead of as a quiet quality
// regression nobody notices until a real board run.
//
// It costs ZERO model calls and touches no network: both judges are
// eval.CodeJudge (deterministic Go funcs), so this runs on the keyless path and
// is safe to enforce on every push (scripts/pre-push-gate.sh stage 3).
//
// Updating the baseline is allowed but must be DELIBERATE: change
// testdata/golden/qa_golden.json's expected_score in a commit whose message
// says why the score legitimately moved. Never edit an expected_score merely to
// turn a red gate green — that converts the regression detector into a rubber
// stamp.
func TestGoldenGate_FrozenQABaseline(t *testing.T) {
	set := loadGoldenSet(t, filepath.Join("testdata", "golden", "qa_golden.json"))
	if len(set.Cases) == 0 {
		t.Fatalf("golden set %q has no cases — an empty gate silently passes everything", set.Name)
	}

	ctx := context.Background()
	for _, gc := range set.Cases {
		t.Run(gc.Name, func(t *testing.T) {
			judge, err := goldenJudge(gc)
			if err != nil {
				t.Fatalf("build judge for case %q: %v", gc.Name, err)
			}
			got, err := judge.Score(ctx, eval.EvalSample{
				Query:    gc.Query,
				Response: gc.Response,
			})
			if err != nil {
				// A judge error is an OPERATIONAL failure. A low-quality sample
				// must surface as a low Value, never as an error (the Judge
				// contract), so any error here is a real defect.
				t.Fatalf("case %q: judge returned an operational error: %v", gc.Name, err)
			}
			if math.Abs(got.Value-gc.ExpectedScore) > 1e-9 {
				t.Errorf("case %q: golden baseline DRIFTED — got %.6f, frozen baseline %.6f (reason: %q).\n"+
					"If this change is intentional, update expected_score in testdata/golden/qa_golden.json "+
					"in a commit that explains why; do NOT edit it just to make this pass.",
					gc.Name, got.Value, gc.ExpectedScore, got.Reasoning)
			}
		})
	}
}

// TestGoldenGate_BaselineIsDeterministic scores the whole set twice and demands
// byte-identical results. A gate that is itself non-deterministic cannot detect
// drift: it would flap, get muted, and stop protecting anything (the same
// premise scripts/arena-determinism-guard.sh enforces for the arena scorers).
func TestGoldenGate_BaselineIsDeterministic(t *testing.T) {
	set := loadGoldenSet(t, filepath.Join("testdata", "golden", "qa_golden.json"))
	ctx := context.Background()

	run := func() []float64 {
		out := make([]float64, 0, len(set.Cases))
		for _, gc := range set.Cases {
			judge, err := goldenJudge(gc)
			if err != nil {
				t.Fatalf("build judge for case %q: %v", gc.Name, err)
			}
			s, err := judge.Score(ctx, eval.EvalSample{Query: gc.Query, Response: gc.Response})
			if err != nil {
				t.Fatalf("case %q: %v", gc.Name, err)
			}
			out = append(out, s.Value)
		}
		return out
	}

	first, second := run(), run()
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("case %q is non-deterministic: %.6f then %.6f", set.Cases[i].Name, first[i], second[i])
		}
	}
}

// --- fixture plumbing -------------------------------------------------------

type goldenSet struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Cases       []goldenCase `json:"cases"`
}

type goldenCase struct {
	Name          string   `json:"name"`
	Judge         string   `json:"judge"`
	Query         string   `json:"query"`
	Response      string   `json:"response"`
	Required      []string `json:"required"`
	ExpectedScore float64  `json:"expected_score"`
}

func loadGoldenSet(t *testing.T, path string) goldenSet {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden set %s: %v", path, err)
	}
	var set goldenSet
	if err := json.Unmarshal(b, &set); err != nil {
		t.Fatalf("parse golden set %s: %v", path, err)
	}
	return set
}

// goldenJudge maps a fixture's declared judge kind to a deterministic
// eval.CodeJudge. Both kinds are pure functions of the sample — no model, no
// network — which is what makes this gate cheap enough to run on every push.
func goldenJudge(gc goldenCase) (eval.Judge, error) {
	switch gc.Judge {
	case "keyword":
		required := gc.Required
		return eval.NewCodeJudge(
			eval.Criterion{
				Name:        "keyword-presence",
				Description: "every required keyword appears in the response (case-insensitive)",
				Scale:       eval.UnitScale,
			},
			func(sample eval.EvalSample) (float64, string) {
				got := strings.ToLower(sample.Response)
				for _, want := range required {
					if !strings.Contains(got, strings.ToLower(want)) {
						return 0, "missing required keyword: " + want
					}
				}
				return 1, "all required keywords present"
			},
		)
	case "json_validity":
		return eval.NewCodeJudge(
			eval.Criterion{
				Name:        "json-validity",
				Description: "the response parses as a JSON object",
				Scale:       eval.UnitScale,
			},
			func(sample eval.EvalSample) (float64, string) {
				var v map[string]any
				if err := json.Unmarshal([]byte(sample.Response), &v); err != nil {
					return 0, "response is not a valid JSON object"
				}
				return 1, "response is a valid JSON object"
			},
		)
	default:
		return nil, &unknownJudgeError{kind: gc.Judge, caseName: gc.Name}
	}
}

type unknownJudgeError struct{ kind, caseName string }

func (e *unknownJudgeError) Error() string {
	return "golden case " + e.caseName + ": unknown judge kind " + e.kind +
		" (supported: keyword, json_validity) — an unrecognized kind must fail loudly, never score 0 silently"
}
