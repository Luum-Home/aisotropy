package memory

import (
	"context"
	"testing"
	"time"
)

// cancelledCtx returns a context already cancelled, for exercising the
// early ctx.Err() guard on every public method.
func cancelledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestAllowGuards(t *testing.T) {
	l := newTestLimiter(t, 1, 5)
	if _, err := l.Allow(context.Background(), ""); err == nil {
		t.Fatal("Allow with empty tenant = nil, want error")
	}
	if _, err := l.Allow(cancelledCtx(), "acme"); err == nil {
		t.Fatal("Allow with cancelled ctx = nil, want ctx error")
	}
}

func TestPressureGuardsAndComputation(t *testing.T) {
	l := newTestLimiter(t, 0, 4) // rate 0: no refill so pressure is deterministic
	frozen := time.Now()
	l.now = func() time.Time { return frozen }
	ctx := context.Background()

	if _, err := l.Pressure(ctx, ""); err == nil {
		t.Fatal("Pressure with empty tenant = nil, want error")
	}
	if _, err := l.Pressure(cancelledCtx(), "acme"); err == nil {
		t.Fatal("Pressure with cancelled ctx = nil, want ctx error")
	}

	// Fresh bucket: pressure 0 (full headroom).
	p, err := l.Pressure(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if p != 0 {
		t.Fatalf("fresh-bucket pressure = %v, want 0", p)
	}

	// Consume 2 of 4 tokens -> pressure 0.5.
	for i := 0; i < 2; i++ {
		if ok, err := l.Allow(ctx, "acme"); err != nil || !ok {
			t.Fatalf("Allow %d = (%v,%v)", i, ok, err)
		}
	}
	p, err = l.Pressure(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if p < 0.49 || p > 0.51 {
		t.Fatalf("pressure after consuming half = %v, want ~0.5", p)
	}

	// Drain the rest -> pressure 1.
	for i := 0; i < 2; i++ {
		_, _ = l.Allow(ctx, "acme")
	}
	p, err = l.Pressure(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if p != 1 {
		t.Fatalf("drained-bucket pressure = %v, want 1", p)
	}
}

// TestPressureRefillLowersPressure exercises the elapsed-time refill branch of
// Pressure (a nonzero rate + advancing clock reduces measured pressure).
func TestPressureRefillLowersPressure(t *testing.T) {
	l := newTestLimiter(t, 10, 4) // 10 tokens/sec
	now := time.Now()
	l.now = func() time.Time { return now }
	ctx := context.Background()

	// Drain the bucket -> pressure 1.
	for i := 0; i < 4; i++ {
		_, _ = l.Allow(ctx, "acme")
	}
	if p, _ := l.Pressure(ctx, "acme"); p != 1 {
		t.Fatalf("drained pressure = %v, want 1", p)
	}
	// Advance 200ms => 2 tokens refilled => pressure 0.5.
	now = now.Add(200 * time.Millisecond)
	p, err := l.Pressure(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if p < 0.49 || p > 0.51 {
		t.Fatalf("pressure after refill = %v, want ~0.5", p)
	}
}

func TestRecentGuards(t *testing.T) {
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}

	if _, err := wm.Recent(cancelledCtx(), key, 5); err == nil {
		t.Fatal("Recent with cancelled ctx = nil, want ctx error")
	}
	if _, err := wm.Recent(context.Background(), Key{}, 5); err == nil {
		t.Fatal("Recent with invalid key = nil, want validation error")
	}
	// n <= 0 short-circuits to an empty result with no error.
	got, err := wm.Recent(context.Background(), key, 0)
	if err != nil {
		t.Fatalf("Recent(n=0): %v", err)
	}
	if got != nil {
		t.Fatalf("Recent(n=0) = %v, want nil", got)
	}
}

func TestSummaryGuards(t *testing.T) {
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}

	if _, err := wm.Summary(cancelledCtx(), key); err == nil {
		t.Fatal("Summary with cancelled ctx = nil, want ctx error")
	}
	if _, err := wm.Summary(context.Background(), Key{}); err == nil {
		t.Fatal("Summary with invalid key = nil, want validation error")
	}
	// No condensation yet: empty summary, no error.
	sum, err := wm.Summary(context.Background(), key)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if sum != "" {
		t.Fatalf("Summary before any condensation = %q, want empty", sum)
	}
}

func TestAppendGuards(t *testing.T) {
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	msg := Message{Role: "user", Content: "x"}

	if err := wm.Append(cancelledCtx(), key, msg); err == nil {
		t.Fatal("Append with cancelled ctx = nil, want ctx error")
	}
	if err := wm.Append(context.Background(), Key{}, msg); err == nil {
		t.Fatal("Append with invalid key = nil, want validation error")
	}
}
