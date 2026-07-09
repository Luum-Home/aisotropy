package memory

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestLimiter(t *testing.T, rate, burst float64) *Limiter {
	t.Helper()
	w, err := OpenInMemory(0, NoopSummarizer{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w.NewLimiter(rate, burst)
}

// TestLimiterConcurrentCannotExceedBurst is the constitutional-gate test: under
// concurrent requests a tenant can never be granted more than its bucket.
func TestLimiterConcurrentCannotExceedBurst(t *testing.T) {
	l := newTestLimiter(t, 0, 5) // rate 0: no refill during the test
	frozen := time.Now()
	l.now = func() time.Time { return frozen } // freeze the clock

	const goroutines = 50
	var granted int64
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := l.Allow(context.Background(), "acme")
			if err != nil {
				t.Errorf("Allow: %v", err)
				return
			}
			if ok {
				atomic.AddInt64(&granted, 1)
			}
		}()
	}
	wg.Wait()

	if granted != 5 {
		t.Fatalf("granted %d tokens under concurrency, want exactly 5 (burst) — a tenant must never exceed its bucket", granted)
	}
}

func TestLimiterSequentialExhaustsThenDenies(t *testing.T) {
	l := newTestLimiter(t, 0, 3)
	frozen := time.Now()
	l.now = func() time.Time { return frozen }
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		ok, err := l.Allow(ctx, "acme")
		if err != nil || !ok {
			t.Fatalf("request %d: ok=%v err=%v, want granted", i, ok, err)
		}
	}
	// Bucket empty now.
	ok, err := l.Allow(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("4th request granted, want denied (bucket empty)")
	}
}

func TestLimiterRefillsOverTime(t *testing.T) {
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
	if ok, _ := l.Allow(ctx, "acme"); ok {
		t.Fatal("granted while empty")
	}
	// Advance 200ms → 10/s * 0.2s = 2 tokens refilled.
	now = now.Add(200 * time.Millisecond)
	got := 0
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow(ctx, "acme"); ok {
			got++
		}
	}
	if got != 2 {
		t.Fatalf("after refill granted %d, want 2 (capped at burst)", got)
	}
}

func TestLimiterIsolatesTenants(t *testing.T) {
	l := newTestLimiter(t, 0, 1)
	frozen := time.Now()
	l.now = func() time.Time { return frozen }
	ctx := context.Background()

	if ok, _ := l.Allow(ctx, "acme"); !ok {
		t.Fatal("acme first request denied")
	}
	if ok, _ := l.Allow(ctx, "acme"); ok {
		t.Fatal("acme second request granted (bucket should be empty)")
	}
	// A different tenant has its own full bucket.
	if ok, _ := l.Allow(ctx, "globex"); !ok {
		t.Fatal("globex denied — tenant buckets must be independent")
	}
}
