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

// TestReplicasFromEnv pins the declared fleet size, which two separate controls
// read: the per-tenant quota split (memory.NewLimiterForReplicas) and the
// erasure receipt's completeness claim (EraseReport.Complete). Every unusable
// value falls back to 1 — a single process — because that is the only assumption
// that cannot make either control claim MORE than it delivers: a fleet declared
// too small splits the quota too little (visible over-admission) rather than
// declaring an erasure complete when it was not.
func TestReplicasFromEnv(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		want int
	}{
		{"unset means a single process", "", 1},
		{"parses a fleet size", "6", 6},
		{"garbage falls back to 1", "three", 1},
		{"zero falls back to 1", "0", 1},
		{"negative falls back to 1", "-4", 1},
		{"float falls back to 1", "2.5", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AISOTROPY_REPLICAS", tc.env)
			if got := replicasFromEnv(); got != tc.want {
				t.Errorf("replicasFromEnv() with %q = %d, want %d", tc.env, got, tc.want)
			}
		})
	}
}
