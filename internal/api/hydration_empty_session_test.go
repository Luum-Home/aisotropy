package api

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/dgraph-io/badger/v4"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/memory"
)

// captureWarn redirects the default logger into a buffer for the duration of a
// test. Hydration's empty-session diagnostic is a log line, so the log IS the
// observable behaviour.
func captureWarn(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestHydrationFirstTurnDoesNotWarn: hydrating a session nobody has written to
// is the normal opening of every conversation. It must stay silent — a
// diagnostic that fires on every first turn is noise, and noise is how the real
// signal gets ignored.
func TestHydrationFirstTurnDoesNotWarn(t *testing.T) {
	ctx := context.Background()
	wm, err := memory.OpenInMemory(0, memory.NoopSummarizer{})
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = wm.Close() })

	key := memory.Key{Tenant: "acme", User: "alice", Session: "fresh"}
	scope, err := facts.NewScope("acme", "alice")
	if err != nil {
		t.Fatal(err)
	}

	buf := captureWarn(t)
	if _, err := hydrateSystemPrompt(ctx, wm, nil, nil, nil, nil, nil, key, scope, "p1", "hola"); err != nil {
		t.Fatalf("hydrateSystemPrompt: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("a first turn logged at WARN: %q", buf.String())
	}
}

// TestHydrationWarnsWhenKnownSessionComesBackEmpty is the request-path proof of
// the fix: the session was written (its sequence counter survives) but nothing
// hydrates. Before, that was byte-identical to a first turn — the model was
// handed the bare persona prompt and no error, no log, ever mentioned that the
// user's history had vanished.
func TestHydrationWarnsWhenKnownSessionComesBackEmpty(t *testing.T) {
	ctx := context.Background()

	db, err := badger.Open(badger.DefaultOptions("").WithInMemory(true).WithLogger(nil))
	if err != nil {
		t.Fatalf("badger.Open: %v", err)
	}
	wm := memory.NewWorkingMemory(db, 10, memory.NoopSummarizer{})
	t.Cleanup(func() { _ = wm.Close() })

	key := memory.Key{Tenant: "acme", User: "bob", Session: "s1"}
	if err := wm.Append(ctx, key, memory.Message{Role: "user", Content: "my seat is 14C"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Make the session's CONTENT disappear while its sequence counter survives —
	// what a wrong or partially-restored data directory looks like to a reader.
	// The counter is the only 8-byte value under the subject prefix (a stored
	// message is JSON, always longer), so that length is the discriminator here.
	if err := db.Update(func(txn *badger.Txn) error {
		var doomed [][]byte
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			if item.ValueSize() == 8 {
				continue // the sequence counter: this is what must survive
			}
			doomed = append(doomed, item.KeyCopy(nil))
		}
		it.Close()
		for _, k := range doomed {
			if derr := txn.Delete(k); derr != nil {
				return derr
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("simulate lost session content: %v", err)
	}

	scope, err := facts.NewScope("acme", "bob")
	if err != nil {
		t.Fatal(err)
	}

	buf := captureWarn(t)
	got, err := hydrateSystemPrompt(ctx, wm, nil, nil, nil, nil, nil, key, scope, "p1", "where do I sit?")
	if err != nil {
		t.Fatalf("hydrateSystemPrompt must still serve the turn: %v", err)
	}
	if got == "" {
		t.Fatal("hydration returned an empty system prompt")
	}
	logged := buf.String()
	if !strings.Contains(logged, "session is known but hydrated EMPTY") {
		t.Fatalf("hydration of a known-but-empty session stayed SILENT; log=%q", logged)
	}
	if strings.Contains(logged, "14C") {
		t.Error("the diagnostic leaked message content")
	}
}
