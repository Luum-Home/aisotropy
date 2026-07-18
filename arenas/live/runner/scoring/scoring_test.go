package scoring

import (
	"testing"
)

// ---- ScoreQA ----------------------------------------------------------------

func TestScoreQA_ExactMatch(t *testing.T) {
	sc := ScoreQA("Paris is the capital of France.", "Paris is the capital of France.")
	if !sc.Pass || sc.Value != 1 {
		t.Fatalf("want pass=true value=1, got %+v", sc)
	}
}

func TestScoreQA_ExactMatch_CaseAndPunctuationInsensitive(t *testing.T) {
	sc := ScoreQA("PARIS is the CAPITAL of France!!", "Paris is the capital of France.")
	if !sc.Pass || sc.Value != 1 {
		t.Fatalf("want pass=true value=1, got %+v", sc)
	}
}

func TestScoreQA_ContainmentPass(t *testing.T) {
	// Reply extends the reference with extra correct detail — should still pass.
	sc := ScoreQA("The chemical symbol for gold is Au, atomic number 79.", "The chemical symbol for gold is Au.")
	if !sc.Pass {
		t.Fatalf("want pass=true (containment), got %+v", sc)
	}
}

func TestScoreQA_Mismatch(t *testing.T) {
	sc := ScoreQA("The capital of Germany is Berlin.", "Paris is the capital of France.")
	if sc.Pass {
		t.Fatalf("want pass=false for unrelated answer, got %+v", sc)
	}
}

func TestScoreQA_EmptyReference(t *testing.T) {
	sc := ScoreQA("anything", "")
	if sc.Pass {
		t.Fatalf("want pass=false with no reference, got %+v", sc)
	}
}

// ---- ScoreMemory --------------------------------------------------------

func TestScoreMemory_ExactRecall(t *testing.T) {
	sc := ScoreMemory("Your favorite color is teal.", "Your favorite color is teal.")
	if !sc.Pass || sc.Value != 1 {
		t.Fatalf("want pass=true value=1, got %+v", sc)
	}
}

func TestScoreMemory_ForgottenFact(t *testing.T) {
	sc := ScoreMemory("I don't recall your favorite color.", "Your favorite color is teal.")
	if sc.Pass {
		t.Fatalf("want pass=false for a forgotten fact, got %+v", sc)
	}
}

// ---- ScoreRAG -----------------------------------------------------------

func TestScoreRAG_GroundedHit(t *testing.T) {
	sc := ScoreRAG("08-blue-guarantee.md", []string{"08-blue-guarantee.md"}, "More than 12 minutes late.", false)
	if !sc.Pass || sc.Value != 1 {
		t.Fatalf("want hit@1 pass, got %+v", sc)
	}
}

func TestScoreRAG_GroundedMiss(t *testing.T) {
	sc := ScoreRAG("99-unrelated.md", []string{"08-blue-guarantee.md"}, "Some answer.", false)
	if sc.Pass {
		t.Fatalf("want miss@1 fail, got %+v", sc)
	}
}

func TestScoreRAG_GroundedCaseInsensitiveDocID(t *testing.T) {
	sc := ScoreRAG("08-BLUE-GUARANTEE.MD", []string{"08-blue-guarantee.md"}, "answer", false)
	if !sc.Pass {
		t.Fatalf("want case-insensitive doc id match to pass, got %+v", sc)
	}
}

func TestScoreRAG_TrapRefused(t *testing.T) {
	sc := ScoreRAG("", nil, "I don't know, that information is not in the source documents.", true)
	if !sc.Pass {
		t.Fatalf("want trap refusal to pass, got %+v", sc)
	}
}

func TestScoreRAG_TrapInvented(t *testing.T) {
	sc := ScoreRAG("", nil, "The answer is definitely 42 kilometers.", true)
	if sc.Pass {
		t.Fatalf("want trap invented-answer to fail, got %+v", sc)
	}
}

// ---- ScoreTools -----------------------------------------------------------

func TestScoreTools_ExactMatch(t *testing.T) {
	calls := []ToolCall{{Name: "calculate", Arguments: map[string]any{"expression": "47 * 89"}, Executed: true}}
	sc := ScoreTools(calls, "calculate", map[string]any{"expression": "47 * 89"})
	if !sc.Pass || sc.Value != 1 {
		t.Fatalf("want pass=true value=1, got %+v", sc)
	}
}

func TestScoreTools_NumericNormalization(t *testing.T) {
	calls := []ToolCall{{Name: "get_weather", Arguments: map[string]any{"location": "Tokyo", "days": "3"}, Executed: true}}
	sc := ScoreTools(calls, "get_weather", map[string]any{"location": "Tokyo", "days": 3})
	if !sc.Pass {
		t.Fatalf("want numeric string/number equivalence to pass, got %+v", sc)
	}
}

func TestScoreTools_WrongTool(t *testing.T) {
	calls := []ToolCall{{Name: "web_search", Arguments: map[string]any{"query": "x"}, Executed: true}}
	sc := ScoreTools(calls, "calculate", map[string]any{"expression": "1+1"})
	if sc.Pass {
		t.Fatalf("want pass=false, expected tool never called, got %+v", sc)
	}
}

func TestScoreTools_CalledButNotExecuted(t *testing.T) {
	calls := []ToolCall{{Name: "calculate", Arguments: map[string]any{"expression": "1+1"}, Executed: false}}
	sc := ScoreTools(calls, "calculate", map[string]any{"expression": "1+1"})
	if sc.Pass {
		t.Fatalf("want pass=false, tool call did not execute, got %+v", sc)
	}
}

func TestScoreTools_ArgMismatch(t *testing.T) {
	calls := []ToolCall{{Name: "calculate", Arguments: map[string]any{"expression": "1+2"}, Executed: true}}
	sc := ScoreTools(calls, "calculate", map[string]any{"expression": "1+1"})
	if sc.Pass {
		t.Fatalf("want pass=false, argument mismatch, got %+v", sc)
	}
}

func TestScoreTools_NoExpectedTool(t *testing.T) {
	sc := ScoreTools(nil, "", nil)
	if sc.Pass {
		t.Fatalf("want pass=false with no expected_tool, got %+v", sc)
	}
}

// ---- ScoreToolsNumeric ------------------------------------------------------

func TestScoreToolsNumeric_ExecutedAndCorrect(t *testing.T) {
	calls := []ToolCall{{Name: "calculate", Arguments: map[string]any{"expression": "47 * 89"}, Executed: true}}
	sc := ScoreToolsNumeric(calls, "The answer is 4,183.", "4183")
	if !sc.Pass || sc.Value != 1 {
		t.Fatalf("want pass=true value=1, got %+v", sc)
	}
}

func TestScoreToolsNumeric_ExecutedButWrongNumber(t *testing.T) {
	calls := []ToolCall{{Name: "calculate", Arguments: nil, Executed: true}}
	sc := ScoreToolsNumeric(calls, "The answer is 9999.", "4183")
	if sc.Pass {
		t.Fatalf("want pass=false, got %+v", sc)
	}
}

func TestScoreToolsNumeric_NoToolExecuted(t *testing.T) {
	sc := ScoreToolsNumeric(nil, "The answer is 4183.", "4183")
	if sc.Pass {
		t.Fatalf("want pass=false, no tool executed, got %+v", sc)
	}
}

func TestScoreToolsNumeric_NoExpectedNumber(t *testing.T) {
	sc := ScoreToolsNumeric(nil, "anything", "")
	if sc.Pass {
		t.Fatalf("want pass=false with no expected number, got %+v", sc)
	}
}

// ---- ToolExecuted -----------------------------------------------------------

func TestToolExecuted(t *testing.T) {
	if ToolExecuted(nil) {
		t.Fatalf("want false for nil calls")
	}
	if ToolExecuted([]ToolCall{{Name: "x", Executed: false}}) {
		t.Fatalf("want false when no call executed")
	}
	if !ToolExecuted([]ToolCall{{Name: "x", Executed: false}, {Name: "y", Executed: true}}) {
		t.Fatalf("want true when any call executed")
	}
}

// ---- ScoreStructured ------------------------------------------------------

func TestScoreStructured_ValidObject(t *testing.T) {
	sc := ScoreStructured(map[string]any{"a": 1})
	if !sc.Pass || sc.Value != 1 {
		t.Fatalf("want pass=true, got %+v", sc)
	}
}

func TestScoreStructured_Nil(t *testing.T) {
	sc := ScoreStructured(nil)
	if sc.Pass {
		t.Fatalf("want pass=false for nil, got %+v", sc)
	}
}

func TestScoreStructured_BareScalar(t *testing.T) {
	sc := ScoreStructured("just a string")
	if sc.Pass {
		t.Fatalf("want pass=false for bare scalar, got %+v", sc)
	}
}

func TestScoreStructuredWithSchema_RequiredFieldMissing(t *testing.T) {
	schema := []byte(`{"type":"object","required":["name","age"],"properties":{"age":{"type":"integer"}}}`)
	sc := ScoreStructuredWithSchema(map[string]any{"name": "x"}, schema)
	if sc.Pass {
		t.Fatalf("want pass=false, missing required field 'age', got %+v", sc)
	}
}

func TestScoreStructuredWithSchema_TypeMismatch(t *testing.T) {
	schema := []byte(`{"type":"object","required":["age"],"properties":{"age":{"type":"integer"}}}`)
	sc := ScoreStructuredWithSchema(map[string]any{"age": "not a number"}, schema)
	if sc.Pass {
		t.Fatalf("want pass=false, wrong type, got %+v", sc)
	}
}

func TestScoreStructuredWithSchema_Valid(t *testing.T) {
	schema := []byte(`{"type":"object","required":["name","age"],"properties":{"age":{"type":"integer"},"name":{"type":"string"}}}`)
	sc := ScoreStructuredWithSchema(map[string]any{"name": "x", "age": float64(5)}, schema)
	if !sc.Pass {
		t.Fatalf("want pass=true, got %+v", sc)
	}
}

// ---- ScoreStream ----------------------------------------------------------

func TestScoreStream_Measured(t *testing.T) {
	sc := ScoreStream(120)
	if !sc.Pass || sc.Value != 1 {
		t.Fatalf("want pass=true, got %+v", sc)
	}
}

func TestScoreStream_NotMeasured(t *testing.T) {
	sc := ScoreStream(0)
	if sc.Pass {
		t.Fatalf("want pass=false for zero TTFT, got %+v", sc)
	}
}

func TestScoreStream_Negative(t *testing.T) {
	sc := ScoreStream(-5)
	if sc.Pass {
		t.Fatalf("want pass=false for negative TTFT, got %+v", sc)
	}
}

// ---- reproducibility (the determinism guard's fixture) ---------------------

// TestDeterministic_Reproducible is the fixture the determinism guard
// (scripts/arena-determinism-guard.sh) runs to prove the scoring package is
// reproducible: the SAME input, scored 100 times in a loop, MUST produce
// byte-identical Score values every time. Any dependency on wall-clock,
// randomness, or unordered map iteration would show up here as a flaky
// Detail string or a varying Value. `go test -count=N` on top of this
// re-executes the whole test process N times, catching any process-level
// nondeterminism (e.g. accidental reliance on ASLR/pointer values leaking
// into a %v format) that a single in-process loop could miss.
func TestDeterministic_Reproducible(t *testing.T) {
	type fixture struct {
		name string
		run  func() Score
	}
	fixtures := []fixture{
		{"qa-exact", func() Score { return ScoreQA("Paris is the capital of France.", "Paris is the capital of France.") }},
		{"qa-containment", func() Score {
			return ScoreQA("The chemical symbol for gold is Au, atomic number 79.", "The chemical symbol for gold is Au.")
		}},
		{"qa-miss", func() Score { return ScoreQA("Berlin.", "Paris is the capital of France.") }},
		{"memory", func() Score { return ScoreMemory("Your favorite color is teal.", "Your favorite color is teal.") }},
		{"rag-hit", func() Score {
			return ScoreRAG("08-blue-guarantee.md", []string{"08-blue-guarantee.md", "09-quiet-car.md"}, "answer", false)
		}},
		{"rag-trap-refused", func() Score {
			return ScoreRAG("", nil, "That information is not in the source documents.", true)
		}},
		{"tools-match", func() Score {
			calls := []ToolCall{
				{Name: "web_search", Arguments: map[string]any{"query": "x"}, Executed: true},
				{Name: "calculate", Arguments: map[string]any{"expression": "47 * 89", "precision": 2}, Executed: true},
			}
			return ScoreTools(calls, "calculate", map[string]any{"expression": "47 * 89", "precision": "2"})
		}},
		{"tools-mismatch", func() Score {
			calls := []ToolCall{{Name: "calculate", Arguments: map[string]any{"expression": "1+2"}, Executed: true}}
			return ScoreTools(calls, "calculate", map[string]any{"expression": "1+1", "unit": "usd"})
		}},
		{"tools-numeric", func() Score {
			calls := []ToolCall{{Name: "calculate", Executed: true}}
			return ScoreToolsNumeric(calls, "The answer is 4,183.", "4183")
		}},
		{"structured-valid", func() Score {
			return ScoreStructured(map[string]any{"z": 1, "a": []any{1, 2, 3}, "m": map[string]any{"k": "v"}})
		}},
		{"structured-schema", func() Score {
			schema := []byte(`{"type":"object","required":["name","age","tags"],"properties":{"age":{"type":"integer"},"name":{"type":"string"},"tags":{"type":"array"}}}`)
			return ScoreStructuredWithSchema(map[string]any{"name": "x", "age": float64(5), "tags": []any{"a", "b"}}, schema)
		}},
		{"stream-measured", func() Score { return ScoreStream(842) }},
		{"stream-zero", func() Score { return ScoreStream(0) }},
	}

	// Establish the baseline on iteration 0, then assert every subsequent
	// iteration of every fixture matches it byte-for-byte (Value, Pass, and
	// Detail all compared).
	baseline := make(map[string]Score, len(fixtures))
	for _, f := range fixtures {
		baseline[f.name] = f.run()
	}

	const iterations = 100
	for i := 1; i < iterations; i++ {
		for _, f := range fixtures {
			got := f.run()
			want := baseline[f.name]
			if got != want {
				t.Fatalf("non-deterministic score for fixture %q at iteration %d:\n  want %+v\n  got  %+v", f.name, i, want, got)
			}
		}
	}
}
