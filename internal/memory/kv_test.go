package memory

import (
	"context"
	"path/filepath"
	"testing"
)

// TestKVRoundTripSurvivesReopen is the durability claim itself: what a KV writes
// to an on-disk store is there for the NEXT process. Anything built on top of it
// (the cost accumulator) inherits exactly this guarantee and no more.
func TestKVRoundTripSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "wm")

	wm, err := Open(dir, 10, NoopSummarizer{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	kv := wm.NewKV("cost:totals:")
	if !kv.Durable() {
		t.Fatal("Durable() = false on an on-disk store")
	}
	if err := kv.Store(ctx, map[string][]byte{"acme": []byte(`{"n":1}`), "globex": []byte(`{"n":2}`)}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := wm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(dir, 10, NoopSummarizer{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	got, err := reopened.NewKV("cost:totals:").Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 || string(got["acme"]) != `{"n":1}` || string(got["globex"]) != `{"n":2}` {
		t.Fatalf("Load after reopen = %v, want both entries with the prefix stripped", got)
	}
}

// TestKVInMemoryStoreIsNotDurable is the trap this exists to expose: with no
// AISOTROPY_DATA_DIR the working-memory store is in-memory, so a KV over it
// writes into something that dies with the process. It must SAY so — a caller
// that trusts a silent write here has a fix that is inert in the default
// configuration.
func TestKVInMemoryStoreIsNotDurable(t *testing.T) {
	wm := newTestStore(t, 10)
	if wm.NewKV("cost:totals:").Durable() {
		t.Fatal("Durable() = true over an in-memory BadgerDB")
	}
}

// TestKVPrefixIsolatesNamespaces: one namespace's scan never returns another's
// keys, and a KV write never disturbs the chat key space it shares a store with.
func TestKVPrefixIsolatesNamespaces(t *testing.T) {
	ctx := context.Background()
	wm := newTestStore(t, 10)
	key := Key{Tenant: "acme", User: "alice", Session: "s1"}
	if err := wm.Append(ctx, key, Message{Role: "user", Content: "hello"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	a := wm.NewKV("ns-a:")
	b := wm.NewKV("ns-b:")
	if err := a.Store(ctx, map[string][]byte{"k": []byte("va")}); err != nil {
		t.Fatalf("Store a: %v", err)
	}
	if err := b.Store(ctx, map[string][]byte{"k": []byte("vb")}); err != nil {
		t.Fatalf("Store b: %v", err)
	}

	gotA, err := a.Load(ctx)
	if err != nil {
		t.Fatalf("Load a: %v", err)
	}
	if len(gotA) != 1 || string(gotA["k"]) != "va" {
		t.Fatalf("namespace a loaded %v, want only its own entry", gotA)
	}

	// The chat buffer sharing the store is untouched.
	msgs, err := wm.Recent(ctx, key, 10)
	if err != nil || len(msgs) != 1 || msgs[0].Content != "hello" {
		t.Fatalf("chat buffer disturbed by KV writes: %v (%v)", msgs, err)
	}
}

// TestKVStoreEmptyIsNoOp keeps the write-behind path free: a flush with nothing
// to write must not open a transaction.
func TestKVStoreEmptyIsNoOp(t *testing.T) {
	wm := newTestStore(t, 10)
	if err := wm.NewKV("ns:").Store(context.Background(), nil); err != nil {
		t.Fatalf("Store(nil): %v", err)
	}
}

// TestKVRespectsCancellation keeps it consistent with every other accessor in
// this package: a cancelled context is refused, not served.
func TestKVRespectsCancellation(t *testing.T) {
	wm := newTestStore(t, 10)
	kv := wm.NewKV("ns:")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := kv.Load(ctx); err == nil {
		t.Error("Load served a cancelled context")
	}
	if err := kv.Store(ctx, map[string][]byte{"k": []byte("v")}); err == nil {
		t.Error("Store served a cancelled context")
	}
}

// TestKVSurfacesStoreErrors: a KV over a closed store must report the failure,
// not swallow it. The cost accumulator decides whether to retry from this error;
// a nil return would make a lost write look like a successful one.
func TestKVSurfacesStoreErrors(t *testing.T) {
	ctx := context.Background()
	wm, err := OpenInMemory(10, NoopSummarizer{})
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	kv := wm.NewKV("ns:")
	if err := wm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := kv.Store(ctx, map[string][]byte{"k": []byte("v")}); err == nil {
		t.Error("Store over a closed store reported success")
	}
	if _, err := kv.Load(ctx); err == nil {
		t.Error("Load over a closed store reported success")
	}
}
