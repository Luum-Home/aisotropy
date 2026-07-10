package memory

import (
	"context"
	"fmt"
	"testing"
)

// benchWindow matches the "~10-15 messages" active window described in
// docs/architecture/memory-tiers.md -> "Working memory": the summary
// buffer's whole point is that the active window stays this small forever,
// no matter how long the session runs, so this is the realistic steady
// state to benchmark — not an unbounded buffer.
const benchWindow = 12

// BenchmarkWorkingMemory_AppendRead measures the cost of a realistic
// hot-path cycle: append one message, then read back the recent window —
// the pair of operations Gollem's memory hydration step performs on every
// request (see docs/architecture/memory-tiers.md -> "Working memory"). Once
// the window fills up, every iteration also exercises the condense+prune
// path (via NoopSummarizer), since that is the actual steady state of a
// long-running session.
//
// Phase 2's definition of done requires session read/write in BadgerDB
// under 1ms (see docs/roadmap/milestones.md -> "Phase 2"); this benchmark
// reports ns/op so that target is directly verifiable via `go test -bench`.
func BenchmarkWorkingMemory_AppendRead(b *testing.B) {
	ctx := context.Background()
	wm, err := OpenInMemory(benchWindow, NoopSummarizer{})
	if err != nil {
		b.Fatalf("OpenInMemory: %v", err)
	}
	defer wm.Close()

	key := Key{Tenant: "bench-tenant", User: "bench-user", Session: "bench-session"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		msg := Message{Role: "user", Content: fmt.Sprintf("benchmark message body %d", i)}
		if err := wm.Append(ctx, key, msg); err != nil {
			b.Fatalf("Append: %v", err)
		}
		if _, err := wm.Recent(ctx, key, 10); err != nil {
			b.Fatalf("Recent: %v", err)
		}
	}
}

// BenchmarkWorkingMemory_AppendOnly isolates the write path alone (no read),
// for comparison against the combined append+read benchmark above.
func BenchmarkWorkingMemory_AppendOnly(b *testing.B) {
	ctx := context.Background()
	wm, err := OpenInMemory(benchWindow, NoopSummarizer{})
	if err != nil {
		b.Fatalf("OpenInMemory: %v", err)
	}
	defer wm.Close()

	key := Key{Tenant: "bench-tenant", User: "bench-user", Session: "bench-session"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		msg := Message{Role: "user", Content: fmt.Sprintf("benchmark message body %d", i)}
		if err := wm.Append(ctx, key, msg); err != nil {
			b.Fatalf("Append: %v", err)
		}
	}
}

// BenchmarkWorkingMemory_AppendRead_LargeWindow stress-tests a
// misconfigured, unrealistically large window (not the documented ~10-15
// message design) to make the cost model explicit: Append's key-count scan
// is O(active window size), so callers must keep maxActiveMessages small,
// exactly as the architecture doc prescribes — this is not expected to hit
// the sub-millisecond target and is intentionally reported here for
// visibility rather than left as a silent gap.
func BenchmarkWorkingMemory_AppendRead_LargeWindow(b *testing.B) {
	ctx := context.Background()
	wm, err := OpenInMemory(1000, NoopSummarizer{})
	if err != nil {
		b.Fatalf("OpenInMemory: %v", err)
	}
	defer wm.Close()

	key := Key{Tenant: "bench-tenant", User: "bench-user", Session: "bench-session"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		msg := Message{Role: "user", Content: fmt.Sprintf("benchmark message body %d", i)}
		if err := wm.Append(ctx, key, msg); err != nil {
			b.Fatalf("Append: %v", err)
		}
		if _, err := wm.Recent(ctx, key, 10); err != nil {
			b.Fatalf("Recent: %v", err)
		}
	}
}
