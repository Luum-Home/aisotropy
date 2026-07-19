package lang

import "testing"

// TestDetect_ShortEnglishPrompts is the regression guard for the low-accuracy
// misclassification bug: short English chat prompts (incl. the arena QA/memory
// prompts) must detect as English, not German. Low-accuracy mode called three of
// these German with high confidence; high-accuracy mode (newDetector) fixes it.
func TestDetect_ShortEnglishPrompts(t *testing.T) {
	english := []string{
		"What is the capital of France?",
		"Which planet in our solar system is the largest?",
		"What is my favorite color?",
		"What is the chemical symbol for gold?",
		"Who wrote the play Romeo and Juliet?",
	}
	for _, p := range english {
		if got := Detect(p); got != Language("en") {
			t.Errorf("Detect(%q) = %q, want en", p, got)
		}
	}
	// Sanity: a clearly non-English short prompt still detects correctly.
	if got := Detect("¿Cuál es la capital de Francia?"); got != Language("es") {
		t.Errorf("Detect(spanish) = %q, want es", got)
	}
}
