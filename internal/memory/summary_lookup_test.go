package memory

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/dgraph-io/badger/v4"
)

// TestLookupSummaryDistinguishesAbsentFromEmpty is the point of the accessor:
// Summary() collapses "this session has no summary yet" and "the summary is the
// empty string" into the same ("", nil), so a caller cannot tell a first turn
// from a store that lost the session. LookupSummary reports which one it is.
func TestLookupSummaryDistinguishesAbsentFromEmpty(t *testing.T) {
	ctx := context.Background()
	wm := newTestStore(t, 10)

	absent := Key{Tenant: "acme", User: "alice", Session: "never-seen"}
	got, found, err := wm.LookupSummary(ctx, absent)
	if err != nil {
		t.Fatalf("LookupSummary on an absent session: %v", err)
	}
	if found {
		t.Fatal("found=true for a session that was never written")
	}
	if got != "" {
		t.Fatalf("summary = %q, want empty for an absent session", got)
	}

	// A summary that was genuinely stored as the empty string must read back as
	// PRESENT — this is the case Summary() cannot express.
	empty := Key{Tenant: "acme", User: "alice", Session: "condensed-to-nothing"}
	if err := wm.db.Update(func(txn *badger.Txn) error {
		return txn.Set(empty.summaryKey(), []byte(""))
	}); err != nil {
		t.Fatalf("seed empty summary: %v", err)
	}
	got, found, err = wm.LookupSummary(ctx, empty)
	if err != nil {
		t.Fatalf("LookupSummary on a stored-empty summary: %v", err)
	}
	if !found {
		t.Fatal("found=false for a summary that IS stored (as the empty string) — absence and emptiness are still indistinguishable")
	}
	if got != "" {
		t.Fatalf("summary = %q, want the stored empty string", got)
	}
}

// TestLookupSummaryFindsStoredSummary covers the ordinary present case and the
// agreement between LookupSummary and the Summary wrapper.
func TestLookupSummaryFindsStoredSummary(t *testing.T) {
	ctx := context.Background()
	wm := newTestStore(t, 2)
	key := Key{Tenant: "acme", User: "bob", Session: "s1"}

	// Overflow the 2-message window so a real condensation writes a summary.
	for _, c := range []string{"one", "two", "three", "four"} {
		if err := wm.Append(ctx, key, Message{Role: "user", Content: c}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	wm.Sync()

	got, found, err := wm.LookupSummary(ctx, key)
	if err != nil {
		t.Fatalf("LookupSummary: %v", err)
	}
	if !found || got == "" {
		t.Fatalf("LookupSummary = (%q, %v), want a stored non-empty summary", got, found)
	}
	viaWrapper, err := wm.Summary(ctx, key)
	if err != nil || viaWrapper != got {
		t.Fatalf("Summary wrapper = (%q, %v), want it to agree with LookupSummary (%q)", viaWrapper, err, got)
	}
}

// TestSessionExistsSeparatesFirstTurnFromLostHistory is the other half: a
// caller that gets no summary and no recent messages can now ask whether the
// session was ever written at all. "Not yet" is legitimate; "it was, and it is
// gone" is a data-loss signal.
func TestSessionExistsSeparatesFirstTurnFromLostHistory(t *testing.T) {
	ctx := context.Background()
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "carol", Session: "s1"}

	exists, err := wm.SessionExists(ctx, key)
	if err != nil {
		t.Fatalf("SessionExists (first turn): %v", err)
	}
	if exists {
		t.Fatal("SessionExists=true before anything was ever appended")
	}

	if err := wm.Append(ctx, key, Message{Role: "user", Content: "hi"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	exists, err = wm.SessionExists(ctx, key)
	if err != nil {
		t.Fatalf("SessionExists (after append): %v", err)
	}
	if !exists {
		t.Fatal("SessionExists=false after a turn was appended — a known session reads as brand new")
	}

	// An erased subject is genuinely gone, counter included: erasure must not
	// leave a tombstone that later reads as "history expected but missing".
	if _, err := wm.EraseSubject(ctx, "acme", "carol"); err != nil {
		t.Fatalf("EraseSubject: %v", err)
	}
	exists, err = wm.SessionExists(ctx, key)
	if err != nil {
		t.Fatalf("SessionExists (after erase): %v", err)
	}
	if exists {
		t.Fatal("SessionExists=true after the subject was erased")
	}
}

// TestSessionExistsValidatesIdentity keeps the accessor on the blessed identity
// path: a malformed key is rejected rather than probed.
func TestSessionExistsValidatesIdentity(t *testing.T) {
	ctx := context.Background()
	wm := newTestStore(t, 10)
	if _, err := wm.SessionExists(ctx, Key{Tenant: "", User: "u", Session: "s"}); err == nil {
		t.Fatal("SessionExists accepted a blank tenant")
	}
	if _, _, err := wm.LookupSummary(ctx, Key{Tenant: "t", User: "", Session: "s"}); err == nil {
		t.Fatal("LookupSummary accepted a blank user")
	}
}

// TestReportEmptyHydrationSilentOnFirstTurn: a session nobody ever wrote to is
// the legitimate empty hydration. It must NOT be reported and must NOT warn,
// or every first turn in the fleet becomes noise and the real signal drowns.
func TestReportEmptyHydrationSilentOnFirstTurn(t *testing.T) {
	ctx := context.Background()
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "dave", Session: "brand-new"}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if wm.ReportEmptyHydration(ctx, key) {
		t.Fatal("a first turn was reported as anomalous")
	}
	if buf.Len() != 0 {
		t.Fatalf("first turn logged at WARN: %q", buf.String())
	}
}

// TestReportEmptyHydrationFlagsLostHistory is the bug this closes: the session
// WAS written (its sequence counter is there) but the messages and summary are
// gone. Before, hydration returned the same ("", nil, no messages) it returns
// for a first turn and the bot answered with no history and no complaint.
func TestReportEmptyHydrationFlagsLostHistory(t *testing.T) {
	ctx := context.Background()
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "erin", Session: "s1"}

	if err := wm.Append(ctx, key, Message{Role: "user", Content: "remember this"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// Simulate the loss: the session's content disappears while its sequence
	// counter survives — what a wrong/partial data directory looks like from the
	// reader's side.
	if err := wm.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(key.chatMessageKey(1))
	}); err != nil {
		t.Fatalf("simulate lost message: %v", err)
	}
	if msgs, err := wm.Recent(ctx, key, 10); err != nil || len(msgs) != 0 {
		t.Fatalf("precondition: Recent = (%d msgs, %v), want an empty buffer", len(msgs), err)
	}
	if _, found, err := wm.LookupSummary(ctx, key); err != nil || found {
		t.Fatalf("precondition: LookupSummary found=%v err=%v, want no stored summary", found, err)
	}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if !wm.ReportEmptyHydration(ctx, key) {
		t.Fatal("lost history reported as a normal first turn — the miss is still silent")
	}
	logged := buf.String()
	if !strings.Contains(logged, "session is known but hydrated EMPTY") {
		t.Fatalf("no WARN for lost history; log=%q", logged)
	}
	for _, want := range []string{"acme", "erin", "s1"} {
		if !strings.Contains(logged, want) {
			t.Errorf("WARN missing identity %q; log=%q", want, logged)
		}
	}
	if strings.Contains(logged, "remember this") {
		t.Error("WARN leaked message content")
	}
}

// TestLookupAccessorsRespectCancellation keeps the new accessors consistent with
// every other read in this package: a cancelled request is refused, not served
// from the store after the client is gone.
func TestLookupAccessorsRespectCancellation(t *testing.T) {
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, _, err := wm.LookupSummary(ctx, key); err == nil {
		t.Error("LookupSummary served a cancelled context")
	}
	if _, err := wm.SessionExists(ctx, key); err == nil {
		t.Error("SessionExists served a cancelled context")
	}
}

// TestReportEmptyHydrationProbeFailureIsNotSilentAndNotAnomalous: when the probe
// itself cannot run, the emptiness is UNEXPLAINED — it must be said out loud, but
// it must not be reported as data loss, because there is no evidence of any.
func TestReportEmptyHydrationProbeFailureIsNotSilentAndNotAnomalous(t *testing.T) {
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if wm.ReportEmptyHydration(ctx, key) {
		t.Error("a failed probe was reported as lost history — that is a claim the probe never made")
	}
	if !strings.Contains(buf.String(), "could not probe") {
		t.Errorf("a failed probe logged nothing; log=%q", buf.String())
	}
}

// TestLookupAccessorsSurfaceStoreErrors: a store-level failure must reach the
// caller as an error, never as "no summary" / "no session". Collapsing a broken
// store into those answers is precisely the silent miss this pair exists to end.
func TestLookupAccessorsSurfaceStoreErrors(t *testing.T) {
	ctx := context.Background()
	wm, err := OpenInMemory(10, NoopSummarizer{})
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	if err := wm.Append(ctx, key, Message{Role: "user", Content: "hi"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := wm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, found, err := wm.LookupSummary(ctx, key); err == nil {
		t.Errorf("LookupSummary over a closed store returned (found=%v, nil error)", found)
	}
	if exists, err := wm.SessionExists(ctx, key); err == nil {
		t.Errorf("SessionExists over a closed store returned (%v, nil error)", exists)
	}
}
