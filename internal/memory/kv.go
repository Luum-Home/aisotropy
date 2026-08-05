package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/dgraph-io/badger/v4"
)

// KV is a small, prefix-namespaced byte store over the working-memory BadgerDB,
// for PROCESS-LEVEL state that needs the same durability as the chat buffer but
// is not itself a conversation — the per-tenant cost accumulator
// (internal/costmeter) is the first user.
//
// SCOPE, and why this is not a hole in the isolation gate. A KV is namespaced by
// its own prefix, disjoint from the chat/summary/seq/outbox/quota key spaces, and
// it stores no message content. It does NOT validate identities the way Key does,
// because its keys are not necessarily identities; a caller storing per-tenant
// state MUST put the tenant in the key (costmeter uses the tenant id verbatim) so
// a read stays scoped. Load is a full prefix scan and is therefore CROSS-TENANT
// by construction — an operator-level read, in the same class as
// costmeter.Snapshot behind GET /costs, and it must never be reached from a
// tenant-scoped request path.
//
// DURABILITY IS NOT ASSUMED. The working-memory store is an in-memory BadgerDB
// unless AISOTROPY_DATA_DIR is set, so a KV over it may be exactly as volatile as
// the map it was meant to replace. Durable() reports which one this is, and
// callers are expected to branch on it rather than write into a store that
// silently forgets — see costmeter.NewPersistent for the pattern.
type KV struct {
	db     *badger.DB
	prefix string
}

// NewKV returns a KV namespaced under prefix over this WorkingMemory's BadgerDB.
// Prefixes must not overlap with each other or with the store's own key spaces
// ("chat:", "summary:", "seq:", "outbox:pending:", "quota:"); ending a prefix
// with ':' keeps it unambiguous.
func (w *WorkingMemory) NewKV(prefix string) *KV {
	return &KV{db: w.db, prefix: prefix}
}

// Durable reports whether writes to this KV outlive the process — that is,
// whether the underlying BadgerDB is on disk (AISOTROPY_DATA_DIR) rather than
// in-memory. It is read from BadgerDB's own options, not from a flag this
// package sets, so it cannot drift from how the store was actually opened.
func (kv *KV) Durable() bool { return !kv.db.Opts().InMemory }

// Load returns every entry under the prefix, keyed WITHOUT it. See the type doc:
// this is a full prefix scan and an operator-level read.
func (kv *KV) Load(ctx context.Context) (map[string][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make(map[string][]byte)
	prefix := []byte(kv.prefix)
	err := kv.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			key := strings.TrimPrefix(string(item.Key()), kv.prefix)
			val, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			out[key] = val
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("memory: load kv prefix %q: %w", kv.prefix, err)
	}
	return out, nil
}

// Store writes every entry in one transaction — either all of them land or none
// do — overwriting the keys given and leaving every other key untouched. An
// empty map is a no-op (no transaction is opened).
func (kv *KV) Store(ctx context.Context, entries map[string][]byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	err := kv.db.Update(func(txn *badger.Txn) error {
		for k, v := range entries {
			if err := txn.Set([]byte(kv.prefix+k), v); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("memory: store kv prefix %q: %w", kv.prefix, err)
	}
	return nil
}
