package memory

// scoping_bench_test.go isolates the two memory hot paths the existing
// working_memory_bench_test.go (Append+Recent cycle) does not report on their
// own: (1) the per-request KEY SCOPING work — Key.Validate plus the derived
// BadgerDB sub-key construction that is the enforcement point for tenant/user
// isolation (key.go), run on every single memory operation; and (2) the READ
// side of hydration in isolation — Recent and Summary — which every turn calls
// to fold the working-memory tier into the system prompt.
//
// The model is not involved in any of this: these are pure Go/BadgerDB costs, so
// every ns/op reported is framework overhead the caller pays per turn regardless
// of which LLM (or the keyless stub) ultimately serves it. Isolation is not free
// but it is measured here so its price is explicit rather than assumed.

import (
	"context"
	"fmt"
	"testing"
)

// benchScopeKey is the realistic three-component identity every operation is
// scoped by; its components are ordinary ASCII, the common case.
var benchScopeKey = Key{Tenant: "acme-prod", User: "alice-42", Session: "chat-session-0007"}

// BenchmarkKeyValidate measures the isolation gate that runs before EVERY
// working-memory read/write: the non-empty + no-reserved-character check on all
// three identity components (the check that makes a forged cross-tenant prefix
// impossible, see reservedKeyChars). It touches no store, so this is the pure
// per-operation validation tax.
func BenchmarkKeyValidate(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := benchScopeKey.Validate(); err != nil {
			b.Fatalf("Validate: %v", err)
		}
	}
}

// BenchmarkKeyChatMessageKey measures deriving the per-message on-disk key
// (prefix + zero-padded sequence). This is the scoping arithmetic Append pays
// once per appended message to place it under — and only under — its session's
// lexicographic prefix; its cost is the price of key-level isolation on the
// write path.
func BenchmarkKeyChatMessageKey(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	var sink int
	for i := range b.N {
		sink += len(benchScopeKey.chatMessageKey(uint64(i)))
	}
	_ = sink
}

// BenchmarkRecent isolates the READ hydration path at a steady-state active
// window: it pre-fills the bounded window once, then measures Recent(key, 10)
// alone — the reverse, prefix-bounded iterator that folds the last N messages
// into the system prompt on every turn. Unlike the existing Append+Read cycle
// benchmark, this reports the read cost with NO write interleaved, so the
// hydration-read tax is visible on its own.
func BenchmarkRecent(b *testing.B) {
	ctx := context.Background()
	wm, err := OpenInMemory(defaultMaxActiveMessages, NoopSummarizer{})
	if err != nil {
		b.Fatalf("OpenInMemory: %v", err)
	}
	defer func() { _ = wm.Close() }()

	// Fill the active window to its steady-state size so the reverse iterator
	// has a realistic amount to walk (bounded by maxActiveMessages by design).
	for i := range defaultMaxActiveMessages {
		if err := wm.Append(ctx, benchScopeKey, Message{Role: "user", Content: fmt.Sprintf("seed message %d", i)}); err != nil {
			b.Fatalf("seed Append: %v", err)
		}
	}
	wm.Sync()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := wm.Recent(ctx, benchScopeKey, 10); err != nil {
			b.Fatalf("Recent: %v", err)
		}
	}
}

// BenchmarkSummary isolates the condensed-summary read — the single-key Get
// hydration also folds into the system prompt every turn. It is the cheapest
// memory read (one keyed lookup, no iteration) and is reported so the full
// hydration read cost (Recent + Summary) can be accounted for, not just the
// window scan.
func BenchmarkSummary(b *testing.B) {
	ctx := context.Background()
	wm, err := OpenInMemory(defaultMaxActiveMessages, NoopSummarizer{})
	if err != nil {
		b.Fatalf("OpenInMemory: %v", err)
	}
	defer func() { _ = wm.Close() }()

	// Seed a summary by overflowing the window once so the read hits a real
	// stored value rather than the empty (key-not-found) fast path.
	for i := range defaultMaxActiveMessages * 2 {
		if err := wm.Append(ctx, benchScopeKey, Message{Role: "user", Content: fmt.Sprintf("overflow %d", i)}); err != nil {
			b.Fatalf("seed Append: %v", err)
		}
	}
	wm.Sync()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := wm.Summary(ctx, benchScopeKey); err != nil {
			b.Fatalf("Summary: %v", err)
		}
	}
}
