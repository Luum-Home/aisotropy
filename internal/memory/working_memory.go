package memory

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dgraph-io/badger/v4"
)

// defaultMaxActiveMessages is used when NewWorkingMemory is given a
// non-positive limit. It matches the "~10-15 messages" window described in
// docs/architecture/memory-tiers.md -> "Working memory".
const defaultMaxActiveMessages = 12

// defaultMaxMessageValueBytes caps the byte length of a single message's
// Content BEFORE it is JSON-encoded and handed to BadgerDB. It is a
// conservative guard (48 KiB) sitting well under BadgerDB's ~64 KiB per-value
// ceiling — the ValueThreshold configured in tuneForWorkingMemory, above which
// an InMemory-mode Set is rejected outright and an on-disk Set spills to the
// value log. A model that echoes a large hydrated prompt back can produce a
// Content field past that ceiling; without this guard the Append transaction
// FAILS and (because Append's error is only logged, never surfaced) the turn is
// silently dropped — silent data loss. Capping the value truncates the tail
// instead, so the write always succeeds.
//
// HONEST SCOPE: this is a truncation GUARD, not the smart fix. It kills the
// silent failure by bounding the value, at the cost of dropping the tail of a
// pathologically large message. The better follow-on is summarize-before-persist
// (condense an oversized message rather than cut it) — tracked as future work in
// docs/architecture/memory-tiers.md; this guard is the safe floor beneath it.
const defaultMaxMessageValueBytes = 48 << 10 // 48 KiB

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
	// maxMessageValueBytes caps a single message's Content byte length before
	// it is persisted (see capMessageContent + defaultMaxMessageValueBytes). It
	// is a configurable field so a deployment with a different BadgerDB
	// ValueThreshold can tune it; NewWorkingMemory seeds it with the
	// conservative default.
	maxMessageValueBytes int
	summarizer           Summarizer

	// mu guards inFlight, pending, closed, and closeGrace below. It is never
	// held across a BadgerDB transaction or a Summarizer.Condense call.
	mu       sync.Mutex
	inFlight map[string]struct{} // session prefixes with a condensation goroutine currently running
	pending  map[string]struct{} // session prefixes that got an Append while their condensation was in flight — coalesced into one more pass so the window can't exceed the max (see triggerCondensation)
	closed   bool
	// closeGrace bounds how long Close waits for an in-flight condensation to
	// finish before cancelling it (see defaultCloseCondensationGrace and
	// SetCloseGrace).
	closeGrace time.Duration

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
		db:                   db,
		maxActiveMessages:    maxActiveMessages,
		maxMessageValueBytes: defaultMaxMessageValueBytes,
		summarizer:           summarizer,
		inFlight:             make(map[string]struct{}),
		pending:              make(map[string]struct{}),
		closeGrace:           defaultCloseCondensationGrace,
		shutdownCtx:          shutdownCtx,
		shutdownCancel:       cancel,
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

// OpenEncrypted opens (creating if necessary) a production, on-disk BadgerDB
// instance rooted at path with AES encryption AT REST enabled, and wraps it in
// a WorkingMemory. encryptionKey must be 16, 24, or 32 bytes (AES-128, AES-192,
// or AES-256); BadgerDB encrypts every SSTable and value-log entry with it, so
// the active chat buffer and its condensed summary are never written to disk in
// plaintext. Reopening the same directory later requires the same key —
// BadgerDB refuses to open an encrypted store with a wrong or absent key.
//
// The caller owns supplying the key from a secret manager / environment and
// MUST never log it; a wrong-length key is rejected here rather than deferred
// to BadgerDB. Encryption additionally requires a non-zero index cache, which
// tuneForWorkingMemory already configures.
func OpenEncrypted(path string, maxActiveMessages int, summarizer Summarizer, encryptionKey []byte) (*WorkingMemory, error) {
	switch len(encryptionKey) {
	case 16, 24, 32:
	default:
		return nil, fmt.Errorf("memory: encryption key must be 16, 24, or 32 bytes for AES-128/192/256, got %d", len(encryptionKey))
	}
	opts := tuneForWorkingMemory(badger.DefaultOptions(path)).WithEncryptionKey(encryptionKey)
	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("memory: open encrypted badger at %q: %w", path, err)
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

// defaultCloseCondensationGrace bounds how long Close waits for an in-flight
// condensation to COMPLETE before it cancels shutdownCtx and forces the
// goroutines to abort.
//
// Why a grace period exists at all (see
// docs/architecture/arena-memory-forensics.md §4): Close used to cancel
// shutdownCtx FIRST and only then wait on wg. A condensation in flight at that
// moment aborts inside Summarizer.Condense, condenseAndPrune returns early, and
// NEITHER the new summary NOR the prune of the evicted messages lands — the
// session is left over its active-message cap with no corresponding summary,
// and whether that happens depends purely on wall-clock timing between the
// background goroutine and the caller's shutdown. That non-determinism is a
// real state-consistency defect for any consumer using on-disk working memory,
// and it made arena memory tasks flip between pass and fail across runs.
//
// Why the wait is BOUNDED: Condense is a real model call (LLMSummarizer) and a
// hung or slow provider must not block process shutdown forever. 5s is chosen
// as the tradeoff point — comfortably longer than a normal condensation call
// (single-paragraph completions land well inside a second or two on the
// providers this ships against) yet short enough to sit under the shutdown
// budgets typical of container orchestrators, which commonly send SIGKILL 10s
// after SIGTERM. Past the deadline the old behavior applies: cancel, let the
// goroutines abort, and accept the un-applied condensation rather than hang.
const defaultCloseCondensationGrace = 5 * time.Second

// SetCloseGrace overrides the bounded grace period Close gives an in-flight
// condensation to finish before cancelling it (see
// defaultCloseCondensationGrace). A non-positive duration means "do not wait" —
// cancel immediately, which is the pre-fix behavior. It exists so a deployment
// with a tighter shutdown budget can shorten the wait, and so tests can drive
// both sides of the deadline without sleeping for seconds. Call it before
// Close; it has no effect once Close has begun.
func (w *WorkingMemory) SetCloseGrace(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closeGrace = d
}

// Close stops accepting new background condensations, gives any condensation
// already in flight a BOUNDED grace period to finish and durably apply its
// summary + prune, then cancels shutdownCtx (aborting anything still running),
// waits for every background goroutine to exit, and only then closes the
// underlying BadgerDB instance. This makes shutdown deterministic in both
// directions: a normal-speed condensation always lands, and a hung one can
// never block shutdown past the grace deadline. No goroutine started by Append
// can still be running — or leaked — once Close returns.
func (w *WorkingMemory) Close() error {
	w.mu.Lock()
	alreadyClosed := w.closed
	w.closed = true
	grace := w.closeGrace
	w.mu.Unlock()

	if !alreadyClosed {
		// closed=true above already stops triggerCondensation from spawning any
		// NEW goroutine, so this only ever waits on work that was already in
		// flight — the wait cannot be extended by fresh arrivals.
		if grace > 0 {
			done := make(chan struct{})
			go func() {
				w.wg.Wait()
				close(done)
			}()
			timer := time.NewTimer(grace)
			select {
			case <-done:
			case <-timer.C:
				slog.Warn("working memory: condensation still in flight after close grace period; cancelling",
					"grace", grace)
			}
			timer.Stop()
		}
		w.shutdownCancel()
	}

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

	// Per-value size guard (done BEFORE the transaction so it logs once, never
	// per retry): a message whose Content exceeds the cap is truncated on a rune
	// boundary so the encoded value stays comfortably under BadgerDB's per-value
	// ceiling. Without this an oversized value makes the Set below FAIL, and
	// because Append's error is only logged upstream the whole turn would be
	// silently dropped. Truncating keeps the write successful; we WARN (identity
	// + sizes only, never the content) so the truncation is observable, not
	// silent.
	if capped, over, truncated := capMessageContent(msg.Content, w.maxMessageValueBytes); truncated {
		msg.Content = capped
		slog.Warn("working memory: message content truncated to per-value cap before persist",
			"tenant", key.Tenant,
			"user", key.User,
			"session", key.Session,
			"cap_bytes", w.maxMessageValueBytes,
			"over_bytes", over,
		)
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

// eraseBatchSize bounds how many keys one erase transaction deletes, keeping a
// subject erase comfortably under BadgerDB's per-transaction batch limits even
// for a subject with many sessions.
const eraseBatchSize = 1000

// EraseSubject deletes EVERY working-memory key for a single (tenant,user)
// subject — every session's active chat buffer, condensed summary, and sequence
// counter — implementing the working-memory half of subject-level erasure
// (GDPR Art.17). It is scoped strictly to the (tenant,user) prefix (see
// SubjectPrefix): another subject's keys are structurally unreachable, and a
// blank or malformed tenant/user is rejected up front rather than widened into
// a wildcard delete. Idempotent: erasing a subject that has no data returns
// (0, nil), and erasing twice is safe. Returns the number of keys deleted.
//
// It erases only the persisted keys. A condensation goroutine mid-flight for one
// of the subject's sessions is not cancelled here; because every write is
// prefix-scoped and Append re-derives keys, a late write landing after an erase
// simply re-creates that session's keys, which a subsequent erase removes. A
// caller erasing a LIVE subject should stop sending its turns first.
func (w *WorkingMemory) EraseSubject(ctx context.Context, tenant, user string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	prefix, err := SubjectPrefix(tenant, user)
	if err != nil {
		return 0, err
	}

	deleted := 0
	for {
		// Collect one batch of keys under the subject prefix with a key-only scan
		// (PrefetchValues=false), then delete them in a separate write txn — never
		// deleting while iterating the same transaction.
		var batch [][]byte
		if err := w.db.View(func(txn *badger.Txn) error {
			opts := badger.DefaultIteratorOptions
			opts.Prefix = prefix
			opts.PrefetchValues = false
			it := txn.NewIterator(opts)
			defer it.Close()
			for it.Seek(prefix); it.ValidForPrefix(prefix) && len(batch) < eraseBatchSize; it.Next() {
				batch = append(batch, append([]byte(nil), it.Item().Key()...))
			}
			return nil
		}); err != nil {
			return deleted, fmt.Errorf("memory: scan subject keys for erase: %w", err)
		}
		if len(batch) == 0 {
			return deleted, nil
		}
		if err := w.db.Update(func(txn *badger.Txn) error {
			for _, k := range batch {
				if err := txn.Delete(k); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return deleted, fmt.Errorf("memory: erase subject keys: %w", err)
		}
		deleted += len(batch)
		if len(batch) < eraseBatchSize {
			return deleted, nil
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
//
// It is the LOSSY convenience wrapper over LookupSummary: it collapses "no
// summary is stored" and "the stored summary is empty" into the same ("", nil).
// A caller that needs to tell those apart — anything that would otherwise answer
// as if the session had no history — must use LookupSummary (and, for the
// stronger question "was this session ever written at all?", SessionExists).
func (w *WorkingMemory) Summary(ctx context.Context, key Key) (string, error) {
	summary, _, err := w.LookupSummary(ctx, key)
	return summary, err
}

// LookupSummary returns key's condensed "session state" summary and whether one
// is actually STORED for this session.
//
// found is the whole point. A missing summary key is a legitimate, expected
// state — a session on its first turns has had no condensation yet — so it is
// not an error and must not be reported as one. But it is NOT the same fact as
// "the summary is empty", and Summary's single ("", nil) return made the two
// indistinguishable: a mis-mounted AISOTROPY_DATA_DIR, a store opened against
// the wrong directory, or a partially wiped session all read exactly like a
// brand-new conversation, and hydration answered with no history and no signal
// that anything was missing.
//
// The split is: this accessor reports the FACT (stored / not stored); the caller
// owns the EXPECTATION (did I have reason to believe there was history here?).
// Pair it with SessionExists, which answers the expectation half from the store.
func (w *WorkingMemory) LookupSummary(ctx context.Context, key Key) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if err := key.Validate(); err != nil {
		return "", false, err
	}

	var (
		summary string
		found   bool
	)
	err := w.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key.summaryKey())
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return item.Value(func(val []byte) error {
			summary = string(val)
			return nil
		})
	})
	if err != nil {
		return "", false, fmt.Errorf("memory: read summary: %w", err)
	}
	return summary, found, nil
}

// SessionExists reports whether this session was EVER written to — that is,
// whether its sequence counter is present. Append allocates that counter on the
// first turn and never removes it (condensation prunes messages, not the
// counter; only EraseSubject deletes it along with everything else under the
// subject prefix), so it is the durable "this session is known" marker.
//
// It exists so a caller can distinguish the two readings of an empty hydration:
//
//	false — the session has no history YET (first turn). Legitimate; not an error.
//	true  — the session is known but nothing came back. Something that was
//	        written is not readable: the wrong data directory, a store opened
//	        empty, or a partial wipe. That is a defect, and the caller should say
//	        so rather than answer as if the user had never spoken.
//
// It is one point-get, and callers only need it on the empty-hydration path.
func (w *WorkingMemory) SessionExists(ctx context.Context, key Key) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := key.Validate(); err != nil {
		return false, err
	}

	exists := false
	err := w.db.View(func(txn *badger.Txn) error {
		_, err := txn.Get(key.seqKey())
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		exists = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("memory: probe session existence: %w", err)
	}
	return exists, nil
}

// ReportEmptyHydration is the diagnostic a caller runs when it hydrated this
// session and got NOTHING — no stored summary and no recent messages — to find
// out which of the two very different situations it is in, and to make the bad
// one visible. It reports true when the emptiness is ANOMALOUS.
//
// The old behaviour was that both situations produced the same silence: the
// summary read swallowed badger.ErrKeyNotFound into ("", nil) and the recent
// read returned an empty slice, so a first turn and a store that lost the
// session were byte-identical to the caller. A mis-mounted AISOTROPY_DATA_DIR or
// a store opened against the wrong directory therefore degraded into "the bot
// answers as if it had no history", with no error, no log, and nobody the wiser.
//
// It is advisory and never fails the request: the caller has already served its
// reads, and a probe that cannot answer must not turn a working turn into an
// error. A probe failure is itself logged and reported as non-anomalous (it is
// not evidence of loss), so this can be called unconditionally on the empty
// path.
func (w *WorkingMemory) ReportEmptyHydration(ctx context.Context, key Key) bool {
	exists, err := w.SessionExists(ctx, key)
	if err != nil {
		slog.WarnContext(ctx, "working memory: could not probe whether this session has history; hydration emptiness is unexplained",
			"tenant", key.Tenant, "user", key.User, "session", key.Session, "err", err)
		return false
	}
	if !exists {
		// The legitimate case: nothing was ever written under this session key.
		// A first turn is not a defect and is not logged at WARN.
		return false
	}
	slog.WarnContext(ctx, "working memory: session is known but hydrated EMPTY — history was written and is not readable back (check AISOTROPY_DATA_DIR, store identity, and whether this process opened the same directory that served the earlier turns)",
		"tenant", key.Tenant, "user", key.User, "session", key.Session)
	return true
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

// capMessageContent bounds content to at most maxBytes bytes, truncating on a
// UTF-8 rune boundary (never splitting a multi-byte rune) and appending a
// human-readable marker noting how many bytes overflowed the cap. It reports
// truncated=false and returns content unchanged when it already fits, so a
// normal message is preserved byte-for-byte. When it does truncate, the
// returned string is content[:cut] + marker, where cut is at or below maxBytes;
// the appended marker is a few dozen bytes, so the result still sits far under
// BadgerDB's ~64 KiB per-value ceiling given a conservative maxBytes (48 KiB).
// over is len(content)-maxBytes, i.e. how far the ORIGINAL content ran past the
// cap. A non-positive maxBytes disables the guard (returns content untouched).
func capMessageContent(content string, maxBytes int) (capped string, over int, truncated bool) {
	if maxBytes <= 0 || len(content) <= maxBytes {
		return content, 0, false
	}
	over = len(content) - maxBytes
	// Back cut down from maxBytes to the nearest rune boundary so content[:cut]
	// contains only whole runes — utf8.RuneStart(content[cut]) is true exactly
	// when content[cut] is not a UTF-8 continuation byte, i.e. content[:cut]
	// ends on a rune boundary.
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(content[cut]) {
		cut--
	}
	marker := fmt.Sprintf(" …[truncated: %d bytes over cap]", over)
	return content[:cut] + marker, over, true
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
