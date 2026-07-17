package memory

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/dgraph-io/badger/v4"
)

// TestAllowCorruptBucket covers Allow's load-error branch: a stored bucket
// value that is not valid JSON must surface a wrapped quota-check error rather
// than silently reset the tenant's bucket.
func TestAllowCorruptBucket(t *testing.T) {
	wm := newTestStore(t, 10)
	l := wm.NewLimiter(1, 5)
	if err := wm.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(quotaPrefix+"acme"), []byte("{not-json"))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Allow(context.Background(), "acme"); err == nil || !strings.Contains(err.Error(), "quota check") {
		t.Fatalf("Allow over a corrupt bucket = %v, want a wrapped quota-check error", err)
	}
}

// TestPressureCorruptBucket covers Pressure's load-error branch symmetrically.
func TestPressureCorruptBucket(t *testing.T) {
	wm := newTestStore(t, 10)
	l := wm.NewLimiter(1, 5)
	if err := wm.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(quotaPrefix+"acme"), []byte("{not-json"))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Pressure(context.Background(), "acme"); err == nil || !strings.Contains(err.Error(), "quota pressure") {
		t.Fatalf("Pressure over a corrupt bucket = %v, want a wrapped pressure error", err)
	}
}

// TestPressureDegenerateBurst covers the l.burst <= 0 branch: a bucket with no
// capacity is treated as fully consumed (pressure 1). NewLimiter clamps burst
// to >=1, so the Limiter is constructed directly to reach this defensive path
// that guards against a zeroed/corrupt capacity.
func TestPressureDegenerateBurst(t *testing.T) {
	wm := newTestStore(t, 10)
	l := &Limiter{db: wm.db, rate: 0, burst: 0, now: time.Now, locks: make(map[string]*sync.Mutex)}
	p, err := l.Pressure(context.Background(), "acme")
	if err != nil {
		t.Fatalf("Pressure: %v", err)
	}
	if p != 1 {
		t.Fatalf("degenerate-burst pressure = %v, want 1", p)
	}
}

// TestPressureClampsStoredOverflow covers the pressure<0 and pressure>1 clamp
// branches, which guard against a persisted bucket left inconsistent with the
// current burst (e.g. capacity reduced between process runs). An over-full
// stored bucket clamps to 0; a negative-token stored bucket clamps to 1.
func TestPressureClampsStoredOverflow(t *testing.T) {
	wm := newTestStore(t, 10)
	// rate 0 so no refill perturbs the stored tokens; frozen clock.
	l := wm.NewLimiter(0, 4)
	frozen := time.Now()
	l.now = func() time.Time { return frozen }

	writeBucket := func(tenant string, tokens float64) {
		b := bucket{Tokens: tokens, LastRefillNS: frozen.UnixNano()}
		payload, _ := json.Marshal(b)
		if err := wm.db.Update(func(txn *badger.Txn) error {
			return txn.Set([]byte(quotaPrefix+tenant), payload)
		}); err != nil {
			t.Fatal(err)
		}
	}

	// tokens > burst => 1 - tokens/burst < 0 => clamp to 0.
	writeBucket("over", 10)
	if p, err := l.Pressure(context.Background(), "over"); err != nil || p != 0 {
		t.Fatalf("Pressure(over-full) = (%v,%v), want (0,nil)", p, err)
	}
	// tokens < 0 => 1 - tokens/burst > 1 => clamp to 1.
	writeBucket("under", -3)
	if p, err := l.Pressure(context.Background(), "under"); err != nil || p != 1 {
		t.Fatalf("Pressure(negative-tokens) = (%v,%v), want (1,nil)", p, err)
	}
}

// --- summarizer branches --------------------------------------------------

// TestNoopSummarizerEmptyBatch covers the len(oldest)==0 short-circuit: an
// empty condensation batch returns the existing summary unchanged.
func TestNoopSummarizerEmptyBatch(t *testing.T) {
	got, err := NoopSummarizer{}.Condense(context.Background(), "prior state", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "prior state" {
		t.Fatalf("Condense(empty) = %q, want the existing summary unchanged", got)
	}
}

// TestBoundNoopSummaryUTF8Boundary drives boundNoopSummary past its cap with a
// multi-byte run so the tail cut lands mid-rune, exercising the rune-start
// advance that prevents emitting a split (invalid) UTF-8 character.
func TestBoundNoopSummaryUTF8Boundary(t *testing.T) {
	// Each "世" is 3 bytes; well over maxNoopSummaryLen so the tail is cut.
	long := strings.Repeat("世", maxNoopSummaryLen)
	out := boundNoopSummary(long)

	if len(out) > maxNoopSummaryLen {
		// The elision marker + kept tail must respect the cap boundary intent.
		if !strings.HasPrefix(out, noopElisionMarker) {
			t.Fatalf("over-cap summary not marked as elided: %.40q", out)
		}
	}
	if !strings.HasPrefix(out, noopElisionMarker) {
		t.Fatalf("expected elision marker prefix, got %.40q", out)
	}
	// The kept tail (after the marker) must be valid UTF-8 — no split rune.
	tail := strings.TrimPrefix(out, noopElisionMarker)
	if !utf8.ValidString(tail) {
		t.Fatal("boundNoopSummary produced invalid UTF-8 (split a multi-byte rune)")
	}
}
