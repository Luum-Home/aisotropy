package memory

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLimiterHighContention is a load test: correctness must hold at scale. With
// the clock frozen (no refill), 2000 goroutines racing a burst-500 bucket must
// grant EXACTLY 500 — heavy contention never lets a tenant exceed its bucket.
func TestLimiterHighContention(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in -short mode")
	}
	l := newTestLimiter(t, 0, 500)
	frozen := time.Now()
	l.now = func() time.Time { return frozen }

	const goroutines = 2000
	var granted int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if ok, err := l.Allow(context.Background(), "acme"); err == nil && ok {
				atomic.AddInt64(&granted, 1)
			}
		}()
	}
	wg.Wait()

	if granted != 500 {
		t.Fatalf("granted %d under 2000-way contention, want exactly 500 (burst)", granted)
	}
}

// TestLimiterSustainedRateApproximatesConfig checks the refill path under load:
// over a fixed window the granted count tracks burst + rate*window, deterministic
// because the clock is driven by the test rather than wall time.
func TestLimiterSustainedRateApproximatesConfig(t *testing.T) {
	l := newTestLimiter(t, 100, 10) // 100 tokens/s, burst 10
	start := time.Now()
	var mu sync.Mutex
	clock := start
	l.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	ctx := context.Background()

	granted := 0
	// Simulate 1 second in 1000 steps of 1ms; at 100/s a token frees ~every 10ms.
	for step := 0; step < 1000; step++ {
		mu.Lock()
		clock = start.Add(time.Duration(step) * time.Millisecond)
		mu.Unlock()
		if ok, _ := l.Allow(ctx, "acme"); ok {
			granted++
		}
	}
	// Expected ≈ burst(10) + rate(100)*1s = 110. Allow a small margin for the
	// discrete stepping.
	if granted < 105 || granted > 115 {
		t.Fatalf("granted %d over 1s at 100/s burst 10, want ~110", granted)
	}
}

// BenchmarkLimiterAllow measures the per-call cost of the BadgerDB-backed token
// bucket on the hot (granted) path, single tenant (worst-case serialization).
func BenchmarkLimiterAllow(b *testing.B) {
	w, err := OpenInMemory(0, NoopSummarizer{})
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	l := w.NewLimiter(1e9, 1e9) // effectively unlimited: every call is granted
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := l.Allow(ctx, "acme"); err != nil {
			b.Fatal(err)
		}
	}
}
