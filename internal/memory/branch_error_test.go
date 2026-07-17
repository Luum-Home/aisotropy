package memory

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgraph-io/badger/v4"
)

// --- construction / open error paths -------------------------------------

// TestNewWorkingMemoryNilDefaults hits the two guard branches of
// NewWorkingMemory in one call: a non-positive limit falls back to the
// default window, and a nil summarizer falls back to NoopSummarizer so the
// store is usable without any LLM wiring.
func TestNewWorkingMemoryNilDefaults(t *testing.T) {
	wm, err := OpenInMemory(0, nil) // limit<=0 AND nil summarizer
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = wm.Close() })

	if wm.maxActiveMessages != defaultMaxActiveMessages {
		t.Fatalf("maxActiveMessages = %d, want default %d", wm.maxActiveMessages, defaultMaxActiveMessages)
	}
	if _, ok := wm.summarizer.(NoopSummarizer); !ok {
		t.Fatalf("nil summarizer not replaced with NoopSummarizer, got %T", wm.summarizer)
	}
	// The store must actually work end-to-end with the defaulted deps.
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	if err := wm.Append(context.Background(), key, Message{Role: "user", Content: "hi"}); err != nil {
		t.Fatalf("Append on defaulted store: %v", err)
	}
}

// TestOpenBadPath exercises Open's error branch: pointing BadgerDB at a path
// that is a regular file (not a directory) makes badger.Open fail, and Open
// must wrap and surface that error rather than return a half-built store.
func TestOpenBadPath(t *testing.T) {
	f := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	wm, err := Open(f, 10, NoopSummarizer{})
	if err == nil {
		_ = wm.Close()
		t.Fatal("Open on a file path = nil error, want failure")
	}
	if !strings.Contains(err.Error(), "open badger") {
		t.Fatalf("error = %v, want it to mention opening badger", err)
	}
}

// --- nextSeq branches -----------------------------------------------------

// TestNextSeqCorruptValue stores a sequence value that is not 8 bytes and
// verifies nextSeq rejects it: a corrupt counter must be a loud error, not a
// silently-misread sequence that could collide message keys.
func TestNextSeqCorruptValue(t *testing.T) {
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	seqKey := key.seqKey()

	if err := wm.db.Update(func(txn *badger.Txn) error {
		return txn.Set(seqKey, []byte{1, 2, 3}) // wrong length
	}); err != nil {
		t.Fatal(err)
	}

	err := wm.db.Update(func(txn *badger.Txn) error {
		_, err := nextSeq(txn, seqKey)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "corrupt sequence") {
		t.Fatalf("nextSeq on corrupt value = %v, want a corrupt-sequence error", err)
	}
}

// TestNextSeqReadOnlyTxnSetFails covers the txn.Set failure branch: inside a
// read-only View transaction the write of the incremented counter must fail.
func TestNextSeqReadOnlyTxnSetFails(t *testing.T) {
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}

	err := wm.db.View(func(txn *badger.Txn) error {
		_, err := nextSeq(txn, key.seqKey())
		return err
	})
	if !errors.Is(err, badger.ErrReadOnlyTxn) {
		t.Fatalf("nextSeq inside View = %v, want ErrReadOnlyTxn", err)
	}
}

// TestAppendPropagatesNextSeqError verifies Append surfaces a nextSeq failure:
// a corrupt sequence counter must abort the append (and be wrapped), never
// write a message under an undefined sequence.
func TestAppendPropagatesNextSeqError(t *testing.T) {
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	if err := wm.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key.seqKey(), []byte{9, 9}) // corrupt
	}); err != nil {
		t.Fatal(err)
	}
	err := wm.Append(context.Background(), key, Message{Role: "user", Content: "x"})
	if err == nil || !strings.Contains(err.Error(), "append message") {
		t.Fatalf("Append with corrupt seq = %v, want a wrapped append error", err)
	}
}

// --- decodeMessages branches ----------------------------------------------

// TestDecodeMessagesKeyNotFound covers the txn.Get error branch of
// decodeMessages when asked for a key that does not exist.
func TestDecodeMessagesKeyNotFound(t *testing.T) {
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	missing := key.chatMessageKey(999)

	err := wm.db.View(func(txn *badger.Txn) error {
		_, err := decodeMessages(txn, [][]byte{missing})
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "read stored message") {
		t.Fatalf("decodeMessages(missing) = %v, want a read error", err)
	}
}

// TestDecodeMessagesCorruptValue covers the json.Unmarshal error branch: a
// stored value that is not valid message JSON must fail decoding rather than
// yield a zero-value message.
func TestDecodeMessagesCorruptValue(t *testing.T) {
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	badKey := key.chatMessageKey(1)
	if err := wm.db.Update(func(txn *badger.Txn) error {
		return txn.Set(badKey, []byte("{not json"))
	}); err != nil {
		t.Fatal(err)
	}

	err := wm.db.View(func(txn *badger.Txn) error {
		_, err := decodeMessages(txn, [][]byte{badKey})
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "decode stored message") {
		t.Fatalf("decodeMessages(corrupt) = %v, want a decode error", err)
	}
}

// --- Recent decode error --------------------------------------------------

// TestRecentDecodeError covers Recent's decode-and-wrap branch when a stored
// message value is corrupt.
func TestRecentDecodeError(t *testing.T) {
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	if err := wm.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key.chatMessageKey(1), []byte("not-json"))
	}); err != nil {
		t.Fatal(err)
	}
	_, err := wm.Recent(context.Background(), key, 5)
	if err == nil || !strings.Contains(err.Error(), "read recent messages") {
		t.Fatalf("Recent over a corrupt buffer = %v, want a wrapped read error", err)
	}
}

// --- condenseAndPrune decode error ----------------------------------------

// TestCondenseAndPruneDecodeError drives condenseAndPrune with an excess key
// whose stored value is corrupt: the decode failure must propagate so a bad
// read can never silently prune (and lose) the message.
func TestCondenseAndPruneDecodeError(t *testing.T) {
	wm := newTestStore(t, 2)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	badKey := key.chatMessageKey(1)
	if err := wm.db.Update(func(txn *badger.Txn) error {
		return txn.Set(badKey, []byte("{broken"))
	}); err != nil {
		t.Fatal(err)
	}

	err := wm.condenseAndPrune(context.Background(), key, [][]byte{badKey})
	if err == nil || !strings.Contains(err.Error(), "read messages pending condensation") {
		t.Fatalf("condenseAndPrune over corrupt data = %v, want a read error", err)
	}
	// The corrupt message must NOT have been deleted by the aborted prune.
	if err := wm.db.View(func(txn *badger.Txn) error {
		_, err := txn.Get(badKey)
		return err
	}); err != nil {
		t.Fatalf("excess key was pruned despite a failed condensation: %v", err)
	}
}

// --- triggerCondensation closed guard -------------------------------------

// TestTriggerCondensationAfterClose covers the closed-store guard: once
// Close has run, triggerCondensation must be a no-op (never spawn a goroutine
// against a closed db).
func TestTriggerCondensationAfterClose(t *testing.T) {
	wm, err := OpenInMemory(2, NoopSummarizer{})
	if err != nil {
		t.Fatal(err)
	}
	if err := wm.Close(); err != nil {
		t.Fatal(err)
	}
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	// Must not panic or spawn work; wg stays at zero so a follow-up Sync
	// returns immediately.
	wm.triggerCondensation(key)
	wm.Sync()
}

// TestSeqValueRoundTrip is a small guard that an 8-byte counter written by
// nextSeq reads back as the expected number (the happy path of the length
// check), so the corrupt-value test above is meaningfully distinct.
func TestSeqValueRoundTrip(t *testing.T) {
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	var got uint64
	if err := wm.db.Update(func(txn *badger.Txn) error {
		n, err := nextSeq(txn, key.seqKey())
		got = n
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("first nextSeq = %d, want 1", got)
	}
	if err := wm.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key.seqKey())
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			if binary.BigEndian.Uint64(v) != 1 {
				t.Fatalf("stored seq = %d, want 1", binary.BigEndian.Uint64(v))
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
}
