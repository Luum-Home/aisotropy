package memory

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	badger "github.com/dgraph-io/badger/v4"
)

// BadgerWorkingBackend implements WorkingBackend on an embedded BadgerDB — the
// reference driver, and the one that keeps today's behaviour when WorkingMemory
// is eventually built on the seam.
//
// It exists for a reason beyond parity. A contract satisfied only by an
// in-memory map in a test file has not been shown to be implementable by a real
// store: the map has free transactions, free ordering and no key encoding, so it
// can satisfy an interface that a persistent driver cannot. Running the SAME
// conformance suite against a driver with real keys, real iteration order and
// real transactions is what turns the interface from plausible into evidenced.
//
// KEY LAYOUT. Two disjoint trees, and the split is what makes the two hard
// operations single-prefix scans rather than filtered walks:
//
//	wm:{ns}:s:{session}:chat:{seq20}   messages
//	wm:{ns}:s:{session}:summary        condensed summary
//	wm:{ns}:s:{session}:seq            ordering counter
//	ob:{ns}:{id}                       outbox rows (pending and parked alike)
//
// The outbox lives in its own tree because the drain is CROSS-tenant: it must
// read every namespace's pending rows in one scan, which a per-namespace layout
// would turn into "enumerate all namespaces, then scan each". DeleteNamespace
// pays for that with two prefix drops instead of one — still no filtering, so it
// cannot miss a key shape.
//
// Note what the namespace is here: an opaque string this type receives and
// concatenates. It never parses it, and it holds no way to derive one.
type BadgerWorkingBackend struct {
	db *badger.DB
	// ownsDB records whether Close should close the database. A backend sharing
	// a caller's *badger.DB must not close it out from under them.
	ownsDB bool
}

// NewBadgerWorkingBackend wraps an existing BadgerDB as a WorkingBackend. The
// caller keeps ownership of db: Close here does NOT close it.
func NewBadgerWorkingBackend(db *badger.DB) (*BadgerWorkingBackend, error) {
	if db == nil {
		return nil, errors.New("memory: badger working backend requires a non-nil *badger.DB")
	}
	return &BadgerWorkingBackend{db: db}, nil
}

// OpenBadgerWorkingBackend opens an in-memory BadgerDB and wraps it. Intended
// for tests and for the conformance suite; Close closes the database.
func OpenBadgerWorkingBackend() (*BadgerWorkingBackend, error) {
	db, err := badger.Open(tuneForWorkingMemory(badger.DefaultOptions("").WithInMemory(true)))
	if err != nil {
		return nil, fmt.Errorf("memory: open in-memory badger working backend: %w", err)
	}
	return &BadgerWorkingBackend{db: db, ownsDB: true}, nil
}

func (b *BadgerWorkingBackend) sessionPrefix(ns, session string) string {
	return "wm:" + ns + ":s:" + session + ":"
}

func (b *BadgerWorkingBackend) chatPrefix(ns, session string) string {
	return b.sessionPrefix(ns, session) + "chat:"
}

func (b *BadgerWorkingBackend) chatKey(ns, session string, seq uint64) []byte {
	return fmt.Appendf(nil, "%s%020d", b.chatPrefix(ns, session), seq)
}

func (b *BadgerWorkingBackend) summaryKey(ns, session string) []byte {
	return []byte(b.sessionPrefix(ns, session) + "summary")
}

func (b *BadgerWorkingBackend) seqKey(ns, session string) []byte {
	return []byte(b.sessionPrefix(ns, session) + "seq")
}

func (b *BadgerWorkingBackend) outboxKey(ns, id string) []byte {
	return []byte("ob:" + ns + ":" + id)
}

// NextSeq allocates the next ordering value for (ns, session).
func (b *BadgerWorkingBackend) NextSeq(ctx context.Context, ns, session string) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var seq uint64
	err := b.db.Update(func(txn *badger.Txn) error {
		var err error
		seq, err = nextSeq(txn, b.seqKey(ns, session))
		return err
	})
	return seq, err
}

func (b *BadgerWorkingBackend) AppendMessage(ctx context.Context, ns, session string, msg BackendMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("memory: encode backend message: %w", err)
	}
	return b.db.Update(func(txn *badger.Txn) error {
		return txn.Set(b.chatKey(ns, session, msg.Seq), payload)
	})
}

// AppendMessageWithEvent writes both records in ONE Badger transaction, so a
// crash mid-commit leaves both or neither. This is the driver honouring
// ADR-0081's first constraint; the conformance suite deliberately does not claim
// to have proven it, because a black-box suite cannot kill a process at the
// right instant.
func (b *BadgerWorkingBackend) AppendMessageWithEvent(ctx context.Context, ns, session string, msg BackendMessage, event BackendEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	msgPayload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("memory: encode backend message: %w", err)
	}
	evPayload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("memory: encode backend event: %w", err)
	}
	return b.db.Update(func(txn *badger.Txn) error {
		if err := txn.Set(b.chatKey(ns, session, msg.Seq), msgPayload); err != nil {
			return err
		}
		return txn.Set(b.outboxKey(ns, event.ID), evPayload)
	})
}

// scanMessages returns every message under (ns, session), ascending by Seq.
// Badger iterates keys in lexicographic order and the sequence is zero-padded
// to 20 digits, so byte order IS numeric order — which is why the padding is a
// correctness detail and not cosmetics.
func (b *BadgerWorkingBackend) scanMessages(ctx context.Context, ns, session string) ([]BackendMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []BackendMessage
	err := b.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		prefix := []byte(b.chatPrefix(ns, session))
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			var m BackendMessage
			if err := it.Item().Value(func(v []byte) error { return json.Unmarshal(v, &m) }); err != nil {
				return fmt.Errorf("memory: decode backend message: %w", err)
			}
			out = append(out, m)
		}
		return nil
	})
	return out, err
}

func (b *BadgerWorkingBackend) Recent(ctx context.Context, ns, session string, n int) ([]BackendMessage, error) {
	if n <= 0 {
		return nil, nil
	}
	all, err := b.scanMessages(ctx, ns, session)
	if err != nil || len(all) == 0 {
		return nil, err
	}
	if n > len(all) {
		n = len(all)
	}
	return all[len(all)-n:], nil // the NEWEST n, still oldest-first
}

func (b *BadgerWorkingBackend) OldestMessages(ctx context.Context, ns, session string, n int) ([]BackendMessage, error) {
	if n <= 0 {
		return nil, nil
	}
	all, err := b.scanMessages(ctx, ns, session)
	if err != nil || len(all) == 0 {
		return nil, err
	}
	if n > len(all) {
		n = len(all)
	}
	return all[:n], nil
}

func (b *BadgerWorkingBackend) CountMessages(ctx context.Context, ns, session string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	n := 0
	err := b.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		prefix := []byte(b.chatPrefix(ns, session))
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			n++
		}
		return nil
	})
	return n, err
}

// Condense replaces the summary and prunes the named messages in one
// transaction: a summary written without its prune duplicates content, and a
// prune without its summary loses it.
func (b *BadgerWorkingBackend) Condense(ctx context.Context, ns, session, summary string, removeSeqs []uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return b.db.Update(func(txn *badger.Txn) error {
		if err := txn.Set(b.summaryKey(ns, session), []byte(summary)); err != nil {
			return err
		}
		for _, seq := range removeSeqs {
			if err := txn.Delete(b.chatKey(ns, session, seq)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (b *BadgerWorkingBackend) GetSummary(ctx context.Context, ns, session string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	var (
		summary string
		found   bool
	)
	err := b.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(b.summaryKey(ns, session))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return item.Value(func(v []byte) error {
			summary = string(v)
			return nil
		})
	})
	return summary, found, err
}

func (b *BadgerWorkingBackend) SessionExists(ctx context.Context, ns, session string) (bool, error) {
	n, err := b.CountMessages(ctx, ns, session)
	if err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	_, ok, err := b.GetSummary(ctx, ns, session)
	return ok, err
}

// DeleteNamespace drops both trees for ns. Two prefix scans, no predicate: a
// filtered erase can only remove the key shapes whoever wrote it enumerated, so
// adding a shape later would silently leave tenant data behind a receipt saying
// it was gone.
func (b *BadgerWorkingBackend) DeleteNamespace(ctx context.Context, ns string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	removed := 0
	for _, prefix := range []string{"wm:" + ns + ":", "ob:" + ns + ":"} {
		var keys [][]byte
		if err := b.db.View(func(txn *badger.Txn) error {
			opts := badger.DefaultIteratorOptions
			opts.PrefetchValues = false
			it := txn.NewIterator(opts)
			defer it.Close()
			p := []byte(prefix)
			for it.Seek(p); it.ValidForPrefix(p); it.Next() {
				keys = append(keys, it.Item().KeyCopy(nil))
			}
			return nil
		}); err != nil {
			return removed, err
		}
		for _, k := range keys {
			// The sequence counter is bookkeeping, not a record: counting it
			// would inflate the erase receipt above the number of stored items.
			isSeq := strings.HasSuffix(string(k), ":seq")
			if err := b.db.Update(func(txn *badger.Txn) error { return txn.Delete(k) }); err != nil {
				return removed, err
			}
			if !isSeq {
				removed++
			}
		}
	}
	return removed, nil
}

// scanEvents walks the whole outbox tree across every namespace — the
// cross-tenant drain read.
func (b *BadgerWorkingBackend) scanEvents(ctx context.Context, dead bool) ([]BackendEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []BackendEvent
	err := b.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		prefix := []byte("ob:")
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			var e BackendEvent
			if err := it.Item().Value(func(v []byte) error { return json.Unmarshal(v, &e) }); err != nil {
				return fmt.Errorf("memory: decode backend event: %w", err)
			}
			if e.Dead == dead {
				out = append(out, e)
			}
		}
		return nil
	})
	return out, err
}

func (b *BadgerWorkingBackend) PendingEvents(ctx context.Context) ([]BackendEvent, error) {
	return b.scanEvents(ctx, false)
}

func (b *BadgerWorkingBackend) DeadEvents(ctx context.Context) ([]BackendEvent, error) {
	return b.scanEvents(ctx, true)
}

// PutEvent writes an event keyed by (ns, id) — idempotent on id by construction,
// which is the property ADR-0081 makes load-bearing rather than a nicety.
func (b *BadgerWorkingBackend) PutEvent(ctx context.Context, ns string, event BackendEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("memory: encode backend event: %w", err)
	}
	return b.db.Update(func(txn *badger.Txn) error {
		return txn.Set(b.outboxKey(ns, event.ID), payload)
	})
}

// DeleteEvent removes an event. An unknown id is a no-op: the drain is
// at-least-once, so a second drainer WILL delete a row the first already
// removed, and erroring there turns a benign race into a failed pass.
func (b *BadgerWorkingBackend) DeleteEvent(ctx context.Context, ns, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return b.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(b.outboxKey(ns, id))
	})
}

// Namespaces implements plugintest.RawWorkingInspector so the conformance suite
// can inspect the seal directly on a real driver.
func (b *BadgerWorkingBackend) Namespaces() []string {
	seen := map[string]struct{}{}
	_ = b.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		for _, prefix := range []string{"wm:", "ob:"} {
			p := []byte(prefix)
			for it.Seek(p); it.ValidForPrefix(p); it.Next() {
				k := string(it.Item().Key())
				rest := k[len(prefix):]
				if i := strings.IndexByte(rest, ':'); i > 0 {
					seen[rest[:i]] = struct{}{}
				}
			}
		}
		return nil
	})
	out := make([]string, 0, len(seen))
	for ns := range seen {
		out = append(out, ns)
	}
	return out
}

func (b *BadgerWorkingBackend) Close() error {
	if b.ownsDB {
		return b.db.Close()
	}
	return nil
}

// compile-time proof the driver satisfies the seam.
var _ WorkingBackend = (*BadgerWorkingBackend)(nil)

// binary is referenced by nextSeq's encoding; kept explicit so a future edit
// that drops the import fails loudly rather than silently changing the layout.
var _ = binary.BigEndian
