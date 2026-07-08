package memory

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
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
// Wiring into the chat flow is the next step: internal/api/chat_stream.go's
// ChatInput contract currently carries only TenantID (no user_id/session_id
// — see internal/api/chat.go), so persisting messages here and prepending
// Recent() to the system prompt would require extending that Huma contract
// first. Per the constitutional gates on backward-compatible API contracts,
// that is a deliberate, separate change, not bundled into this standalone
// store.
type WorkingMemory struct {
	db                *badger.DB
	maxActiveMessages int
	summarizer        Summarizer
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
	return &WorkingMemory{db: db, maxActiveMessages: maxActiveMessages, summarizer: summarizer}
}

// Open opens (creating if necessary) a production, on-disk BadgerDB instance
// rooted at path and wraps it in a WorkingMemory. This is the persistent
// path referenced in docs/components/badgerdb-working-memory.md.
func Open(path string, maxActiveMessages int, summarizer Summarizer) (*WorkingMemory, error) {
	opts := badger.DefaultOptions(path)
	opts.Logger = nil // BadgerDB's default logger is noisy on stdout; the
	// caller's slog-based logging is the source of truth for this service.
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
	opts := badger.DefaultOptions("").WithInMemory(true)
	opts.Logger = nil
	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("memory: open in-memory badger: %w", err)
	}
	return NewWorkingMemory(db, maxActiveMessages, summarizer), nil
}

// Close closes the underlying BadgerDB instance.
func (w *WorkingMemory) Close() error {
	return w.db.Close()
}

// Append records msg as the newest entry in key's active buffer. If msg.
// Timestamp is zero, it is set to time.Now().UTC(). If the active buffer
// now exceeds the configured maxActiveMessages, the oldest excess messages
// are condensed into the summary buffer (via the configured Summarizer) and
// removed from the active buffer, keeping the window bounded — see
// docs/architecture/memory-tiers.md -> "Token optimization — Summary
// Buffer".
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

	var excessKeys [][]byte
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
		if over := len(keys) - w.maxActiveMessages; over > 0 {
			excessKeys = keys[:over]
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("memory: append message: %w", err)
	}

	if len(excessKeys) == 0 {
		return nil
	}
	// Condensation (and, for LLMSummarizer, a real LLM call) runs after the
	// append transaction has already committed, so it never holds a
	// BadgerDB transaction open for the duration of a network call.
	return w.condenseAndPrune(ctx, key, excessKeys)
}

// condenseAndPrune folds the messages under excessKeys (the oldest ones
// that no longer fit in the active window) into the summary buffer and
// deletes them from the active buffer. Only these — never the whole active
// buffer — are decoded from JSON, so the cost of pruning is proportional to
// how many messages fell out of the window, not to the window's size.
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
