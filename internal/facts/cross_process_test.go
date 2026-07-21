package facts

import (
	"context"
	"path/filepath"
	"testing"
)

// This file proves the facts tier's actual cross-PROCESS contract: a fact
// written through one Store handle (standing in for one OS process — e.g. the
// arena's `memory`-mode persist invocation, or any real aisotropy consumer
// whose process restarts) is readable through a COMPLETELY SEPARATE, later
// Store handle opened against the SAME backing store (standing in for a
// second process — e.g. the arena's paired recall invocation).
//
// This is exactly the capability
// docs/architecture/arena-memory-forensics.md §3.3/§5.1 found the arena's own
// adapter was silently defeating: arenas/live/aisotropy/rqlite_reset.go used
// to DROP the facts table on EVERY process boot, including the recall
// process's own boot, so the facts tier could never contribute to
// cross-process recall no matter how correct this package's own Store
// implementations are. These tests pin the product-level guarantee
// (internal/facts itself has always honored it) independently of that
// adapter-side defect, so the guarantee is verified where it actually lives.
//
// Offline throughout: no network, no model calls. The rqlite case uses the
// package's own httptest fake (see rqlite_httptest_test.go); the SQLite case
// uses a real on-disk file (not ":memory:", which is inherently
// single-connection and would not exercise a genuine second "process").

// TestSQLiteStore_CrossProcessRecall proves the embedded, file-backed default:
// a fact saved through one SQLiteStore handle survives that handle's Close and
// is found by a brand-new SQLiteStore opened later against the SAME on-disk
// file — the literal shape of one OS process exiting and a fresh one starting.
func TestSQLiteStore_CrossProcessRecall(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "facts.db")
	sc, err := NewScope("arena", "live")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}

	// "Process 1" (persist): open, write, close — nothing kept in memory that
	// a second process could piggyback on.
	proc1, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("process 1: OpenSQLite: %v", err)
	}
	if err := proc1.Save(context.Background(), sc, Fact{
		ID: "cross-proc-1", TenantID: "arena", UserID: "live",
		Content: "the needle fact is Vermillion Bravo", Importance: 0.8,
	}); err != nil {
		t.Fatalf("process 1: Save: %v", err)
	}
	if err := proc1.Close(); err != nil {
		t.Fatalf("process 1: Close: %v", err)
	}

	// "Process 2" (recall): a completely independent handle, re-opened later
	// against the same file. If this were a fresh OS process, this is exactly
	// what its boot would do.
	proc2, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("process 2: OpenSQLite: %v", err)
	}
	defer func() { _ = proc2.Close() }()

	got, err := proc2.Search(context.Background(), sc, "Vermillion Bravo", 5)
	if err != nil {
		t.Fatalf("process 2: Search: %v", err)
	}
	if len(got) != 1 || got[0].ID != "cross-proc-1" {
		t.Fatalf("process 2 did not recall process 1's fact: got %+v", got)
	}
	t.Logf("cross-process SQLite recall OK: %q (score=%.4f)", got[0].Content, got[0].Score)
}

// TestRqliteStore_CrossProcessRecall proves the networked path the arena
// actually exercises: TWO separate RqliteStore client connections against ONE
// shared rqlite-like backend (the package's httptest fake stands in for the
// real, independently-running rqlite service) — a fact saved through the
// first connection is found through the second, entirely independent one,
// with no shared Go state between them (a real second OS process would share
// nothing but the network endpoint either).
func TestRqliteStore_CrossProcessRecall(t *testing.T) {
	f := newFakeRqlite(t)
	sc, err := NewScope("arena", "live")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}

	// "Process 1" (persist).
	proc1, err := OpenRqlite(f.url())
	if err != nil {
		t.Fatalf("process 1: OpenRqlite: %v", err)
	}
	if err := proc1.Save(context.Background(), sc, Fact{
		ID: "cross-proc-rqlite-1", TenantID: "arena", UserID: "live",
		Content: "the needle fact is Vermillion Bravo", Importance: 0.8,
	}); err != nil {
		t.Fatalf("process 1: Save: %v", err)
	}
	proc1.Close()

	// "Process 2" (recall): a brand-new connection, no shared Go state with
	// proc1 beyond the same rqlite endpoint — the honest analogue of a second
	// OS process talking to the same long-lived rqlite service.
	proc2, err := OpenRqlite(f.url())
	if err != nil {
		t.Fatalf("process 2: OpenRqlite: %v", err)
	}
	defer proc2.Close()

	got, err := proc2.Search(context.Background(), sc, "Vermillion Bravo", 5)
	if err != nil {
		t.Fatalf("process 2: Search: %v", err)
	}
	if len(got) != 1 || got[0].ID != "cross-proc-rqlite-1" {
		t.Fatalf("process 2 did not recall process 1's fact via the shared rqlite backend: got %+v", got)
	}
	t.Logf("cross-process rqlite recall OK: %q (score=%.4f)", got[0].Content, got[0].Score)
}
