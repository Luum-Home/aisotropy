package memory

import (
	"context"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// TestLimiterForReplicasSplitsBurst is the multi-replica half of the
// constitutional gate: N replicas each running their own embedded bucket must
// not collectively hand a tenant N times its burst. With the fleet size
// declared, each replica's bucket is 1/N of the fleet-wide capacity.
func TestLimiterForReplicasSplitsBurst(t *testing.T) {
	w, err := OpenInMemory(0, NoopSummarizer{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	// Fleet-wide intent: burst 4. This process is 1 of 4 replicas, so its own
	// bucket must hold 1 token, not 4.
	l := w.NewLimiterForReplicas(0, 4, 4)
	frozen := time.Now()
	l.now = func() time.Time { return frozen }
	ctx := context.Background()

	ok, err := l.Allow(ctx, "acme")
	if err != nil || !ok {
		t.Fatalf("first Allow = (%v, %v), want (true, nil)", ok, err)
	}
	ok, err = l.Allow(ctx, "acme")
	if err != nil {
		t.Fatalf("second Allow error: %v", err)
	}
	if ok {
		t.Fatal("second Allow granted: this replica handed out the WHOLE fleet burst; 4 replicas would give the tenant 4x its quota")
	}
}

// TestLimiterForReplicasSplitsRate checks the sustained-rate half: the refill
// rate is divided too, so the fleet refills at the declared rate rather than at
// N times it.
func TestLimiterForReplicasSplitsRate(t *testing.T) {
	w, err := OpenInMemory(0, NoopSummarizer{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	// Fleet-wide 4 req/s over 4 replicas => 1 req/s here. Burst 4 => 1 here.
	l := w.NewLimiterForReplicas(4, 4, 4)
	now := time.Now()
	l.now = func() time.Time { return now }
	ctx := context.Background()

	if ok, err := l.Allow(ctx, "acme"); err != nil || !ok {
		t.Fatalf("initial Allow = (%v, %v), want (true, nil)", ok, err)
	}
	// Half a second later a 4/s replica would have refilled 2 tokens; a 1/s
	// replica has refilled 0.5 and must still deny.
	now = now.Add(500 * time.Millisecond)
	if ok, err := l.Allow(ctx, "acme"); err != nil || ok {
		t.Fatalf("Allow after 500ms = (%v, %v), want (false, nil): the per-replica refill rate was not divided by the fleet size", ok, err)
	}
	// A full second in, the divided rate has refilled exactly one token.
	now = now.Add(500 * time.Millisecond)
	if ok, err := l.Allow(ctx, "acme"); err != nil || !ok {
		t.Fatalf("Allow after 1s = (%v, %v), want (true, nil): the divided rate must still refill", ok, err)
	}
}

// TestLimiterForReplicasDefaultIsUnchanged pins the default: a fleet of one (or
// an unset/invalid replica count) behaves exactly like NewLimiter.
func TestLimiterForReplicasDefaultIsUnchanged(t *testing.T) {
	w, err := OpenInMemory(0, NoopSummarizer{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	for _, replicas := range []int{1, 0, -3} {
		l := w.NewLimiterForReplicas(7, 5, replicas)
		if l.rate != 7 || l.burst != 5 {
			t.Fatalf("replicas=%d: rate/burst = %v/%v, want 7/5 (unchanged)", replicas, l.rate, l.burst)
		}
	}
}

// TestLimiterForReplicasBurstFloor documents the one place the split is NOT
// conservative: a replica's bucket never drops below a single token, so a fleet
// larger than the declared burst can still exceed it (N replicas x 1 token).
func TestLimiterForReplicasBurstFloor(t *testing.T) {
	w, err := OpenInMemory(0, NoopSummarizer{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	l := w.NewLimiterForReplicas(10, 2, 8) // 2/8 = 0.25, floored to 1
	if l.burst != 1 {
		t.Fatalf("burst = %v, want the 1-token floor", l.burst)
	}
	if l.rate != 10.0/8.0 {
		t.Fatalf("rate = %v, want 1.25", l.rate)
	}
}

// TestLimiterRejectsCorruptBucket: a bucket value that will not decode must fail
// the check rather than be treated as a fresh, full bucket. Failing open here
// would let a corrupted key hand a tenant unlimited pre-LLM admissions — the
// constitutional gate inverted by a decoding accident.
func TestLimiterRejectsCorruptBucket(t *testing.T) {
	w, err := OpenInMemory(0, NoopSummarizer{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	if err := w.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(quotaPrefix+"acme"), []byte("{not json"))
	}); err != nil {
		t.Fatalf("seed corrupt bucket: %v", err)
	}

	l := w.NewLimiter(1, 5)
	ok, err := l.Allow(context.Background(), "acme")
	if err == nil {
		t.Fatal("Allow accepted a corrupt bucket without error")
	}
	if ok {
		t.Fatal("Allow ADMITTED the request over a corrupt bucket — the quota gate failed open")
	}
}
