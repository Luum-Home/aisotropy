package memory

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// recordingSink is an idempotent ConsolidationSink for tests: it records how
// many times each event id was consolidated, and can be told to fail its first
// failUntil calls to exercise at-least-once retry.
type recordingSink struct {
	mu        sync.Mutex
	calls     int
	failUntil int
	ids       map[string]int
}

func newSink() *recordingSink { return &recordingSink{ids: map[string]int{}} }

func (s *recordingSink) Consolidate(_ context.Context, e OutboxEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.calls <= s.failUntil {
		return errors.New("sink temporarily down")
	}
	s.ids[e.ID]++
	return nil
}

func (s *recordingSink) distinct() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ids)
}

func appendTurn(t *testing.T, w *WorkingMemory, key Key, ref, content string) {
	t.Helper()
	ev := NewOutboxEvent(key, ref, content, time.Now().UTC())
	if err := w.AppendWithOutbox(context.Background(), key, Message{Role: "user", Content: content}, ev); err != nil {
		t.Fatalf("AppendWithOutbox: %v", err)
	}
}

// TestAppendWithOutboxSameTxn is the Phase 5 DoD local-atomicity test: the
// message and its pending event are written together.
func TestAppendWithOutboxSameTxn(t *testing.T) {
	w, err := OpenInMemory(0, NoopSummarizer{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	appendTurn(t, w, key, "m1", "remember the deploy runbook")

	// The message landed in working memory...
	recent, err := w.Recent(context.Background(), key, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].Content != "remember the deploy runbook" {
		t.Fatalf("message not persisted: %+v", recent)
	}
	// ...and so did exactly one pending consolidation event.
	n, err := w.PendingOutboxCount()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pending outbox = %d, want 1 (event must be written with the message)", n)
	}
}

// TestOutboxDrainDeletesOnSuccess: a successful sink consolidates and clears.
func TestOutboxDrainDeletesOnSuccess(t *testing.T) {
	w, err := OpenInMemory(0, NoopSummarizer{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	appendTurn(t, w, key, "m1", "fact one")
	appendTurn(t, w, key, "m2", "fact two")

	sink := newSink()
	got, err := w.RecoverOutbox(context.Background(), sink)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("consolidated %d, want 2", got)
	}
	if n, _ := w.PendingOutboxCount(); n != 0 {
		t.Fatalf("pending = %d, want 0 after successful drain", n)
	}
	if sink.distinct() != 2 {
		t.Fatalf("sink saw %d distinct events, want 2", sink.distinct())
	}
}

// TestOutboxAtLeastOnceOnFailure is the "no data loss" half of the DoD chaos
// test: a failing sink must NOT drop the event; a later attempt drains it.
func TestOutboxAtLeastOnceOnFailure(t *testing.T) {
	w, err := OpenInMemory(0, NoopSummarizer{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	appendTurn(t, w, key, "m1", "durable fact")

	sink := &recordingSink{ids: map[string]int{}, failUntil: 1} // first call fails
	got, err := w.drainOutboxOnce(context.Background(), sink)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("consolidated %d on a failing sink, want 0", got)
	}
	if n, _ := w.PendingOutboxCount(); n != 1 {
		t.Fatalf("pending = %d after failure, want 1 (event must survive)", n)
	}
	// The sink recovers; the event is still there and now drains.
	got, err = w.drainOutboxOnce(context.Background(), sink)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("consolidated %d on retry, want 1", got)
	}
	if n, _ := w.PendingOutboxCount(); n != 0 {
		t.Fatalf("pending = %d after retry, want 0", n)
	}
}

// TestOutboxCrashSurvivalAndColdBoot is the durability + cold-boot half of the
// DoD: a file-backed store keeps pending events across a process restart, and a
// fresh process drains the orphans via the same path (RecoverOutbox).
func TestOutboxCrashSurvivalAndColdBoot(t *testing.T) {
	dir := t.TempDir()
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}

	// Process 1: write a turn, then "crash" (Close) before any drain.
	w1, err := Open(dir, 0, NoopSummarizer{})
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	appendTurn(t, w1, key, "m1", "survive the restart")
	if n, _ := w1.PendingOutboxCount(); n != 1 {
		t.Fatalf("pre-crash pending = %d, want 1", n)
	}
	if err := w1.Close(); err != nil {
		t.Fatalf("close 1: %v", err)
	}

	// Process 2: cold boot on the same data dir. The event is an orphan.
	w2, err := Open(dir, 0, NoopSummarizer{})
	if err != nil {
		t.Fatalf("open 2: %v", err)
	}
	t.Cleanup(func() { _ = w2.Close() })
	if n, _ := w2.PendingOutboxCount(); n != 1 {
		t.Fatalf("orphan pending after restart = %d, want 1 (event must survive the crash)", n)
	}
	sink := newSink()
	got, err := w2.RecoverOutbox(context.Background(), sink)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 || sink.distinct() != 1 {
		t.Fatalf("cold-boot recovery: consolidated %d / distinct %d, want 1/1", got, sink.distinct())
	}
	if n, _ := w2.PendingOutboxCount(); n != 0 {
		t.Fatalf("pending after cold-boot drain = %d, want 0", n)
	}
}

// TestOutboxWorkerDrainsInBackground exercises RunOutboxWorker end to end.
func TestOutboxWorkerDrainsInBackground(t *testing.T) {
	w, err := OpenInMemory(0, NoopSummarizer{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	appendTurn(t, w, key, "m1", "background fact")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := newSink()
	go w.RunOutboxWorker(ctx, sink, 20*time.Millisecond)

	// Poll for the worker to drain (bounded, no fixed sleep).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if n, _ := w.PendingOutboxCount(); n == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n, _ := w.PendingOutboxCount(); n != 0 {
		t.Fatalf("worker did not drain: pending = %d", n)
	}
	if sink.distinct() != 1 {
		t.Fatalf("sink distinct = %d, want 1", sink.distinct())
	}
}

func TestOutboxKeyDeterministicPerTenant(t *testing.T) {
	at := time.Unix(0, 1_700_000_000_000_000_000).UTC()
	e := OutboxEvent{ID: "m1", Tenant: "acme", CreatedAt: at}
	k1 := string(outboxKey(e))
	k2 := string(outboxKey(e))
	if k1 != k2 {
		t.Fatal("outboxKey not deterministic")
	}
	// Different tenant -> different prefix (per-tenant scan isolation).
	e2 := e
	e2.Tenant = "globex"
	if string(outboxKey(e2)) == k1 {
		t.Fatal("outbox keys collide across tenants")
	}
}
