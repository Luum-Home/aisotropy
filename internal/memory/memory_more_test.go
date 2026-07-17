package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestNewLimiterClampsInputs covers the two guard branches of NewLimiter: a
// negative rate is clamped to 0 (no refill) and a sub-1 burst is clamped to 1
// (at least one token available).
func TestNewLimiterClampsInputs(t *testing.T) {
	w, err := OpenInMemory(0, NoopSummarizer{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	// burst < 1 clamps to 1: exactly one request is allowed before denial, and
	// rate < 0 clamps to 0 so there is no refill.
	l := w.NewLimiter(-5, 0)
	frozen := time.Now()
	l.now = func() time.Time { return frozen }
	ctx := context.Background()

	if ok, err := l.Allow(ctx, "acme"); err != nil || !ok {
		t.Fatalf("first Allow = (%v,%v), want (true,nil) — burst clamped to 1", ok, err)
	}
	if ok, _ := l.Allow(ctx, "acme"); ok {
		t.Fatal("second Allow granted; burst should be exactly 1 and rate 0 (no refill)")
	}
	// Even after time passes, rate 0 means no refill.
	frozen = frozen.Add(time.Hour)
	if ok, _ := l.Allow(ctx, "acme"); ok {
		t.Fatal("Allow granted after an hour; rate clamped to 0 must never refill")
	}
}

// erroringSummarizer always fails, so the condensation background path hits
// condenseAndPrune's summarizer-error branch. A failed condensation must NOT
// prune messages — losing chat history on a transient summarizer failure would
// be a data-loss bug.
type erroringSummarizer struct{}

func (erroringSummarizer) Condense(context.Context, string, []Message) (string, error) {
	return "", errors.New("summarizer unavailable")
}

func TestCondensationFailureDoesNotDropMessages(t *testing.T) {
	wm, err := OpenInMemory(2, erroringSummarizer{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wm.Close() })
	ctx := context.Background()
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}

	const n = 6
	for i := 0; i < n; i++ {
		if err := wm.Append(ctx, key, Message{Role: "user", Content: "keep me"}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	// Give the background condensation goroutine time to run (and fail).
	time.Sleep(200 * time.Millisecond)

	// A failing summarizer must leave the buffer intact: no summary written,
	// and all messages still retrievable.
	sum, err := wm.Summary(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if sum != "" {
		t.Fatalf("summary = %q, want empty — a failed condensation must not write one", sum)
	}
	got, err := wm.Recent(ctx, key, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("Recent returned %d messages, want %d — a failed condensation must not prune", len(got), n)
	}
}

func TestMinDuration(t *testing.T) {
	if got := minDuration(2*time.Second, 5*time.Second); got != 2*time.Second {
		t.Fatalf("minDuration(2s,5s) = %v, want 2s", got)
	}
	if got := minDuration(9*time.Second, 3*time.Second); got != 3*time.Second {
		t.Fatalf("minDuration(9s,3s) = %v, want 3s", got)
	}
	if got := minDuration(4*time.Second, 4*time.Second); got != 4*time.Second {
		t.Fatalf("minDuration(4s,4s) = %v, want 4s", got)
	}
}

// TestNewOutboxEventDefaultsTimestamp covers the at.IsZero() branch: a zero
// timestamp is replaced with a non-zero "now", and the id/tenant/user/session
// are copied from the key.
func TestNewOutboxEventDefaultsTimestamp(t *testing.T) {
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	e := NewOutboxEvent(key, "msg-ref-1", "hello world", time.Time{})
	if e.CreatedAt.IsZero() {
		t.Fatal("NewOutboxEvent left a zero CreatedAt, want it filled with now")
	}
	if e.ID != "msg-ref-1" || e.Tenant != "acme" || e.User != "alice" || e.Session != "s1" {
		t.Fatalf("event identity fields not copied from key: %+v", e)
	}
	if e.Content != "hello world" {
		t.Fatalf("event content = %q, want %q", e.Content, "hello world")
	}

	// A supplied timestamp is preserved (normalized to UTC).
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	e2 := NewOutboxEvent(key, "ref2", "c", at)
	if !e2.CreatedAt.Equal(at) {
		t.Fatalf("CreatedAt = %v, want the supplied %v", e2.CreatedAt, at)
	}
}

// TestAppendWithOutboxAtomicity verifies the message AND its outbox event are
// both durable after AppendWithOutbox: Recent returns the message and the
// pending outbox count reflects the enqueued event.
func TestAppendWithOutboxAtomicity(t *testing.T) {
	wm := newTestStore(t, 10)
	ctx := context.Background()
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	msg := Message{Role: "user", Content: "consolidate me"}
	event := NewOutboxEvent(key, "ref-1", msg.Content, time.Time{})

	if err := wm.AppendWithOutbox(ctx, key, msg, event); err != nil {
		t.Fatalf("AppendWithOutbox: %v", err)
	}

	got, err := wm.Recent(ctx, key, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "consolidate me" {
		t.Fatalf("Recent = %+v, want the appended message", got)
	}
	n, err := wm.PendingOutboxCount()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pending outbox count = %d, want 1 (event enqueued atomically)", n)
	}
}

// TestAppendWithOutboxRejectsInvalidKey exercises the key-validation and
// cancelled-context guard branches of AppendWithOutbox.
func TestAppendWithOutboxRejectsInvalidKey(t *testing.T) {
	wm := newTestStore(t, 10)
	badKey := Key{Tenant: "", User: "alice", Session: "s1"} // empty tenant
	msg := Message{Role: "user", Content: "x"}
	if err := wm.AppendWithOutbox(context.Background(), badKey, msg, OutboxEvent{}); err == nil {
		t.Fatal("AppendWithOutbox with an invalid key = nil, want validation error")
	}

	goodKey := Key{Tenant: "acme", User: "alice", Session: "s1"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := wm.AppendWithOutbox(ctx, goodKey, msg, OutboxEvent{}); err == nil {
		t.Fatal("AppendWithOutbox with a cancelled context = nil, want ctx error")
	}
}

// countingSink counts Consolidate calls and can fail a configurable number of
// times before succeeding, exercising the outbox worker's retry/leave-in-place
// behavior.
type countingSink struct {
	calls int
}

func (c *countingSink) Consolidate(context.Context, OutboxEvent) error {
	c.calls++
	return nil
}

// TestRunOutboxWorkerDrainsAndStops runs the worker against a live queue: it
// consolidates the pending event, deletes it, then the worker exits on ctx
// cancel. This exercises the drain-progress and cancellation branches.
func TestRunOutboxWorkerDrainsAndStops(t *testing.T) {
	wm := newTestStore(t, 10)
	ctx := context.Background()
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	event := NewOutboxEvent(key, "ref-1", "content", time.Time{})
	if err := wm.AppendWithOutbox(ctx, key, Message{Role: "user", Content: "content"}, event); err != nil {
		t.Fatal(err)
	}

	sink := &countingSink{}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		wm.RunOutboxWorker(runCtx, sink, 5*time.Millisecond)
		close(done)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		n, err := wm.PendingOutboxCount()
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n, _ := wm.PendingOutboxCount(); n != 0 {
		t.Fatalf("pending after worker run = %d, want 0 (drained)", n)
	}
	if sink.calls == 0 {
		t.Fatal("sink was never called")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop after ctx cancel")
	}
}

// TestRunOutboxWorkerLeavesEventOnSinkFailure verifies a failing sink leaves
// the event pending (at-least-once), then a later success drains it.
func TestRunOutboxWorkerLeavesEventOnSinkFailure(t *testing.T) {
	wm := newTestStore(t, 10)
	ctx := context.Background()
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	event := NewOutboxEvent(key, "ref-1", "content", time.Time{})
	if err := wm.AppendWithOutbox(ctx, key, Message{Role: "user", Content: "content"}, event); err != nil {
		t.Fatal(err)
	}

	sink := &flakySink{failUntil: 2}
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go wm.RunOutboxWorker(runCtx, sink, 5*time.Millisecond)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if n, _ := wm.PendingOutboxCount(); n == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("event was never drained despite the sink eventually succeeding")
}

// flakySink fails its first failUntil calls, then succeeds.
type flakySink struct {
	calls     int
	failUntil int
}

func (f *flakySink) Consolidate(context.Context, OutboxEvent) error {
	f.calls++
	if f.calls <= f.failUntil {
		return errors.New("sink temporarily down")
	}
	return nil
}

// TestRecoverOutboxDrainsOrphans covers RecoverOutbox: a pending event left
// from a prior run is consolidated on the cold-boot reconciliation path.
func TestRecoverOutboxDrainsOrphans(t *testing.T) {
	wm := newTestStore(t, 10)
	ctx := context.Background()
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	event := NewOutboxEvent(key, "ref-1", "content", time.Time{})
	if err := wm.AppendWithOutbox(ctx, key, Message{Role: "user", Content: "content"}, event); err != nil {
		t.Fatal(err)
	}

	sink := &countingSink{}
	n, err := wm.RecoverOutbox(ctx, sink)
	if err != nil {
		t.Fatalf("RecoverOutbox: %v", err)
	}
	if n != 1 {
		t.Fatalf("recovered %d events, want 1", n)
	}
	if remaining, _ := wm.PendingOutboxCount(); remaining != 0 {
		t.Fatalf("pending after recover = %d, want 0", remaining)
	}
}

// TestSummaryContainsNoopMarkerNotAsserted is a guard that our helpers compile
// and the NoopSummarizer path still produces a non-error summary through the
// full Append pipeline (light end-to-end smoke over the condensation branch).
func TestNoopCondensationEndToEnd(t *testing.T) {
	wm := newTestStore(t, 2)
	ctx := context.Background()
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	for i := 0; i < 6; i++ {
		if err := wm.Append(ctx, key, Message{Role: "user", Content: "hello there"}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sum, _ := wm.Summary(ctx, key)
		if strings.Contains(sum, "Session state") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("NoopSummarizer condensation never produced a summary")
}
