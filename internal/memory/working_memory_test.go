package memory

import (
	"context"
	"fmt"
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

	// Condensation now runs in a background goroutine (see
	// TestAsyncCondensationDoesNotBlockAppend); wait for any in-flight
	// condensation to finish before asserting on the bounded window.
	wm.wg.Wait()

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

// delayedSummarizer wraps NoopSummarizer with an artificial delay, standing
// in for a real (LLM-backed) Summarizer's network latency. It exists so
// tests can prove Append does not block on Summarizer.Condense.
type delayedSummarizer struct {
	delay time.Duration
}

func (s delayedSummarizer) Condense(ctx context.Context, existingSummary string, oldest []Message) (string, error) {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return NoopSummarizer{}.Condense(ctx, existingSummary, oldest)
}

// TestAsyncCondensationDoesNotBlockAppend verifies the core Phase 2
// follow-up requirement (see docs/architecture/memory-tiers.md -> "Token
// optimization — Summary Buffer": "an asynchronous goroutine condenses the
// oldest ones"): Append must return quickly even when the configured
// Summarizer is slow, because condensation is handed off to a background
// goroutine rather than run inline. Close (or, as here, waiting on the
// internal WaitGroup) must still observe a fully condensed, bounded active
// buffer once every background condensation has drained.
func TestAsyncCondensationDoesNotBlockAppend(t *testing.T) {
	ctx := context.Background()
	const maxActive = 5
	const delay = 15 * time.Millisecond

	wm, err := OpenInMemory(maxActive, delayedSummarizer{delay: delay})
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

	// A synchronous implementation would spend at least
	// (totalMessages-maxActive) * delay inside this loop, since every
	// append past the window triggers one condensation. The asynchronous
	// implementation triggers at most one background goroutine per session
	// at a time and never waits on it from Append, so the whole loop should
	// finish in a small fraction of that.
	start := time.Now()
	for i := 0; i < totalMessages; i++ {
		msg := Message{Role: "user", Content: fmt.Sprintf("turn %d", i)}
		if err := wm.Append(ctx, key, msg); err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
	}
	elapsed := time.Since(start)

	syncLowerBound := (totalMessages - maxActive) * delay
	if maxWait := syncLowerBound / 3; elapsed > maxWait {
		t.Fatalf("Append loop took %v across %d messages with a %v summarizer delay, want well under %v — "+
			"condensation must run in a background goroutine, not block Append", elapsed, totalMessages, delay, maxWait)
	}

	// Drain every in-flight background condensation before asserting on the
	// final, converged state.
	wm.wg.Wait()

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
