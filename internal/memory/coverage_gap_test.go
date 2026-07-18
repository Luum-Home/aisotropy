package memory

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// This file closes the remaining statement-coverage gaps in the memory
// package's error-handling branches that the existing branch_error_test.go /
// outbox_branch_test.go / quota_branch_test.go suites do not reach. Each test
// documents exactly which defensive branch it drives and how, since several
// of these paths (a non-NotFound error from badger.Txn.Get, for instance) are
// not reachable through the public API at all — see the "uncoverable" note at
// the bottom of this file for the ones deliberately left untouched.

// --- language.go: ctx cancellation guards ----------------------------------

// TestGetSubjectLanguageCancelledContext covers GetSubjectLanguage's leading
// ctx.Err() guard: a cancelled caller context must short-circuit before any
// BadgerDB access.
func TestGetSubjectLanguageCancelledContext(t *testing.T) {
	wm := newTestStore(t, 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := wm.GetSubjectLanguage(ctx, "acme", "alice")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("GetSubjectLanguage(cancelled ctx) = %v, want context.Canceled", err)
	}
}

// TestPutSubjectLanguageCancelledContext is the write-side counterpart.
func TestPutSubjectLanguageCancelledContext(t *testing.T) {
	wm := newTestStore(t, 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := wm.PutSubjectLanguage(ctx, "acme", "alice", "es")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("PutSubjectLanguage(cancelled ctx) = %v, want context.Canceled", err)
	}
}

// TestGetSubjectLanguageClosedStore covers GetSubjectLanguage's outer
// db.View error-wrap branch: once the underlying store is closed, View
// itself fails (before the closure runs) and the wrapped error must surface.
func TestGetSubjectLanguageClosedStore(t *testing.T) {
	wm := newTestStore(t, 10)
	if err := wm.db.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, err := wm.GetSubjectLanguage(context.Background(), "acme", "alice")
	if err == nil || !strings.Contains(err.Error(), "get subject language") {
		t.Fatalf("GetSubjectLanguage over a closed store = %v, want a wrapped get error", err)
	}
}

// TestPutSubjectLanguageKeyTooLarge covers PutSubjectLanguage's outer
// db.Update error-wrap branch. subjectLangKey builds the on-disk key from the
// tenant/user components, so a tenant long enough to push the encoded key
// past BadgerDB's ~65000-byte key ceiling makes the inner txn.Set fail
// (ErrLargeKey), which must be wrapped and surfaced rather than swallowed.
func TestPutSubjectLanguageKeyTooLarge(t *testing.T) {
	wm := newTestStore(t, 10)
	longTenant := strings.Repeat("a", 100000)
	err := wm.PutSubjectLanguage(context.Background(), longTenant, "alice", "es")
	if err == nil || !strings.Contains(err.Error(), "put subject language") {
		t.Fatalf("PutSubjectLanguage with an oversized key = %v, want a wrapped put error", err)
	}
}

// --- outbox.go: encode / write failure branches ----------------------------

// TestEnqueueOutboxEncodeError covers enqueueOutbox's json.Marshal error
// branch directly. time.Time.MarshalJSON rejects a year outside [0,9999], so
// an OutboxEvent built with such a CreatedAt is a legitimate (not
// artificially forced) encoding failure.
func TestEnqueueOutboxEncodeError(t *testing.T) {
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	badEvent := OutboxEvent{
		ID:        "ref-1",
		Tenant:    key.Tenant,
		User:      key.User,
		Session:   key.Session,
		Content:   "c",
		CreatedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	err := wm.db.Update(func(txn *badger.Txn) error {
		return enqueueOutbox(txn, badEvent)
	})
	if err == nil || !strings.Contains(err.Error(), "encode outbox event") {
		t.Fatalf("enqueueOutbox with an unencodable event = %v, want an encode error", err)
	}
}

// TestAppendWithOutboxEventEncodeError drives the same encode failure through
// AppendWithOutbox's call site (the message itself is valid), covering the
// "if err := enqueueOutbox(txn, event); err != nil" propagation branch and
// confirming the whole transaction — including the otherwise-valid message
// write — is rolled back.
func TestAppendWithOutboxEventEncodeError(t *testing.T) {
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	badEvent := OutboxEvent{
		ID:        "ref-2",
		Tenant:    key.Tenant,
		User:      key.User,
		Session:   key.Session,
		Content:   "c",
		CreatedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	err := wm.AppendWithOutbox(context.Background(), key, Message{Role: "user", Content: "hi"}, badEvent)
	if err == nil || !strings.Contains(err.Error(), "encode outbox event") {
		t.Fatalf("AppendWithOutbox with an unencodable event = %v, want the encode error to surface", err)
	}
	if got, _ := wm.Recent(context.Background(), key, 10); len(got) != 0 {
		t.Fatalf("message buffer has %d entries after a rolled-back AppendWithOutbox, want 0", len(got))
	}
}

// TestAppendWithOutboxMessageEncodeError covers AppendWithOutbox's own
// json.Marshal(msg) branch (distinct from the outbox event's): a message
// whose Timestamp year is out of range fails to encode before the outbox
// event is ever reached.
func TestAppendWithOutboxMessageEncodeError(t *testing.T) {
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	badTime := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	event := NewOutboxEvent(key, "ref-3", "c", time.Now())
	err := wm.AppendWithOutbox(context.Background(), key, Message{Role: "user", Content: "hi", Timestamp: badTime}, event)
	if err == nil || !strings.Contains(err.Error(), "encode message") {
		t.Fatalf("AppendWithOutbox with an unencodable message = %v, want an encode-message error", err)
	}
	if n, _ := wm.PendingOutboxCount(); n != 0 {
		t.Fatalf("pending outbox = %d after a message-encode failure, want 0 (whole txn rolled back)", n)
	}
}

// TestAppendWithOutboxValueTooLarge covers AppendWithOutbox's txn.Set error
// branch. Unlike Append, AppendWithOutbox does not run the per-message
// truncation guard (capMessageContent), so content past BadgerDB's configured
// value-log segment size (16MB, see tuneForWorkingMemory) makes the message
// write itself fail.
func TestAppendWithOutboxValueTooLarge(t *testing.T) {
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	oversized := strings.Repeat("x", 20<<20)
	event := NewOutboxEvent(key, "ref-4", "c", time.Now())
	err := wm.AppendWithOutbox(context.Background(), key, Message{Role: "user", Content: oversized}, event)
	if err == nil || !strings.Contains(err.Error(), "append with outbox") {
		t.Fatalf("AppendWithOutbox with an oversized message = %v, want a wrapped append error", err)
	}
}

// TestDrainOutboxOnceDeleteFailsAfterSinkClosesStore covers drainOutboxOnce's
// delete-error branch (the "delete drained event" wrap): a sink that closes
// the underlying store as a side effect of a successful Consolidate call
// makes the subsequent txn.Delete of that same event fail, which must be
// surfaced rather than silently treated as "processed".
func TestDrainOutboxOnceDeleteFailsAfterSinkClosesStore(t *testing.T) {
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	event := NewOutboxEvent(key, "ref-5", "c", time.Now())
	if err := wm.AppendWithOutbox(context.Background(), key, Message{Role: "user", Content: "hi"}, event); err != nil {
		t.Fatal(err)
	}
	sink := &storeClosingSink{wm: wm}
	_, err := wm.drainOutboxOnce(context.Background(), sink)
	if err == nil || !strings.Contains(err.Error(), "delete drained event") {
		t.Fatalf("drainOutboxOnce with a store closed mid-drain = %v, want a wrapped delete error", err)
	}
	if sink.calls != 1 {
		t.Fatalf("sink called %d times, want exactly 1 (consolidate succeeded, delete then failed)", sink.calls)
	}
}

// storeClosingSink consolidates successfully but closes the WorkingMemory's
// store as a side effect, so the caller's subsequent delete of the drained
// event key fails against a closed BadgerDB.
type storeClosingSink struct {
	wm    *WorkingMemory
	calls int
}

func (s *storeClosingSink) Consolidate(context.Context, OutboxEvent) error {
	s.calls++
	return s.wm.db.Close()
}

// --- quota.go: bucket-encode failure ---------------------------------------

// TestAllowBucketEncodeError covers Allow's json.Marshal(b) error branch. A
// Limiter is constructed directly (in-package) with burst = NaN: min(NaN, x)
// is NaN per IEEE 754 (and Go's min builtin), so the computed token count
// propagates to a NaN bucket.Tokens, which json.Marshal legitimately rejects
// ("unsupported value: NaN") — this is not reachable via NewLimiter, which
// clamps burst >= 1, so it stands in for a corrupted/degenerate limiter
// configuration.
func TestAllowBucketEncodeError(t *testing.T) {
	wm := newTestStore(t, 10)
	l := &Limiter{db: wm.db, rate: 1, burst: math.NaN(), now: time.Now, locks: make(map[string]*sync.Mutex)}
	_, err := l.Allow(context.Background(), "acme")
	if err == nil || !strings.Contains(err.Error(), "quota check") {
		t.Fatalf("Allow with a NaN-burst limiter = %v, want a wrapped quota-check error", err)
	}
}

// --- working_memory.go: Append encode / write / erasure branches ----------

// TestAppendMessageEncodeError covers Append's json.Marshal(msg) branch
// (distinct from AppendWithOutbox's): an out-of-range Timestamp year fails to
// encode.
func TestAppendMessageEncodeError(t *testing.T) {
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	badTime := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	err := wm.Append(context.Background(), key, Message{Role: "user", Content: "hi", Timestamp: badTime})
	if err == nil || !strings.Contains(err.Error(), "encode message") {
		t.Fatalf("Append with an unencodable message = %v, want an encode-message error", err)
	}
}

// TestAppendValueTooLargeWithoutGuard covers Append's txn.Set error branch.
// capMessageContent normally truncates oversized content before the
// transaction (defaultMaxMessageValueBytes = 48KiB), so to reach the
// underlying BadgerDB size-limit error the guard itself is disabled
// (maxMessageValueBytes <= 0, its own documented "disables the guard" case)
// via the unexported field — legitimate in-package access, not a change to
// production behavior.
func TestAppendValueTooLargeWithoutGuard(t *testing.T) {
	wm := newTestStore(t, 10)
	wm.maxMessageValueBytes = 0
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	oversized := strings.Repeat("x", 20<<20)
	err := wm.Append(context.Background(), key, Message{Role: "user", Content: oversized})
	if err == nil || !strings.Contains(err.Error(), "append message") {
		t.Fatalf("Append with an oversized message and a disabled guard = %v, want a wrapped append error", err)
	}
}

// TestEraseSubjectCancelledContext covers EraseSubject's leading ctx.Err()
// guard.
func TestEraseSubjectCancelledContext(t *testing.T) {
	wm := newTestStore(t, 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := wm.EraseSubject(ctx, "acme", "alice")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("EraseSubject(cancelled ctx) = %v, want context.Canceled", err)
	}
}

// TestEraseSubjectClosedStore covers EraseSubject's scan-error wrap branch
// ("scan subject keys for erase"): the initial db.View fails outright once
// the store is closed.
func TestEraseSubjectClosedStore(t *testing.T) {
	wm := newTestStore(t, 10)
	if err := wm.db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := wm.EraseSubject(context.Background(), "acme", "alice")
	if err == nil || !strings.Contains(err.Error(), "scan subject keys for erase") {
		t.Fatalf("EraseSubject over a closed store = %v, want a wrapped scan error", err)
	}
}

// --- working_memory.go: Summary read-wrap and condensation write branches -

// TestSummaryClosedStore covers Summary's outer db.View error-wrap branch.
func TestSummaryClosedStore(t *testing.T) {
	wm := newTestStore(t, 10)
	if err := wm.db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := wm.Summary(context.Background(), Key{Tenant: "acme", User: "alice", Session: "s1"})
	if err == nil || !strings.Contains(err.Error(), "read summary") {
		t.Fatalf("Summary over a closed store = %v, want a wrapped read error", err)
	}
}

// hugeSummarizer returns a summary well past BadgerDB's configured
// value-log segment size (16MB, see tuneForWorkingMemory), so persisting it
// legitimately fails the underlying txn.Set — not an artificial fault.
type hugeSummarizer struct{}

func (hugeSummarizer) Condense(context.Context, string, []Message) (string, error) {
	return strings.Repeat("y", 20<<20), nil
}

// TestCondenseAndPruneSummaryTooLarge covers condenseAndPrune's
// txn.Set(summaryKey, ...) error branch: a summarizer that returns an
// oversized paragraph fails to persist, and the failure must propagate
// (wrapped) rather than silently drop the pending prune.
func TestCondenseAndPruneSummaryTooLarge(t *testing.T) {
	wm, err := OpenInMemory(2, hugeSummarizer{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wm.Close() })

	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	if err := wm.Append(context.Background(), key, Message{Role: "user", Content: "hi"}); err != nil {
		t.Fatal(err)
	}

	err2 := wm.condenseAndPrune(context.Background(), key, [][]byte{key.chatMessageKey(1)})
	if err2 == nil || !strings.Contains(err2.Error(), "prune condensed messages") {
		t.Fatalf("condenseAndPrune with an oversized summary = %v, want a wrapped prune error", err2)
	}
	// The excess key must survive the aborted prune — never lose a message on
	// a failed condensation, mirroring TestCondenseAndPruneDecodeError's
	// invariant in branch_error_test.go.
	if err := wm.db.View(func(txn *badger.Txn) error {
		_, err := txn.Get(key.chatMessageKey(1))
		return err
	}); err != nil {
		t.Fatalf("excess key was pruned despite a failed condense-and-set: %v", err)
	}
}

// --- documented uncoverable branches ---------------------------------------
//
// The following defensive branches remain uncovered after this file, and are
// not reachable through the public API (verified experimentally, not just
// asserted):
//
//   - language.go GetSubjectLanguage / working_memory.go Summary / nextSeq /
//     quota.go load: the "non-ErrKeyNotFound error from txn.Get" branches.
//     BadgerDB's Txn.Get only ever returns nil or ErrKeyNotFound for a
//     well-formed key in a live, non-discarded transaction — Get performs no
//     key-size validation (confirmed: a 200000-byte key returns
//     ErrKeyNotFound, not a size error, unlike Set/Delete/txn.Set which do
//     validate and fail). Forcing any other Get error would require
//     corrupting the on-disk value log or discarding the transaction from
//     inside code this package does not control.
//   - outbox.go AppendWithOutbox / working_memory.go Append / condensePass:
//     the "if err != nil" checks following activeKeys(txn, prefix) calls.
//     activeKeys (working_memory.go) unconditionally returns a nil error on
//     every code path — the check is defensive against a future change to
//     that helper's signature, not a reachable branch today.
//   - summarizer.go boundNoopSummary's "keep <= 0" branch: keep is computed
//     from two untyped constants (maxNoopSummaryLen - len(noopElisionMarker))
//     and does not depend on the input string at all, so its sign is fixed
//     at compile time (currently positive) — no input can flip it.
//   - working_memory.go OpenInMemory's badger.Open error branch: OpenInMemory
//     always applies the same fixed, internally-consistent option set
//     (tuneForWorkingMemory + WithInMemory(true)); there is no parameter this
//     package exposes that can make that fixed configuration fail to open.
//   - working_memory.go condenseAndPrune's txn.Delete(k) error branch inside
//     the excess-keys loop: reaching it needs a key that both (a) already has
//     a value stored (so decodeMessages succeeds first) and (b) is too large
//     for Delete to accept — but Set enforces the identical size ceiling, so
//     no key can ever satisfy both at once through the store's own write path.
