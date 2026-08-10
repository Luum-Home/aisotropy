package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// This file implements the Transactional Outbox (docs/architecture/consistency.md),
// the consistency keystone. BadgerDB is not only working memory: it is also the
// durable event queue. On the hot path a chat message and its "intent to
// consolidate" are written in ONE ACID transaction (AppendWithOutbox), so a
// crash can never leave a message without its pending event or vice versa.
// Off the hot path a worker (RunOutboxWorker) drains pending events into the
// long-term sinks (facts + semantic) with at-least-once delivery and
// exponential backoff, deleting an event only once the sink confirms success.
// Orphaned events left by a crash are drained on the next start — the same code
// path, no special recovery mode (RecoverOutbox).

const outboxPrefix = "outbox:pending:"

// outboxDeadPrefix holds events that exhausted their attempts. They are MOVED
// here, never deleted: an event the sink could not consolidate is still a real
// turn a tenant said, and discarding it would lose data quietly — the one
// outcome the outbox exists to prevent. Parked events stop consuming a drain
// pass and become visible via DeadOutboxCount.
const outboxDeadPrefix = "outbox:dead:"

const (
	outboxInitialBackoff = 200 * time.Millisecond
	outboxMaxBackoff     = 30 * time.Second
	outboxDefaultPoll    = 1 * time.Second
	// defaultMaxOutboxAttempts bounds retries per event. Without a bound, an
	// event the sink can never consolidate — malformed content, a payload the
	// extractor rejects, a permanently absent tier — is retried on every pass
	// forever: unbounded work that never converges, and a pending set that only
	// grows. At the default 1s poll, 10 attempts is roughly ten seconds of
	// transient-failure tolerance before the event is parked, while the worker's
	// own backoff covers longer store-level outages.
	defaultMaxOutboxAttempts = 10
)

// OutboxEvent is a pending consolidation intent. It carries the text to
// consolidate (rather than only a reference) so a worker can process it even
// after the source message has been pruned from the active buffer, and so the
// event is self-contained for the facts + semantic sinks.
type OutboxEvent struct {
	// ID is a stable event id: enqueueing the same turn twice writes the same
	// key, so the event layer itself does not duplicate (and the sinks are
	// idempotent on top of that).
	ID      string `json:"id"`
	Tenant  string `json:"tenant"`
	User    string `json:"user"`
	Session string `json:"session"`
	// Content is the turn text to consolidate (typically the user prompt) —
	// enough for the facts extractor and the embedder.
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	// Attempts counts how many times a sink has been asked to consolidate this
	// event and refused. It is persisted on each failure so the count survives a
	// restart — a bound that resets on every crash bounds nothing. Events written
	// before this field existed decode as 0, which is the correct starting point.
	Attempts int `json:"attempts,omitempty"`
	// LastError is the most recent sink failure, kept so a parked event can be
	// diagnosed without reproducing the outage that parked it. It holds the error
	// text only; the event's Content is already alongside it, and no new tenant
	// data is introduced by this field.
	LastError string `json:"last_error,omitempty"`
}

// NewOutboxEvent builds an event for a turn. The id is derived from the message
// reference so re-enqueueing the same turn is idempotent at the event layer.
func NewOutboxEvent(key Key, messageRef, content string, at time.Time) OutboxEvent {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return OutboxEvent{
		ID:        messageRef,
		Tenant:    key.Tenant,
		User:      key.User,
		Session:   key.Session,
		Content:   content,
		CreatedAt: at.UTC(),
	}
}

// outboxKey encodes the pending-event key: outbox:pending:{tenant}:{ts}:{id}.
// tenant leads so a per-tenant scan is a prefix scan; the nanosecond timestamp
// orders events roughly by arrival.
func outboxKey(e OutboxEvent) []byte {
	return fmt.Appendf(nil, "%s%s:%020d:%s", outboxPrefix, e.Tenant, e.CreatedAt.UnixNano(), e.ID)
}

// deadOutboxKey mirrors outboxKey under the dead-letter prefix, so a parked
// event keeps its tenant grouping and creation ordering and can be located by
// the same (tenant, time, id) coordinates it had while pending.
func deadOutboxKey(e OutboxEvent) []byte {
	return fmt.Appendf(nil, "%s%s:%020d:%s", outboxDeadPrefix, e.Tenant, e.CreatedAt.UnixNano(), e.ID)
}

// enqueueOutbox writes the event within the caller's transaction, so it commits
// atomically with whatever else the transaction writes.
func enqueueOutbox(txn *badger.Txn, e OutboxEvent) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("memory: encode outbox event: %w", err)
	}
	return txn.Set(outboxKey(e), payload)
}

// AppendWithOutbox appends msg AND records event in the SAME BadgerDB
// transaction — the Phase 1 local-atomicity guarantee (consistency.md): either
// both are durable or neither is, even on a crash mid-commit. It mirrors
// Append's message write and adds the outbox event before the transaction
// commits.
func (w *WorkingMemory) AppendWithOutbox(ctx context.Context, key Key, msg Message, event OutboxEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := key.Validate(); err != nil {
		return err
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}

	chatPrefix := key.chatPrefix()
	seqKey := key.seqKey()

	over := false
	err := w.db.Update(func(txn *badger.Txn) error {
		seq, err := nextSeq(txn, seqKey)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("memory: encode message: %w", err)
		}
		if err := txn.Set(key.chatMessageKey(seq), payload); err != nil {
			return err
		}
		// Same transaction: the intent to consolidate. This is the whole point
		// of the outbox — it cannot be split from the message write.
		if err := enqueueOutbox(txn, event); err != nil {
			return err
		}
		keys, err := activeKeys(txn, chatPrefix)
		if err != nil {
			return err
		}
		over = len(keys) > w.maxActiveMessages
		return nil
	})
	if err != nil {
		return fmt.Errorf("memory: append with outbox: %w", err)
	}
	if over {
		w.triggerCondensation(key)
	}
	return nil
}

// ConsolidationSink consolidates one drained event into the long-term sinks
// (facts + semantic). It is implemented in the wiring layer (cmd/api) so the
// memory package need not import facts/semantic. It MUST be idempotent:
// delivery is at-least-once, so the same event can be handed to it more than
// once (a retry after a partial failure, or a cold-boot re-drain).
type ConsolidationSink interface {
	Consolidate(ctx context.Context, e OutboxEvent) error
}

// PendingOutboxCount returns how many pending events remain — for tests,
// observability, and backpressure signals.
func (w *WorkingMemory) PendingOutboxCount() (int, error) {
	n := 0
	err := w.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		prefix := []byte(outboxPrefix)
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			n++
		}
		return nil
	})
	return n, err
}

// RecoverOutbox drains every currently-pending event once through sink. Called
// on startup it is the cold-boot reconciliation: any event still present is, by
// definition, an orphan from a previous shutdown, and it is drained by the
// exact same path as steady-state work. Returns the number consolidated.
func (w *WorkingMemory) RecoverOutbox(ctx context.Context, sink ConsolidationSink) (int, error) {
	return w.drainOutboxOnce(ctx, sink)
}

// RunOutboxWorker drains pending events until ctx is cancelled. On an empty
// queue it waits poll; on a sink failure it backs off exponentially (capped)
// and leaves the event in place — at-least-once, no silent discard. The caller
// owns the goroutine's lifetime: cancel ctx before WorkingMemory.Close.
func (w *WorkingMemory) RunOutboxWorker(ctx context.Context, sink ConsolidationSink, poll time.Duration) {
	if poll <= 0 {
		poll = outboxDefaultPoll
	}
	backoff := outboxInitialBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		processed, err := w.drainOutboxOnce(ctx, sink)

		var wait time.Duration
		switch {
		case err != nil:
			// A store error (not a per-event sink failure): back off.
			wait = backoff
			backoff = minDuration(backoff*2, outboxMaxBackoff)
		case processed == 0:
			// Nothing to do (or every event's sink is still failing): poll.
			wait = poll
			backoff = outboxInitialBackoff
		default:
			// Made progress: loop immediately to drain the rest.
			backoff = outboxInitialBackoff
			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// drainOutboxOnce processes every currently-pending event once: consolidate via
// sink, and delete the key only on success. An event whose sink call fails is
// left in place for a later attempt (at-least-once). Returns how many were
// consolidated and deleted this pass.
func (w *WorkingMemory) drainOutboxOnce(ctx context.Context, sink ConsolidationSink) (int, error) {
	type pending struct {
		key   []byte
		event OutboxEvent
	}
	var pend []pending
	err := w.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		prefix := []byte(outboxPrefix)
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			k := item.KeyCopy(nil)
			var e OutboxEvent
			if err := item.Value(func(v []byte) error { return json.Unmarshal(v, &e) }); err != nil {
				return fmt.Errorf("memory: decode outbox event: %w", err)
			}
			pend = append(pend, pending{key: k, event: e})
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	processed := 0
	for _, p := range pend {
		if ctx.Err() != nil {
			return processed, ctx.Err()
		}
		if err := sink.Consolidate(ctx, p.event); err != nil {
			// Leave the event for a later attempt; do not fail the whole pass —
			// other events may still be consolidable. But COUNT the attempt, and
			// park the event once it has exhausted its budget: retrying an event
			// the sink will never accept is unbounded work that never converges.
			if perr := w.recordOutboxFailure(p.key, p.event, err); perr != nil {
				return processed, perr
			}
			continue
		}
		if err := w.db.Update(func(txn *badger.Txn) error { return txn.Delete(p.key) }); err != nil {
			return processed, fmt.Errorf("memory: delete drained event: %w", err)
		}
		processed++
	}
	return processed, nil
}

// recordOutboxFailure persists one failed delivery attempt for event e. Below
// the attempt budget it rewrites the event in place with the incremented count;
// at or above it, the event is MOVED to the dead-letter prefix in a single
// transaction — written there before (and atomically with) its removal from
// pending, so no crash window can drop it.
func (w *WorkingMemory) recordOutboxFailure(key []byte, e OutboxEvent, cause error) error {
	e.Attempts++
	e.LastError = cause.Error()

	maxAttempts := w.maxOutboxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxOutboxAttempts
	}

	if e.Attempts < maxAttempts {
		return w.db.Update(func(txn *badger.Txn) error {
			payload, err := json.Marshal(e)
			if err != nil {
				return fmt.Errorf("memory: encode outbox event: %w", err)
			}
			return txn.Set(key, payload)
		})
	}

	err := w.db.Update(func(txn *badger.Txn) error {
		payload, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("memory: encode dead outbox event: %w", err)
		}
		if err := txn.Set(deadOutboxKey(e), payload); err != nil {
			return err
		}
		return txn.Delete(key)
	})
	if err != nil {
		return fmt.Errorf("memory: park exhausted outbox event: %w", err)
	}
	// Loud, and with the identity needed to find it — but never the content,
	// which is tenant data and already persisted where it belongs.
	slog.Error("memory: outbox event parked after exhausting its attempts",
		"event_id", e.ID,
		"tenant", e.Tenant,
		"user", e.User,
		"session", e.Session,
		"attempts", e.Attempts,
		"last_error", e.LastError,
	)
	return nil
}

// DeadOutboxCount returns how many events have been parked after exhausting
// their delivery attempts. A non-zero value is consolidation the tenant did not
// get: it belongs on a dashboard, not in a log nobody reads. Mirrors
// PendingOutboxCount.
func (w *WorkingMemory) DeadOutboxCount() (int, error) {
	n := 0
	err := w.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		prefix := []byte(outboxDeadPrefix)
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			n++
		}
		return nil
	})
	return n, err
}

// forEachOutbox decodes every event under prefix and hands it to fn. Separate
// from the *OutboxCount helpers on purpose: those skip value prefetch because
// they only count, while this one pays for the decode.
func (w *WorkingMemory) forEachOutbox(prefix string, fn func(OutboxEvent)) error {
	return w.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		p := []byte(prefix)
		for it.Seek(p); it.ValidForPrefix(p); it.Next() {
			var e OutboxEvent
			if err := it.Item().Value(func(v []byte) error { return json.Unmarshal(v, &e) }); err != nil {
				return fmt.Errorf("memory: decode outbox event: %w", err)
			}
			fn(e)
		}
		return nil
	})
}

func (w *WorkingMemory) forEachPendingOutbox(fn func(OutboxEvent)) error {
	return w.forEachOutbox(outboxPrefix, fn)
}

func (w *WorkingMemory) forEachDeadOutbox(fn func(OutboxEvent)) error {
	return w.forEachOutbox(outboxDeadPrefix, fn)
}

// RequeueDeadOutbox moves every parked event back to the pending prefix with a
// fresh attempt budget, and returns how many were requeued.
//
// This is the other half of parking, and without it the dead-letter prefix is a
// place data goes to be preserved and never used again — which is only a slower
// kind of loss. The operator's actual situation is "the sink was misconfigured
// for an hour, fix it, replay what piled up", and that needs one call rather
// than a hand-written BadgerDB script.
//
// Deliberately MANUAL. Nothing calls this on a timer, because an automatic
// requeue would restore the unbounded retry through the back door: an event the
// sink can never accept would cycle pending → dead → pending forever, and the
// bound would exist only on paper. Requeueing is a decision that something
// changed, and only an operator knows that.
//
// Attempts and LastError are cleared: the count answers "how many tries since
// someone last decided this was worth retrying", and a stale count would park
// the event again on its first failure.
func (w *WorkingMemory) RequeueDeadOutbox(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var parked []OutboxEvent
	if err := w.forEachDeadOutbox(func(e OutboxEvent) { parked = append(parked, e) }); err != nil {
		return 0, err
	}

	requeued := 0
	for _, e := range parked {
		if err := ctx.Err(); err != nil {
			return requeued, err
		}
		dead := deadOutboxKey(e)
		e.Attempts = 0
		e.LastError = ""
		// One transaction per event: the pending write and the dead delete commit
		// together, so an interrupted requeue leaves each event in exactly one of
		// the two prefixes — never in both (a duplicate) and never in neither.
		if err := w.db.Update(func(txn *badger.Txn) error {
			if err := enqueueOutbox(txn, e); err != nil {
				return err
			}
			return txn.Delete(dead)
		}); err != nil {
			return requeued, fmt.Errorf("memory: requeue dead outbox event %q: %w", e.ID, err)
		}
		requeued++
	}
	if requeued > 0 {
		slog.InfoContext(ctx, "memory: dead-letter outbox events requeued", "count", requeued)
	}
	return requeued, nil
}

// SetMaxOutboxAttempts overrides how many failed deliveries an event tolerates
// before it is parked. Non-positive restores the default.
func (w *WorkingMemory) SetMaxOutboxAttempts(n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.maxOutboxAttempts = n
}

// minDuration returns the smaller of a and b.
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
