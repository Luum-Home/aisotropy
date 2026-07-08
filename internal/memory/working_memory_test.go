package memory

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T, maxActive int) *WorkingMemory {
	t.Helper()
	wm, err := OpenInMemory(maxActive, NoopSummarizer{})
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() {
		if err := wm.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return wm
}

// TestAppendRecentRoundtrip verifies that messages appended to a session
// come back from Recent in the same order they were written.
func TestAppendRecentRoundtrip(t *testing.T) {
	ctx := context.Background()
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}

	want := []string{"hello", "how are you", "tell me about aisotropy", "thanks"}
	for _, content := range want {
		if err := wm.Append(ctx, key, Message{Role: "user", Content: content}); err != nil {
			t.Fatalf("Append(%q): %v", content, err)
		}
	}

	got, err := wm.Recent(ctx, key, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("Recent returned %d messages, want %d", len(got), len(want))
	}
	for i, m := range got {
		if m.Content != want[i] {
			t.Errorf("message %d = %q, want %q (order not preserved)", i, m.Content, want[i])
		}
		if m.Timestamp.IsZero() {
			t.Errorf("message %d has a zero Timestamp, want Append to fill it in", i)
		}
	}
}

// TestRecentLimitsToN verifies Recent never returns more than n messages,
// and returns the most recent ones when the buffer holds more than n.
func TestRecentLimitsToN(t *testing.T) {
	ctx := context.Background()
	// maxActive large enough that nothing gets pruned into the summary —
	// this test is specifically about Recent's own "n" parameter, not the
	// summary-buffer eviction (covered by TestSummaryBufferBoundsWindow).
	wm := newTestStore(t, 100)
	key := Key{Tenant: "acme", User: "bob", Session: "s1"}

	for i := 0; i < 5; i++ {
		if err := wm.Append(ctx, key, Message{Role: "user", Content: fmt.Sprintf("msg-%d", i)}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := wm.Recent(ctx, key, 2)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Recent(n=2) returned %d messages, want 2", len(got))
	}
	if got[0].Content != "msg-3" || got[1].Content != "msg-4" {
		t.Errorf("Recent(n=2) = [%q, %q], want [\"msg-3\", \"msg-4\"] (the two newest)",
			got[0].Content, got[1].Content)
	}
}

// TestTenantIsolation is the constitutional-gate test: two tenants (or two
// users, or two sessions) writing to the same physical BadgerDB instance
// must never observe each other's messages or summary, even when their
// other key components collide.
func TestTenantIsolation(t *testing.T) {
	ctx := context.Background()
	wm := newTestStore(t, 10)

	keyA := Key{Tenant: "tenant-a", User: "shared-user", Session: "shared-session"}
	keyB := Key{Tenant: "tenant-b", User: "shared-user", Session: "shared-session"}

	if err := wm.Append(ctx, keyA, Message{Role: "user", Content: "tenant A secret"}); err != nil {
		t.Fatalf("Append tenant A: %v", err)
	}
	if err := wm.Append(ctx, keyB, Message{Role: "user", Content: "tenant B secret"}); err != nil {
		t.Fatalf("Append tenant B: %v", err)
	}

	gotA, err := wm.Recent(ctx, keyA, 10)
	if err != nil {
		t.Fatalf("Recent tenant A: %v", err)
	}
	gotB, err := wm.Recent(ctx, keyB, 10)
	if err != nil {
		t.Fatalf("Recent tenant B: %v", err)
	}

	if len(gotA) != 1 || gotA[0].Content != "tenant A secret" {
		t.Fatalf("tenant A buffer = %+v, want exactly its own message", gotA)
	}
	if len(gotB) != 1 || gotB[0].Content != "tenant B secret" {
		t.Fatalf("tenant B buffer = %+v, want exactly its own message", gotB)
	}

	// Same assertion for the summary buffer once condensation kicks in.
	wmSmall := newTestStore(t, 1)
	if err := wmSmall.Append(ctx, keyA, Message{Role: "user", Content: "A-1"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := wmSmall.Append(ctx, keyA, Message{Role: "user", Content: "A-2"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := wmSmall.Append(ctx, keyB, Message{Role: "user", Content: "B-1"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Condensation now runs in a background goroutine (see
	// TestAsyncCondensationDoesNotBlockAppend); wait for any in-flight
	// condensation to finish before asserting on the summary buffer.
	wmSmall.wg.Wait()

	summaryA, err := wmSmall.Summary(ctx, keyA)
	if err != nil {
		t.Fatalf("Summary tenant A: %v", err)
	}
	summaryB, err := wmSmall.Summary(ctx, keyB)
	if err != nil {
		t.Fatalf("Summary tenant B: %v", err)
	}
	if summaryB != "" {
		t.Fatalf("tenant B summary = %q, want empty (only tenant A exceeded the window)", summaryB)
	}
	if summaryA == "" {
		t.Fatal("tenant A summary is empty, want a condensed session-state paragraph")
	}
}

// TestKeyValidateRejectsReservedCharacters verifies the injection-prevention
// gate: a key component cannot smuggle the ':' separator (or other reserved
// characters) to forge a collision with another tenant/user/session.
func TestKeyValidateRejectsReservedCharacters(t *testing.T) {
	cases := []Key{
		{Tenant: "", User: "u", Session: "s"},
		{Tenant: "t", User: "", Session: "s"},
		{Tenant: "t", User: "u", Session: ""},
		{Tenant: "tenant:evil", User: "u", Session: "s"},
		{Tenant: "t", User: "user:evil", Session: "s"},
		{Tenant: "t", User: "u", Session: "session:evil"},
	}
	for _, k := range cases {
		if err := k.Validate(); err == nil {
			t.Errorf("Key(%+v).Validate() = nil, want an error", k)
		}
	}

	valid := Key{Tenant: "t", User: "u", Session: "s"}
	if err := valid.Validate(); err != nil {
		t.Errorf("Key(%+v).Validate() = %v, want nil", valid, err)
	}
}

// TestSummaryBufferBoundsWindow verifies the core Phase 2 requirement: once
// the active buffer exceeds maxActiveMessages, it is pruned back down to at
// most maxActiveMessages and a non-empty summary exists — the active window
// never grows unbounded (see docs/architecture/memory-tiers.md -> "Token
// optimization — Summary Buffer"). Uses NoopSummarizer, so this requires no
// LLM credentials.
func TestSummaryBufferBoundsWindow(t *testing.T) {
	ctx := context.Background()
	const maxActive = 5
	wm := newTestStore(t, maxActive)
	key := Key{Tenant: "acme", User: "carol", Session: "s1"}

	const totalMessages = 20
	for i := 0; i < totalMessages; i++ {
		msg := Message{Role: "user", Content: fmt.Sprintf("turn %d", i)}
		if err := wm.Append(ctx, key, msg); err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
	}

	// Condensation runs in a background goroutine (see
	// TestAsyncCondensationDoesNotBlockAppend); Sync waits for it to converge
	// before asserting on the bounded window. Coalescing (the pending set)
	// guarantees the window ends within the max even for appends that land
	// while a condensation pass is already running.
	wm.Sync()

	got, err := wm.Recent(ctx, key, totalMessages)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) > maxActive {
		t.Fatalf("active buffer holds %d messages after %d appends, want <= %d (window must never grow unbounded)",
			len(got), totalMessages, maxActive)
	}

	// The newest messages must still be the ones retained.
	if got[len(got)-1].Content != fmt.Sprintf("turn %d", totalMessages-1) {
		t.Errorf("last retained message = %q, want the most recent turn", got[len(got)-1].Content)
	}

	summary, err := wm.Summary(ctx, key)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary == "" {
		t.Fatal("Summary is empty after exceeding the active window, want a condensed paragraph")
	}
}

// gatedSummarizer is a test-only Summarizer whose Condense call parks on a
// channel the test controls (release) instead of returning immediately. It
// signals started (closed exactly once, via sync.Once) the first time
// Condense is entered, so a test can deterministically observe "condensation
// has begun and is currently blocked" without measuring any elapsed time.
// Once the test closes release, every current and future call to Condense
// (condensePass can invoke it more than once per session — see
// condenseInBackground) unblocks immediately, since a receive on a closed
// channel never blocks.
type gatedSummarizer struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *gatedSummarizer) Condense(ctx context.Context, existingSummary string, oldest []Message) (string, error) {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return NoopSummarizer{}.Condense(ctx, existingSummary, oldest)
}

// TestAsyncCondensationDoesNotBlockAppend verifies the core Phase 2
// follow-up requirement (see docs/architecture/memory-tiers.md -> "Token
// optimization — Summary Buffer": "an asynchronous goroutine condenses the
// oldest ones"): Append must return without waiting on Summarizer.Condense,
// because condensation is handed off to a background goroutine rather than
// run inline.
//
// This is proven deterministically via channel synchronization, not a
// wall-clock bound: gatedSummarizer.Condense parks on a release channel that
// only this test controls, so it cannot possibly complete until the test
// closes it. The append loop below runs to completion — in program order,
// before this test ever touches gate.release — while gate.release is still
// open. If Append blocked on Condense, that loop could never return (the
// very first Append past the window would deadlock waiting on a Condense
// call that waits on a channel nothing has closed yet), so the loop
// finishing at all is already the proof. The explicit assertions below only
// add: (a) a bounded deadlock guard in case condensation was never triggered
// at all (which would otherwise make the test pass vacuously), and (b) an
// ordering check that Condense had in fact started — and is therefore
// necessarily still parked on the still-open release channel — strictly
// after the append loop's last call already returned.
func TestAsyncCondensationDoesNotBlockAppend(t *testing.T) {
	ctx := context.Background()
	const maxActive = 5
	const safetyTimeout = 5 * time.Second // deadlock guard only, never the assertion itself

	gate := &gatedSummarizer{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}

	wm, err := OpenInMemory(maxActive, gate)
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() {
		if err := wm.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	key := Key{Tenant: "acme", User: "dave", Session: "s1"}
	const totalMessages = 40

	// This loop must complete on its own: gate.release is not closed until
	// after it returns, so if Append waited on Condense this call would
	// never come back (and the test would fail on Go's own test timeout,
	// not on a duration comparison here).
	for i := 0; i < totalMessages; i++ {
		msg := Message{Role: "user", Content: fmt.Sprintf("turn %d", i)}
		if err := wm.Append(ctx, key, msg); err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
	}

	// The append loop above has already returned by program order. Now
	// confirm condensation was in fact triggered (otherwise the loop
	// finishing would prove nothing): wait for gate.started to be closed,
	// with safetyTimeout only guarding against a genuine deadlock/regression
	// — it is not what makes the test pass or fail under normal operation.
	select {
	case <-gate.started:
	case <-time.After(safetyTimeout):
		t.Fatal("Condense was never invoked (deadlock guard tripped) — background condensation did not start")
	}

	// gate.release is only ever closed by this test, and that has not
	// happened yet, so the Condense call we just confirmed started is
	// necessarily still parked inside its `case <-s.release` — it cannot
	// have returned. Since the append loop already completed before this
	// point, this is the deterministic proof that Append returned while
	// Condense was still blocked, i.e. Append does not wait on the
	// summarizer.
	close(gate.release)

	// Drain every in-flight background condensation before asserting on the
	// final, converged state.
	wm.Sync()

	got, err := wm.Recent(ctx, key, totalMessages)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) > maxActive {
		t.Fatalf("active buffer holds %d messages after background condensation drained, want <= %d (window must be bounded once drained)",
			len(got), maxActive)
	}
	if got[len(got)-1].Content != fmt.Sprintf("turn %d", totalMessages-1) {
		t.Errorf("last retained message = %q, want the most recent turn", got[len(got)-1].Content)
	}

	summary, err := wm.Summary(ctx, key)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary == "" {
		t.Fatal("Summary is empty after background condensation drained, want a condensed paragraph")
	}
}

// TestAppendRejectsInvalidKey verifies Append/Recent/Summary all validate
// their Key before touching BadgerDB.
func TestAppendRejectsInvalidKey(t *testing.T) {
	ctx := context.Background()
	wm := newTestStore(t, 10)
	bad := Key{Tenant: "t", User: "", Session: "s"}

	if err := wm.Append(ctx, bad, Message{Role: "user", Content: "x"}); err == nil {
		t.Error("Append with invalid key = nil error, want an error")
	}
	if _, err := wm.Recent(ctx, bad, 5); err == nil {
		t.Error("Recent with invalid key = nil error, want an error")
	}
	if _, err := wm.Summary(ctx, bad); err == nil {
		t.Error("Summary with invalid key = nil error, want an error")
	}
}
