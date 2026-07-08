package memory

import (
	"context"
	"fmt"
	"testing"
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
