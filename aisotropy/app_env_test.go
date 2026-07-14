package aisotropy

import "testing"

// TestRatePerSecFromEnv is the WP3 acceptance criterion: the per-tenant rate
// limiter's requests/sec is configurable via AISOTROPY_RATE_PER_SEC, defaults
// to defaultRatePerSec when unset, and falls back to the same default (with a
// logged warning) on an unparsable or non-positive value — the limiter is
// never constructed with a zero or negative rate.
func TestRatePerSecFromEnv(t *testing.T) {
	t.Run("unset defaults", func(t *testing.T) {
		t.Setenv("AISOTROPY_RATE_PER_SEC", "")
		if got := ratePerSecFromEnv(); got != defaultRatePerSec {
			t.Errorf("ratePerSecFromEnv() = %v, want default %v", got, defaultRatePerSec)
		}
	})

	t.Run("parses a valid float", func(t *testing.T) {
		t.Setenv("AISOTROPY_RATE_PER_SEC", "2.5")
		if got := ratePerSecFromEnv(); got != 2.5 {
			t.Errorf("ratePerSecFromEnv() = %v, want 2.5", got)
		}
	})

	t.Run("parses a valid integer", func(t *testing.T) {
		t.Setenv("AISOTROPY_RATE_PER_SEC", "50")
		if got := ratePerSecFromEnv(); got != 50 {
			t.Errorf("ratePerSecFromEnv() = %v, want 50", got)
		}
	})

	t.Run("garbage falls back to default", func(t *testing.T) {
		t.Setenv("AISOTROPY_RATE_PER_SEC", "not-a-number")
		if got := ratePerSecFromEnv(); got != defaultRatePerSec {
			t.Errorf("ratePerSecFromEnv() = %v, want default %v on invalid input", got, defaultRatePerSec)
		}
	})

	t.Run("zero falls back to default", func(t *testing.T) {
		t.Setenv("AISOTROPY_RATE_PER_SEC", "0")
		if got := ratePerSecFromEnv(); got != defaultRatePerSec {
			t.Errorf("ratePerSecFromEnv() = %v, want default %v on zero", got, defaultRatePerSec)
		}
	})

	t.Run("negative falls back to default", func(t *testing.T) {
		t.Setenv("AISOTROPY_RATE_PER_SEC", "-3")
		if got := ratePerSecFromEnv(); got != defaultRatePerSec {
			t.Errorf("ratePerSecFromEnv() = %v, want default %v on negative", got, defaultRatePerSec)
		}
	})
}

// TestBurstFromEnv mirrors TestRatePerSecFromEnv for AISOTROPY_BURST: an
// integer bucket capacity, defaulting to defaultBurst when unset, unparsable,
// or non-positive.
func TestBurstFromEnv(t *testing.T) {
	t.Run("unset defaults", func(t *testing.T) {
		t.Setenv("AISOTROPY_BURST", "")
		if got := burstFromEnv(); got != defaultBurst {
			t.Errorf("burstFromEnv() = %v, want default %v", got, defaultBurst)
		}
	})

	t.Run("parses a valid integer", func(t *testing.T) {
		t.Setenv("AISOTROPY_BURST", "100")
		if got := burstFromEnv(); got != 100 {
			t.Errorf("burstFromEnv() = %v, want 100", got)
		}
	})

	t.Run("garbage falls back to default", func(t *testing.T) {
		t.Setenv("AISOTROPY_BURST", "lots")
		if got := burstFromEnv(); got != defaultBurst {
			t.Errorf("burstFromEnv() = %v, want default %v on invalid input", got, defaultBurst)
		}
	})

	t.Run("zero falls back to default", func(t *testing.T) {
		t.Setenv("AISOTROPY_BURST", "0")
		if got := burstFromEnv(); got != defaultBurst {
			t.Errorf("burstFromEnv() = %v, want default %v on zero", got, defaultBurst)
		}
	})

	t.Run("negative falls back to default", func(t *testing.T) {
		t.Setenv("AISOTROPY_BURST", "-1")
		if got := burstFromEnv(); got != defaultBurst {
			t.Errorf("burstFromEnv() = %v, want default %v on negative", got, defaultBurst)
		}
	})
}

// TestWithRateLimitMarksExplicit verifies the precedence seam Run relies on:
// only an explicit WithRateLimit sets rateLimitSet, so env overrides apply
// exclusively when the consumer did not choose values programmatically.
func TestWithRateLimitMarksExplicit(t *testing.T) {
	if a := New(); a.rateLimitSet {
		t.Error("New() without WithRateLimit must leave rateLimitSet false (env override active)")
	}
	if a := New(WithRateLimit(5, 7)); !a.rateLimitSet {
		t.Error("WithRateLimit must set rateLimitSet so env vars do not override explicit values")
	}
}
