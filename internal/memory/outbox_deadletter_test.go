package memory

import (
	"context"
	"testing"
	"time"
)

// The question: what happens to an outbox event the sink will NEVER accept?
//
// It used to be retried on every drain pass, forever. Not a leak of memory but a
// leak of work — a pending set that only grows, each pass paying for an event
// that cannot converge, with nothing anywhere saying so. The bound has to come
// with somewhere for the event to go: deleting it after N tries would trade an
// unbounded retry for silent data loss, which is the exact failure the outbox
// exists to prevent. So exhausted events are MOVED to a dead-letter prefix,
// countable and diagnosable.
//
// Found by scripts/robustness-audit.sh (BLOCKER OUTBOX_UNBOUNDED_RETRY).

// neverSucceeds is the attempt budget for a sink that will never accept the
// event: large enough that flakySink's failUntil is effectively unbounded.
const neverSucceeds = 1 << 30

func newOutboxTestWM(t *testing.T, maxAttempts int) *WorkingMemory {
	t.Helper()
	wm, err := OpenInMemory(0, NoopSummarizer{})
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() {
		if err := wm.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	wm.SetMaxOutboxAttempts(maxAttempts)
	return wm
}

func enqueueOne(t *testing.T, wm *WorkingMemory, id string) {
	t.Helper()
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	event := NewOutboxEvent(key, id, "something the tenant said", time.Now().UTC())
	if err := wm.AppendWithOutbox(context.Background(), key, Message{Role: "user", Content: "hi"}, event); err != nil {
		t.Fatalf("AppendWithOutbox: %v", err)
	}
}

func counts(t *testing.T, wm *WorkingMemory) (pending, dead int) {
	t.Helper()
	p, err := wm.PendingOutboxCount()
	if err != nil {
		t.Fatalf("PendingOutboxCount: %v", err)
	}
	d, err := wm.DeadOutboxCount()
	if err != nil {
		t.Fatalf("DeadOutboxCount: %v", err)
	}
	return p, d
}

// TestOutbox_ExhaustedEventIsParkedNotRetriedForever is the blocker itself.
func TestOutbox_ExhaustedEventIsParkedNotRetriedForever(t *testing.T) {
	wm := newOutboxTestWM(t, 3)
	enqueueOne(t, wm, "evt-1")
	sink := &flakySink{failUntil: neverSucceeds} // never succeeds

	for pass := 1; pass <= 3; pass++ {
		if _, err := wm.RecoverOutbox(context.Background(), sink); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
	}

	pending, dead := counts(t, wm)
	if pending != 0 {
		t.Errorf("pending = %d after exhausting the attempt budget, want 0", pending)
	}
	if dead != 1 {
		t.Errorf("dead-lettered = %d, want 1", dead)
	}

	// The bound has to actually stop the work. A fourth pass must not hand the
	// event to the sink again.
	before := sink.calls
	if _, err := wm.RecoverOutbox(context.Background(), sink); err != nil {
		t.Fatalf("post-park pass: %v", err)
	}
	if sink.calls != before {
		t.Errorf("a parked event was handed to the sink again (%d -> %d calls); "+
			"the retry is still unbounded", before, sink.calls)
	}
}

// TestOutbox_TransientFailuresStillConsolidate guards the other direction: the
// bound must not turn a retryable blip into a parked event. A fix that parked
// too eagerly would pass the test above and lose real consolidation here.
func TestOutbox_TransientFailuresStillConsolidate(t *testing.T) {
	wm := newOutboxTestWM(t, 5)
	enqueueOne(t, wm, "evt-2")
	sink := &flakySink{failUntil: 2} // fails twice, then succeeds

	for pass := 1; pass <= 3; pass++ {
		if _, err := wm.RecoverOutbox(context.Background(), sink); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
	}

	pending, dead := counts(t, wm)
	if pending != 0 || dead != 0 {
		t.Errorf("after a transient failure the event must consolidate and disappear; pending=%d dead=%d", pending, dead)
	}
}

// TestOutbox_AttemptCountIsPersisted: a bound that resets whenever the process
// restarts bounds nothing. The count lives in the stored event, so a re-read
// picks up where the previous run left off.
func TestOutbox_AttemptCountIsPersisted(t *testing.T) {
	wm := newOutboxTestWM(t, 4)
	enqueueOne(t, wm, "evt-3")
	sink := &flakySink{failUntil: neverSucceeds}

	for pass := 1; pass <= 2; pass++ {
		if _, err := wm.RecoverOutbox(context.Background(), sink); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
	}

	var seen OutboxEvent
	found := false
	if err := wm.forEachPendingOutbox(func(e OutboxEvent) { seen, found = e, true }); err != nil {
		t.Fatalf("scan pending: %v", err)
	}
	if !found {
		t.Fatal("the event should still be pending after 2 of 4 attempts")
	}
	if seen.Attempts != 2 {
		t.Errorf("persisted Attempts = %d, want 2 — an in-memory-only counter would read 0 here", seen.Attempts)
	}
	if seen.LastError == "" {
		t.Error("LastError is empty; a parked event would be undiagnosable without it")
	}
}

// TestOutbox_ParkedEventKeepsItsContent: parking is not deletion. The turn is
// still recoverable, which is the whole reason the event is moved rather than
// dropped.
func TestOutbox_ParkedEventKeepsItsContent(t *testing.T) {
	wm := newOutboxTestWM(t, 1)
	enqueueOne(t, wm, "evt-4")
	sink := &flakySink{failUntil: neverSucceeds}

	if _, err := wm.RecoverOutbox(context.Background(), sink); err != nil {
		t.Fatalf("drain: %v", err)
	}

	var dead OutboxEvent
	found := false
	if err := wm.forEachDeadOutbox(func(e OutboxEvent) { dead, found = e, true }); err != nil {
		t.Fatalf("scan dead: %v", err)
	}
	if !found {
		t.Fatal("the exhausted event was not parked — it was lost")
	}
	if dead.Content != "something the tenant said" {
		t.Errorf("parked content = %q, want the original turn text", dead.Content)
	}
	if dead.Tenant != "acme" || dead.User != "alice" {
		t.Errorf("parked event lost its identity: tenant=%q user=%q", dead.Tenant, dead.User)
	}
}
