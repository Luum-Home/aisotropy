package memory

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// defaultMaxActiveMessages is used when NewWorkingMemory is given a
// non-positive limit. It matches the "~10-15 messages" window described in
// docs/architecture/memory-tiers.md -> "Working memory".
const defaultMaxActiveMessages = 12

// WorkingMemory is the L1/working-memory tier of Aisotropy's memory
// hierarchy: the active chat buffer plus its condensed summary, backed by an
// embedded BadgerDB instance (see docs/components/badgerdb-working-memory.md).
//
// Tenant/user isolation is enforced entirely at the key level (see key.go):
// every operation validates its Key and reads/writes only under that
// session's prefix, so two tenants' buffers can never be mixed in the same
// iteration even though they share one physical BadgerDB instance.
//
// Working memory is wired into both chat endpoints (internal/api): each turn is
// persisted and the condensed Summary() plus Recent() are folded into the
// system prompt during hydration. The ChatInput contract carries
// tenant_id/user_id (path params) and session_id (body), which together key
// every entry (see memory.Key). On the streaming path the write goes through
// AppendWithOutbox so the same transaction also queues the consolidation event.
type WorkingMemory struct {
	db                *badger.DB
	maxActiveMessages int
	summarizer        Summarizer

	// mu guards inFlight and closed below. It is never held across a
	// BadgerDB transaction or a Summarizer.Condense call.
	mu       sync.Mutex
	inFlight map[string]struct{} // session prefixes with a condensation goroutine currently running
	pending  map[string]struct{} // session prefixes that got an Append while their condensation was in flight — coalesced into one more pass so the window can't exceed the max (see triggerCondensation)
	closed   bool

	// wg tracks every background condensation goroutine spawned by Append.
	// Close waits on it so shutdown is deterministic and leak-free.
	wg sync.WaitGroup

	// shutdownCtx/shutdownCancel give background condensation goroutines a
	// lifetime tied to the WorkingMemory itself rather than to whichever
	// request's context happened to trigger them — a request's context is
	// typically cancelled the moment its HTTP handler returns, long before a
	// background condensation has a chance to run. Close cancels this
	// context so any in-flight Summarizer.Condense call can abort promptly.
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc
}

// NewWorkingMemory builds a WorkingMemory over an already-open BadgerDB
// instance. maxActiveMessages bounds the active buffer window (N in the
// design doc); a non-positive value falls back to defaultMaxActiveMessages.
// A nil summarizer falls back to NoopSummarizer, so the store is usable
// without any LLM credentials.
//
// Close() on the returned WorkingMemory closes the underlying db — callers
// that need to keep the db open past the WorkingMemory's lifetime should
// manage Close() themselves and not call it here.
func NewWorkingMemory(db *badger.DB, maxActiveMessages int, summarizer Summarizer) *WorkingMemory {
	if maxActiveMessages <= 0 {
		maxActiveMessages = defaultMaxActiveMessages
	}
	if summarizer == nil {
		summarizer = NoopSummarizer{}
	}
	shutdownCtx, cancel := context.WithCancel(context.Background())
	return &WorkingMemory{
		db:                db,
		maxActiveMessages: maxActiveMessages,
		summarizer:        summarizer,
		inFlight:          make(map[string]struct{}),
		pending:           make(map[string]struct{}),
		shutdownCtx:       shutdownCtx,
		shutdownCancel:    cancel,
	}
}

// tuneForWorkingMemory adjusts BadgerDB's defaults — which are sized for
// large, high-throughput datasets — down to the footprint appropriate for
// this package's actual workload: a handful of small (sub-KB) JSON messages
// per session, capped at maxActiveMessages, per tenant/user/session. Left at
// their defaults, BadgerDB's LSM buffers and caches are tens to hundreds of
// megabytes each, dwarfing the few kilobytes of chat data actually being
// stored (see the benchmarks in working_memory_bench_test.go, which measured
// ~5-6MB allocated per Append before this tuning).
//
// DetectConflicts is turned off deliberately, not just for memory: Append's
// background condensation goroutine (see triggerCondensation) now runs its
// own read/write transactions concurrently with new Append calls on the same
// session prefix. BadgerDB's optimistic SSI conflict detection tracks entire
// iterated key ranges, so a condensation transaction that deletes old
// messages under a session's chat prefix races, at the detector level, with
// an Append transaction concurrently iterating that same prefix to count the
// active window — even though the two transactions never touch the same
// individual key. Without disabling detection, that manifests as spurious
// "Transaction Conflict" errors under load. This is safe here because
// WorkingMemory's own correctness never depends on BadgerDB's conflict
// detection: each session key has at most one writer goroutine invariant to
// preserve on the application side — Append callers are expected to append
// to a given tenant/user/session key sequentially (a chat session has one
// active turn at a time), and only one background condensation goroutine is
// ever in flight per session key (see the inFlight single-flight guard in
// triggerCondensation).
//
// ValueThreshold cannot simply be left at BadgerDB's 1MB default once
// BaseTableSize/MemTableSize are reduced below it — BadgerDB requires
// ValueThreshold to stay under the max batch size it derives from those, and
// refuses to open otherwise ("Valuethreshold ... greater than max batch
// size"). It also cannot be tuned too low: in InMemory mode (used by
// OpenInMemory, tests, and benchmarks) any value larger than ValueThreshold
// is rejected outright — there is no on-disk value log for it to spill into
// — and a condensed summary paragraph legitimately grows past a few hundred
// bytes after enough rounds of condensation. 64KB comfortably covers any
// realistic chat message or condensed summary for this workload while
// staying well under the smaller max batch size implied by BaseTableSize
// below.
func tuneForWorkingMemory(opts badger.Options) badger.Options {
	return opts.
		WithLogger(nil).                // BadgerDB's default logger is noisy on stdout; the
		WithNumVersionsToKeep(1).       // caller's slog-based logging is the source of truth for this service.
		WithValueLogFileSize(16 << 20). // 16MB vlog segments, not the 1GB default — this store never approaches that scale.
		WithValueThreshold(64 << 10).   // see the doc comment above: must stay below the reduced max batch size, but comfortably above realistic message/summary sizes.
		WithMemTableSize(4 << 20).      // 4MB memtables instead of 64MB — this is an MB-scale, in-process buffer, not a large dataset.
		WithNumMemtables(2).            // fewer resident memtables (default 5) since each is now much smaller anyway.
		WithNumLevelZeroTables(2).      // flush/compact sooner given the smaller memtables (default 5).
		WithNumLevelZeroTablesStall(4). // scale the stall threshold down alongside NumLevelZeroTables (default 15).
		WithBaseTableSize(1 << 20).     // 1MB SSTables instead of 2MB.
		WithBlockCacheSize(8 << 20).    // 8MB block cache instead of the 256MB default.
		WithIndexCacheSize(8 << 20).    // 8MB index cache instead of keeping all indexes permanently resident.
		WithDetectConflicts(false).     // see the doc comment above: required for the async condensation goroutine, safe under our single-writer-per-key usage.
		WithCompactL0OnClose(false)     // this process's data footprint is small enough that compaction on close buys nothing.
}

// Open opens (creating if necessary) a production, on-disk BadgerDB instance
// rooted at path and wraps it in a WorkingMemory. This is the persistent
// path referenced in docs/components/badgerdb-working-memory.md.
func Open(path string, maxActiveMessages int, summarizer Summarizer) (*WorkingMemory, error) {
	opts := tuneForWorkingMemory(badger.DefaultOptions(path))
	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("memory: open badger at %q: %w", path, err)
	}
	return NewWorkingMemory(db, maxActiveMessages, summarizer), nil
}

// OpenInMemory opens a BadgerDB instance with no on-disk footprint,
// intended for tests and benchmarks (see the component doc's
// "in-memory mode" note). Data does not survive process restart.
func OpenInMemory(maxActiveMessages int, summarizer Summarizer) (*WorkingMemory, error) {
	opts := tuneForWorkingMemory(badger.DefaultOptions("").WithInMemory(true))
	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("memory: open in-memory badger: %w", err)
	}
	return NewWorkingMemory(db, maxActiveMessages, summarizer), nil
}

// Close stops accepting new background condensations, cancels
// shutdownCtx (so any Summarizer.Condense call currently in flight can abort
// promptly), waits for every in-flight background condensation goroutine to
// finish, and only then closes the underlying BadgerDB instance. This makes
// shutdown deterministic: no goroutine started by Append can still be
// running — or leaked — once Close returns.
func (w *WorkingMemory) Close() error {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		w.shutdownCancel()
	}
	w.mu.Unlock()

	w.wg.Wait()
	return w.db.Close()
}

// Append records msg as the newest entry in key's active buffer and returns
// as soon as that write is durably persisted — it never blocks on
// summarization. If msg.Timestamp is zero, it is set to time.Now().UTC(). If
// the active buffer now exceeds the configured maxActiveMessages, a
// background goroutine condenses the oldest excess messages into the summary
// buffer (via the configured Summarizer) and removes them from the active
// buffer, keeping the window bounded — see
// docs/architecture/memory-tiers.md -> "Token optimization — Summary
// Buffer": "an asynchronous goroutine condenses the oldest ones". At most
// one such goroutine runs per session at a time (see triggerCondensation);
// Close waits for all of them to finish.
func (w *WorkingMemory) Append(ctx context.Context, key Key, msg Message) error {
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

		// activeKeys only reads keys (PrefetchValues=false), never the
		// stored JSON payloads, so counting the buffer costs nothing more
		// than a key-only scan even before anything is due for pruning.
		keys, err := activeKeys(txn, chatPrefix)
		if err != nil {
			return err
		}
		over = len(keys) > w.maxActiveMessages
		return nil
	})
	if err != nil {
		return fmt.Errorf("memory: append message: %w", err)
	}

	if over {
		// Condensation (and, for LLMSummarizer, a real LLM call) is handed
		// off to a background goroutine so Append never blocks the caller
		// on it — see triggerCondensation.
		w.triggerCondensation(key)
	}
	return nil
}

// triggerCondensation starts a background goroutine that condenses key's
// active buffer down to maxActiveMessages, unless one is already running for
// this key (single-flight per session) or the WorkingMemory has been
// closed. It is safe to call on every Append: it is a cheap, mutex-guarded
// no-op whenever a condensation for this key is already in flight.
func (w *WorkingMemory) triggerCondensation(key Key) {
	sessionKey := string(key.chatPrefix())

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	if _, busy := w.inFlight[sessionKey]; busy {
		// A condensation is already running for this session. Record that more
		// messages arrived so that goroutine runs one more pass after it
		// finishes; otherwise appends landing between its final check and its
		// cleanup would go un-condensed and the window could exceed the max.
		w.pending[sessionKey] = struct{}{}
		return
	}
	w.inFlight[sessionKey] = struct{}{}
	w.wg.Add(1)
	go w.condenseInBackground(key, sessionKey)
}

// condenseInBackground is the body of the single background goroutine
// allowed to run per session key at a time. It loops — recomputing the
// excess keys fresh on every iteration via a read-only transaction — until
// the active buffer is back within maxActiveMessages, so it never works from
// a stale snapshot even though more messages may keep arriving from
// concurrent Append calls while it runs. It always uses w.shutdownCtx
// instead of a caller-supplied context, since it must keep running after the
// Append call that spawned it has already returned.
func (w *WorkingMemory) condenseInBackground(key Key, sessionKey string) {
	defer w.wg.Done()

	for {
		w.condensePass(key)

		// Decide whether to run again. If an Append coalesced work while the
		// pass above was running, do one more pass; otherwise clear the
		// in-flight flag and stop. Both this check and triggerCondensation's
		// pending-set are done under mu, so no Append is lost: it either sets
		// pending (we re-loop and catch it) or, once we clear inFlight, spawns
		// a fresh goroutine. This closes the window where an Append landing
		// between a pass's final check and its cleanup would leave the buffer
		// over the max with no goroutine left to condense it.
		w.mu.Lock()
		if _, more := w.pending[sessionKey]; more {
			delete(w.pending, sessionKey)
			w.mu.Unlock()
			continue
		}
		delete(w.inFlight, sessionKey)
		w.mu.Unlock()
		return
	}
}

// condensePass prunes key's active buffer back within maxActiveMessages,
// recomputing the excess fresh on every iteration via a read-only transaction
// so it never works from a stale snapshot even though more messages may keep
// arriving. It always uses w.shutdownCtx (not a caller context), since it must
// keep running after the Append that triggered it has returned. Best-effort: a
// read error just ends the pass — the coalescing re-check in
// condenseInBackground (or a later Append) will re-trigger.
func (w *WorkingMemory) condensePass(key Key) {
	chatPrefix := key.chatPrefix()
	for {
		if w.shutdownCtx.Err() != nil {
			return
		}

		var excessKeys [][]byte
		err := w.db.View(func(txn *badger.Txn) error {
			keys, err := activeKeys(txn, chatPrefix)
			if err != nil {
				return err
			}
			if over := len(keys) - w.maxActiveMessages; over > 0 {
				excessKeys = keys[:over]
			}
			return nil
		})
		if err != nil || len(excessKeys) == 0 {
			return
		}

		if err := w.condenseAndPrune(w.shutdownCtx, key, excessKeys); err != nil {
			return
		}
	}
}

// Sync blocks until every background condensation triggered so far has
// finished — a quiescence point for callers (and tests) that need the active
// window to have converged after a burst of Appends. It does NOT close the
// store, and it is not a barrier against Appends made concurrently from other
// goroutines after Sync is entered.
func (w *WorkingMemory) Sync() { w.wg.Wait() }

// condenseAndPrune folds the messages under excessKeys (the oldest ones
// that no longer fit in the active window) into the summary buffer and
// deletes them from the active buffer. Only these — never the whole active
// buffer — are decoded from JSON, so the cost of pruning is proportional to
// how many messages fell out of the window, not to the window's size. It is
// only ever invoked from the single per-key background goroutine started by
// triggerCondensation, never directly from Append, and never while a
// BadgerDB transaction is open.
func (w *WorkingMemory) condenseAndPrune(ctx context.Context, key Key, excessKeys [][]byte) error {
	existing, err := w.Summary(ctx, key)
	if err != nil {
		return err
	}

	var oldest []Message
	err = w.db.View(func(txn *badger.Txn) error {
		msgs, err := decodeMessages(txn, excessKeys)
		if err != nil {
			return err
		}
		oldest = msgs
		return nil
	})
	if err != nil {
		return fmt.Errorf("memory: read messages pending condensation: %w", err)
	}

	newSummary, err := w.summarizer.Condense(ctx, existing, oldest)
	if err != nil {
		return fmt.Errorf("memory: condense summary: %w", err)
	}

	err = w.db.Update(func(txn *badger.Txn) error {
		if err := txn.Set(key.summaryKey(), []byte(newSummary)); err != nil {
			return err
		}
		for _, k := range excessKeys {
			if err := txn.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("memory: prune condensed messages: %w", err)
	}
	return nil
}

// endOfPrefixMarker is appended to a prefix to build a seek key guaranteed
// to sort after every real key under that prefix: chatMessageKey's sequence
// suffix is a run of ASCII digits ('0'-'9', 0x30-0x39), all of which sort
// below 0xFF.
var endOfPrefixMarker = []byte{0xFF}

// Recent returns up to the last n messages of key's active buffer, in
// chronological order (oldest first, newest last). If the buffer holds
// fewer than n messages, all of them are returned. Only the last n entries
// are ever read from BadgerDB (via a reverse, prefix-bounded iterator), so
// the cost does not depend on how large the active buffer has grown.
func (w *WorkingMemory) Recent(ctx context.Context, key Key, n int) ([]Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := key.Validate(); err != nil {
		return nil, err
	}
	if n <= 0 {
		return nil, nil
	}

	var reversed []Message
	err := w.db.View(func(txn *badger.Txn) error {
		chatPrefix := key.chatPrefix()

		opts := badger.DefaultIteratorOptions
		opts.Prefix = chatPrefix
		opts.Reverse = true

		it := txn.NewIterator(opts)
		defer it.Close()

		seekKey := append(append([]byte(nil), chatPrefix...), endOfPrefixMarker...)
		for it.Seek(seekKey); it.ValidForPrefix(chatPrefix) && len(reversed) < n; it.Next() {
			var msg Message
			if err := it.Item().Value(func(val []byte) error {
				return json.Unmarshal(val, &msg)
			}); err != nil {
				return fmt.Errorf("memory: decode stored message: %w", err)
			}
			reversed = append(reversed, msg)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("memory: read recent messages: %w", err)
	}

	out := make([]Message, len(reversed))
	for i, m := range reversed {
		out[len(reversed)-1-i] = m
	}
	return out, nil
}

// Summary returns key's condensed "session state" summary, or "" if no
// condensation has happened yet for this session.
func (w *WorkingMemory) Summary(ctx context.Context, key Key) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := key.Validate(); err != nil {
		return "", err
	}

	var summary string
	err := w.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key.summaryKey())
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			summary = string(val)
			return nil
		})
	})
	if err != nil {
		return "", fmt.Errorf("memory: read summary: %w", err)
	}
	return summary, nil
}

// activeKeys returns every message key currently under the active-buffer
// prefix, in ascending (chronological) order, without decoding any stored
// value (PrefetchValues=false). This is what lets Append cheaply learn
// "does this session now exceed the window" without paying the cost of
// JSON-decoding the whole active buffer on every single append.
func activeKeys(txn *badger.Txn, chatPrefix []byte) ([][]byte, error) {
	opts := badger.DefaultIteratorOptions
	opts.Prefix = chatPrefix
	opts.PrefetchValues = false

	it := txn.NewIterator(opts)
	defer it.Close()

	var out [][]byte
	for it.Seek(chatPrefix); it.ValidForPrefix(chatPrefix); it.Next() {
		out = append(out, append([]byte(nil), it.Item().Key()...))
	}
	return out, nil
}

// decodeMessages fetches and JSON-decodes the stored Message for each of
// the given keys, in the order given.
func decodeMessages(txn *badger.Txn, keys [][]byte) ([]Message, error) {
	out := make([]Message, len(keys))
	for i, k := range keys {
		item, err := txn.Get(k)
		if err != nil {
			return nil, fmt.Errorf("memory: read stored message at %q: %w", k, err)
		}
		if err := item.Value(func(val []byte) error {
			return json.Unmarshal(val, &out[i])
		}); err != nil {
			return nil, fmt.Errorf("memory: decode stored message at %q: %w", k, err)
		}
	}
	return out, nil
}

// nextSeq allocates the next monotonic sequence number for a session's
// active buffer, reading and rewriting seqKey within the caller's
// transaction so the read-increment-write is atomic with the message write
// it accompanies.
func nextSeq(txn *badger.Txn, seqKey []byte) (uint64, error) {
	var seq uint64
	item, err := txn.Get(seqKey)
	switch {
	case errors.Is(err, badger.ErrKeyNotFound):
		seq = 0
	case err != nil:
		return 0, err
	default:
		if err := item.Value(func(val []byte) error {
			if len(val) != 8 {
				return fmt.Errorf("memory: corrupt sequence value (want 8 bytes, got %d)", len(val))
			}
			seq = binary.BigEndian.Uint64(val)
			return nil
		}); err != nil {
			return 0, err
		}
	}

	next := seq + 1
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, next)
	if err := txn.Set(seqKey, buf); err != nil {
		return 0, err
	}
	return next, nil
}
