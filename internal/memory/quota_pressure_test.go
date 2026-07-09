package memory

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestPressureFreshTenantIsZero(t *testing.T) {
	l := newTestLimiter(t, 0, 5)
	frozen := time.Now()
	l.now = func() time.Time { return frozen }

	p, err := l.Pressure(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if p != 0 {
		t.Fatalf("fresh tenant pressure = %v, want 0", p)
	}
}

func TestPressureRisesAsTokensAreDrained(t *testing.T) {
	l := newTestLimiter(t, 0, 5) // rate 0: no refill during the test
	frozen := time.Now()
	l.now = func() time.Time { return frozen }
	ctx := context.Background()

	const burst = 5
	for i := 0; i < burst; i++ {
		ok, err := l.Allow(ctx, "acme")
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("Allow denied at drain step %d, want granted", i)
		}
		p, err := l.Pressure(ctx, "acme")
		if err != nil {
			t.Fatal(err)
		}
		want := float64(i+1) / burst
		if math.Abs(p-want) > 1e-9 {
			t.Fatalf("after %d drains: pressure = %v, want %v", i+1, p, want)
		}
	}
}

func TestPressureFullyDrainedBucketIsOne(t *testing.T) {
	l := newTestLimiter(t, 0, 3)
	frozen := time.Now()
	l.now = func() time.Time { return frozen }
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if ok, err := l.Allow(ctx, "acme"); err != nil || !ok {
			t.Fatalf("drain %d: ok=%v err=%v", i, ok, err)
		}
	}

	p, err := l.Pressure(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if p != 1 {
		t.Fatalf("fully-drained bucket pressure = %v, want 1", p)
	}
}

// TestPressureDoesNotConsumeTokens is the key read-only guarantee: calling
// Pressure repeatedly must never change what Allow subsequently grants.
func TestPressureDoesNotConsumeTokens(t *testing.T) {
	l := newTestLimiter(t, 0, 2)
	frozen := time.Now()
	l.now = func() time.Time { return frozen }
	ctx := context.Background()

	// Peek several times before consuming anything.
	for i := 0; i < 5; i++ {
		p, err := l.Pressure(ctx, "acme")
		if err != nil {
			t.Fatal(err)
		}
		if p != 0 {
			t.Fatalf("peek %d: pressure = %v, want 0 (Pressure must not consume tokens)", i, p)
		}
	}

	// Both burst tokens must still be available.
	for i := 0; i < 2; i++ {
		ok, err := l.Allow(ctx, "acme")
		if err != nil || !ok {
			t.Fatalf("Allow %d after peeking: ok=%v err=%v, want granted", i, ok, err)
		}
	}
	if ok, _ := l.Allow(ctx, "acme"); ok {
		t.Fatal("3rd Allow granted, want denied (burst is only 2)")
	}
}

func TestPressureReflectsRefillOverTime(t *testing.T) {
	l := newTestLimiter(t, 10, 2) // 10 tokens/sec, burst 2
	now := time.Now()
	l.now = func() time.Time { return now }
	ctx := context.Background()

	// Drain the burst.
	for i := 0; i < 2; i++ {
		if ok, _ := l.Allow(ctx, "acme"); !ok {
			t.Fatalf("drain %d denied", i)
		}
	}
	p, err := l.Pressure(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if p != 1 {
		t.Fatalf("immediately after drain: pressure = %v, want 1", p)
	}

	// Advance 100ms → 10/s * 0.1s = 1 token refilled → half the burst back.
	now = now.Add(100 * time.Millisecond)
	p, err = l.Pressure(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(p-0.5) > 1e-9 {
		t.Fatalf("after 100ms refill: pressure = %v, want 0.5", p)
	}
}

func TestPressureRequiresTenant(t *testing.T) {
	l := newTestLimiter(t, 0, 1)
	if _, err := l.Pressure(context.Background(), ""); err == nil {
		t.Fatal("Pressure with empty tenant: want error, got nil")
	}
}

func TestPressureRespectsCancelledContext(t *testing.T) {
	l := newTestLimiter(t, 0, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.Pressure(ctx, "acme"); err == nil {
		t.Fatal("Pressure with cancelled ctx: want error, got nil")
	}
}
