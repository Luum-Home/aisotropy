package memory

import (
	"context"
	"encoding/json"
	"fmt"
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

const (
	outboxInitialBackoff = 200 * time.Millisecond
	outboxMaxBackoff     = 30 * time.Second
	outboxDefaultPoll    = 1 * time.Second
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
			// other events may still be consolidable.
			continue
		}
		if err := w.db.Update(func(txn *badger.Txn) error { return txn.Delete(p.key) }); err != nil {
			return processed, fmt.Errorf("memory: delete drained event: %w", err)
		}
		processed++
	}
	return processed, nil
}

// minDuration returns the smaller of a and b.
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
