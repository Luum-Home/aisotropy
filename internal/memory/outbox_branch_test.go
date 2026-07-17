package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// TestAppendWithOutboxCorruptSeq covers AppendWithOutbox's transaction-error
// wrap: a corrupt sequence counter aborts the whole (message + outbox) commit,
// preserving the outbox invariant that neither is written on failure.
func TestAppendWithOutboxCorruptSeq(t *testing.T) {
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	if err := wm.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key.seqKey(), []byte{7}) // corrupt (1 byte)
	}); err != nil {
		t.Fatal(err)
	}
	event := NewOutboxEvent(key, "ref-1", "content", time.Time{})
	err := wm.AppendWithOutbox(context.Background(), key, Message{Role: "user", Content: "x"}, event)
	if err == nil || !strings.Contains(err.Error(), "append with outbox") {
		t.Fatalf("AppendWithOutbox with corrupt seq = %v, want a wrapped error", err)
	}
	// Nothing must have been enqueued.
	if n, _ := wm.PendingOutboxCount(); n != 0 {
		t.Fatalf("pending outbox = %d after a failed AppendWithOutbox, want 0", n)
	}
}

// TestAppendWithOutboxTriggersCondensation covers the over-window branch of
// AppendWithOutbox: appending past maxActiveMessages must kick off background
// condensation (the same path Append takes), which eventually writes a summary.
func TestAppendWithOutboxTriggersCondensation(t *testing.T) {
	wm := newTestStore(t, 2)
	ctx := context.Background()
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	for i := 0; i < 6; i++ {
		event := NewOutboxEvent(key, "ref", "c", time.Time{})
		if err := wm.AppendWithOutbox(ctx, key, Message{Role: "user", Content: "hello there"}, event); err != nil {
			t.Fatalf("AppendWithOutbox: %v", err)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if sum, _ := wm.Summary(ctx, key); strings.Contains(sum, "Session state") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("AppendWithOutbox over the window never triggered condensation")
}

// TestDrainOutboxOnceCorruptEvent covers drainOutboxOnce's decode-error branch:
// a pending event whose stored payload is corrupt must fail the drain pass
// (surfaced as a store error) rather than be silently dropped.
func TestDrainOutboxOnceCorruptEvent(t *testing.T) {
	wm := newTestStore(t, 10)
	// Write a value under the outbox prefix whose payload is not valid JSON.
	badKey := []byte(outboxPrefix + "acme:00000000000000000001:ref")
	if err := wm.db.Update(func(txn *badger.Txn) error {
		return txn.Set(badKey, []byte("{broken"))
	}); err != nil {
		t.Fatal(err)
	}
	sink := &countingSink{}
	if _, err := wm.RecoverOutbox(context.Background(), sink); err == nil || !strings.Contains(err.Error(), "decode outbox event") {
		t.Fatalf("drain over a corrupt event = %v, want a decode error", err)
	}
	if sink.calls != 0 {
		t.Fatalf("sink was called %d times despite a decode failure, want 0", sink.calls)
	}
}

// cancelAfterFirstSink cancels its stored context on the first Consolidate,
// leaving a second pending event so drainOutboxOnce's mid-loop ctx.Err() guard
// fires before the second event is processed.
type cancelAfterFirstSink struct {
	cancel context.CancelFunc
	calls  int
}

func (c *cancelAfterFirstSink) Consolidate(context.Context, OutboxEvent) error {
	c.calls++
	c.cancel()
	return nil
}

// TestDrainOutboxOnceCancelledMidLoop covers the mid-loop cancellation branch:
// with two pending events, a context cancelled after the first is drained must
// stop the pass and return the ctx error, leaving the second event pending.
func TestDrainOutboxOnceCancelledMidLoop(t *testing.T) {
	wm := newTestStore(t, 10)
	ctx, cancel := context.WithCancel(context.Background())
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	// Two events with distinct refs so both are enqueued.
	for i, ref := range []string{"ref-a", "ref-b"} {
		ev := NewOutboxEvent(key, ref, "c", time.Now().Add(time.Duration(i)*time.Millisecond))
		if err := wm.AppendWithOutbox(context.Background(), key, Message{Role: "user", Content: "c"}, ev); err != nil {
			t.Fatal(err)
		}
	}
	sink := &cancelAfterFirstSink{cancel: cancel}
	n, err := wm.drainOutboxOnce(ctx, sink)
	if err == nil {
		t.Fatal("drainOutboxOnce returned nil error after mid-loop cancellation, want ctx error")
	}
	if n != 1 {
		t.Fatalf("processed = %d, want 1 (only the first event before cancellation)", n)
	}
	if remaining, _ := wm.PendingOutboxCount(); remaining != 1 {
		t.Fatalf("pending after cancelled drain = %d, want 1 (second event untouched)", remaining)
	}
}

// TestRunOutboxWorkerReturnsOnPreCancelledCtx covers the loop-top ctx.Err()
// guard and the poll<=0 default: a worker started with an already-cancelled
// context and a non-positive poll returns promptly without draining.
func TestRunOutboxWorkerReturnsOnPreCancelledCtx(t *testing.T) {
	wm := newTestStore(t, 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		wm.RunOutboxWorker(ctx, &countingSink{}, 0) // poll<=0 -> default; ctx already done
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunOutboxWorker did not return on a pre-cancelled context")
	}
}

// TestRunOutboxWorkerBacksOffOnStoreError covers the err!=nil backoff branch of
// RunOutboxWorker: a corrupt pending event makes each drain pass fail, so the
// worker must keep running (backing off) rather than exit or spin, and stop
// cleanly on cancel.
func TestRunOutboxWorkerBacksOffOnStoreError(t *testing.T) {
	wm := newTestStore(t, 10)
	badKey := []byte(outboxPrefix + "acme:00000000000000000001:ref")
	if err := wm.db.Update(func(txn *badger.Txn) error {
		return txn.Set(badKey, []byte("{broken"))
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		wm.RunOutboxWorker(ctx, &countingSink{}, 5*time.Millisecond)
		close(done)
	}()
	// Let it hit the store-error backoff branch at least once.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunOutboxWorker did not stop after cancel while backing off")
	}
}
