package aisotropy

import (
	"testing"
	"time"
)

// TestSemanticTurnTTLFromEnv covers the sleep-cycle Turn-kind age-TTL parser:
// a valid duration is honored; unset, unparsable, and non-positive values all
// fall back to the bounded default so the policy is never accidentally
// unbounded from a bad env value.
func TestSemanticTurnTTLFromEnv(t *testing.T) {
	t.Run("unset defaults", func(t *testing.T) {
		t.Setenv("AISOTROPY_SEMANTIC_TURN_TTL", "")
		if got := semanticTurnTTLFromEnv(); got != defaultSemanticTurnTTL {
			t.Errorf("semanticTurnTTLFromEnv() = %v, want default %v", got, defaultSemanticTurnTTL)
		}
	})
	t.Run("parses a valid duration", func(t *testing.T) {
		t.Setenv("AISOTROPY_SEMANTIC_TURN_TTL", "24h")
		if got := semanticTurnTTLFromEnv(); got != 24*time.Hour {
			t.Errorf("semanticTurnTTLFromEnv() = %v, want 24h", got)
		}
	})
	t.Run("garbage falls back to default", func(t *testing.T) {
		t.Setenv("AISOTROPY_SEMANTIC_TURN_TTL", "not-a-duration")
		if got := semanticTurnTTLFromEnv(); got != defaultSemanticTurnTTL {
			t.Errorf("semanticTurnTTLFromEnv() = %v, want default on invalid", got)
		}
	})
	t.Run("non-positive falls back to default", func(t *testing.T) {
		t.Setenv("AISOTROPY_SEMANTIC_TURN_TTL", "-5h")
		if got := semanticTurnTTLFromEnv(); got != defaultSemanticTurnTTL {
			t.Errorf("semanticTurnTTLFromEnv() = %v, want default on non-positive", got)
		}
	})
}

// TestSemanticTurnMaxFromEnv covers the Turn-kind count-cap parser: a positive
// integer is honored; unset, unparsable, and non-positive fall back to default.
func TestSemanticTurnMaxFromEnv(t *testing.T) {
	t.Run("unset defaults", func(t *testing.T) {
		t.Setenv("AISOTROPY_SEMANTIC_TURN_MAX", "")
		if got := semanticTurnMaxFromEnv(); got != defaultSemanticTurnMax {
			t.Errorf("semanticTurnMaxFromEnv() = %v, want default %v", got, defaultSemanticTurnMax)
		}
	})
	t.Run("parses a valid integer", func(t *testing.T) {
		t.Setenv("AISOTROPY_SEMANTIC_TURN_MAX", "42")
		if got := semanticTurnMaxFromEnv(); got != 42 {
			t.Errorf("semanticTurnMaxFromEnv() = %v, want 42", got)
		}
	})
	t.Run("garbage falls back to default", func(t *testing.T) {
		t.Setenv("AISOTROPY_SEMANTIC_TURN_MAX", "many")
		if got := semanticTurnMaxFromEnv(); got != defaultSemanticTurnMax {
			t.Errorf("semanticTurnMaxFromEnv() = %v, want default on invalid", got)
		}
	})
	t.Run("non-positive falls back to default", func(t *testing.T) {
		t.Setenv("AISOTROPY_SEMANTIC_TURN_MAX", "0")
		if got := semanticTurnMaxFromEnv(); got != defaultSemanticTurnMax {
			t.Errorf("semanticTurnMaxFromEnv() = %v, want default on zero", got)
		}
	})
}

// TestCostBudgetFromEnv covers the gateway cost-budget parser: a positive USD
// value enables shaping; unset, unparsable, and non-positive keep the seam a
// pass-through (0).
func TestCostBudgetFromEnv(t *testing.T) {
	t.Run("unset returns 0", func(t *testing.T) {
		t.Setenv("AISOTROPY_COST_BUDGET_USD", "")
		if got := costBudgetFromEnv(); got != 0 {
			t.Errorf("costBudgetFromEnv() = %v, want 0", got)
		}
	})
	t.Run("parses a valid float", func(t *testing.T) {
		t.Setenv("AISOTROPY_COST_BUDGET_USD", "12.5")
		if got := costBudgetFromEnv(); got != 12.5 {
			t.Errorf("costBudgetFromEnv() = %v, want 12.5", got)
		}
	})
	t.Run("garbage returns 0", func(t *testing.T) {
		t.Setenv("AISOTROPY_COST_BUDGET_USD", "free")
		if got := costBudgetFromEnv(); got != 0 {
			t.Errorf("costBudgetFromEnv() = %v, want 0 on invalid", got)
		}
	})
	t.Run("non-positive returns 0", func(t *testing.T) {
		t.Setenv("AISOTROPY_COST_BUDGET_USD", "-3")
		if got := costBudgetFromEnv(); got != 0 {
			t.Errorf("costBudgetFromEnv() = %v, want 0 on non-positive", got)
		}
	})
}

// TestRequireTLSFromEnv covers the plaintext-rejection gate parser: only a
// truthy value turns it on; unset, empty, and unparsable are off (the
// behind-proxy default posture).
func TestRequireTLSFromEnv(t *testing.T) {
	t.Run("unset is off", func(t *testing.T) {
		t.Setenv("AISOTROPY_REQUIRE_TLS", "")
		if requireTLSFromEnv() {
			t.Error("requireTLSFromEnv() = true, want false when unset")
		}
	})
	t.Run("truthy is on", func(t *testing.T) {
		t.Setenv("AISOTROPY_REQUIRE_TLS", "true")
		if !requireTLSFromEnv() {
			t.Error("requireTLSFromEnv() = false, want true for \"true\"")
		}
	})
	t.Run("unparsable is off", func(t *testing.T) {
		t.Setenv("AISOTROPY_REQUIRE_TLS", "maybe")
		if requireTLSFromEnv() {
			t.Error("requireTLSFromEnv() = true, want false on unparsable value")
		}
	})
}

// TestGuardrailsBlockFromEnv covers the guardrail block-toggle parser: only a
// truthy value blocks; unset, empty, and unparsable keep the annotate+log
// default posture (off).
func TestGuardrailsBlockFromEnv(t *testing.T) {
	const name = "AISOTROPY_TEST_BLOCK_TOGGLE"
	t.Run("unset is off", func(t *testing.T) {
		if guardrailsBlockFromEnv(name) {
			t.Error("guardrailsBlockFromEnv() = true, want false when unset")
		}
	})
	t.Run("truthy is on", func(t *testing.T) {
		t.Setenv(name, "1")
		if !guardrailsBlockFromEnv(name) {
			t.Error("guardrailsBlockFromEnv() = false, want true for \"1\"")
		}
	})
	t.Run("unparsable is off", func(t *testing.T) {
		t.Setenv(name, "sometimes")
		if guardrailsBlockFromEnv(name) {
			t.Error("guardrailsBlockFromEnv() = true, want false on unparsable value")
		}
	})
}

// TestVisionModelFromEnv covers the vision-target reader: it trims whitespace
// and returns "" when unset.
func TestVisionModelFromEnv(t *testing.T) {
	t.Run("unset is empty", func(t *testing.T) {
		t.Setenv("AISOTROPY_VISION_MODEL", "")
		if got := visionModelFromEnv(); got != "" {
			t.Errorf("visionModelFromEnv() = %q, want empty", got)
		}
	})
	t.Run("trims surrounding whitespace", func(t *testing.T) {
		t.Setenv("AISOTROPY_VISION_MODEL", "  cheap  ")
		if got := visionModelFromEnv(); got != "cheap" {
			t.Errorf("visionModelFromEnv() = %q, want \"cheap\"", got)
		}
	})
}

// TestSplitCSV covers the comma-separated env parser: it trims, drops empty
// entries, and returns nil for a blank input.
func TestSplitCSV(t *testing.T) {
	if got := splitCSV(""); got != nil {
		t.Errorf("splitCSV(\"\") = %v, want nil", got)
	}
	if got := splitCSV("   "); got != nil {
		t.Errorf("splitCSV(whitespace) = %v, want nil", got)
	}
	got := splitCSV(" acme , ,globex,  ")
	if len(got) != 2 || got[0] != "acme" || got[1] != "globex" {
		t.Errorf("splitCSV = %v, want [acme globex] (trimmed, empties dropped)", got)
	}
}

// TestHealthProbeVector covers the semantic readiness probe vector builder: it
// returns a unit vector of the requested dimension (first component 1), and an
// empty slice for a zero dimension.
func TestHealthProbeVector(t *testing.T) {
	v := healthProbeVector(4)
	if len(v) != 4 {
		t.Fatalf("healthProbeVector(4) len = %d, want 4", len(v))
	}
	if v[0] != 1 || v[1] != 0 {
		t.Errorf("healthProbeVector(4) = %v, want a unit vector [1 0 0 0]", v)
	}
	if got := healthProbeVector(0); len(got) != 0 {
		t.Errorf("healthProbeVector(0) = %v, want empty", got)
	}
}
