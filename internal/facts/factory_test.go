package facts

import (
	"context"
	"path/filepath"
	"testing"
)

// clearFactsEnv blanks every env var OpenFromEnv consults so each subtest starts
// from a known precedence baseline (empty is treated as unset by OpenFromEnv).
func clearFactsEnv(t *testing.T) {
	t.Helper()
	t.Setenv("RQLITE_URL", "")
	t.Setenv("AISOTROPY_FACTS_DB", "")
	t.Setenv("AISOTROPY_DATA_DIR", "")
}

func TestOpenFromEnv_Default_InMemorySQLite(t *testing.T) {
	clearFactsEnv(t)
	st, err := OpenFromEnv()
	if err != nil {
		t.Fatalf("OpenFromEnv: %v", err)
	}
	defer func() { _ = st.Close() }()
	if _, ok := st.(*SQLiteStore); !ok {
		t.Fatalf("default should be an embedded SQLiteStore, got %T", st)
	}
}

func TestOpenFromEnv_FactsDBFile(t *testing.T) {
	clearFactsEnv(t)
	path := filepath.Join(t.TempDir(), "facts.db")
	t.Setenv("AISOTROPY_FACTS_DB", path)
	st, err := OpenFromEnv()
	if err != nil {
		t.Fatalf("OpenFromEnv: %v", err)
	}
	defer func() { _ = st.Close() }()
	// Prove it is usable: a scoped save+search round-trips.
	sc, _ := NewScope("acme", "alice")
	if err := st.Save(context.Background(), sc, Fact{ID: "f", TenantID: "acme", UserID: "alice", Content: "file backed keyword"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := st.Search(context.Background(), sc, "keyword", 5)
	if err != nil || len(got) != 1 {
		t.Fatalf("Search: got %d (%v)", len(got), err)
	}
}

func TestOpenFromEnv_DataDir(t *testing.T) {
	clearFactsEnv(t)
	t.Setenv("AISOTROPY_DATA_DIR", t.TempDir())
	st, err := OpenFromEnv()
	if err != nil {
		t.Fatalf("OpenFromEnv: %v", err)
	}
	defer func() { _ = st.Close() }()
	if _, ok := st.(*SQLiteStore); !ok {
		t.Fatalf("data-dir path should open a SQLiteStore, got %T", st)
	}
}

func TestOpenFromEnv_RqliteURLTakesPrecedence(t *testing.T) {
	clearFactsEnv(t)
	f := newFakeRqlite(t)
	// Both RQLITE_URL and AISOTROPY_FACTS_DB set: RQLITE_URL must win.
	t.Setenv("AISOTROPY_FACTS_DB", filepath.Join(t.TempDir(), "ignored.db"))
	t.Setenv("RQLITE_URL", f.url())
	st, err := OpenFromEnv()
	if err != nil {
		t.Fatalf("OpenFromEnv: %v", err)
	}
	defer func() { _ = st.Close() }()
	if _, ok := st.(*RqliteStore); !ok {
		t.Fatalf("RQLITE_URL should select RqliteStore, got %T", st)
	}
}

func TestOpenFromEnv_RqliteURLError(t *testing.T) {
	clearFactsEnv(t)
	t.Setenv("RQLITE_URL", "http://127.0.0.1:1")
	if _, err := OpenFromEnv(); err == nil {
		t.Fatalf("expected error from unreachable RQLITE_URL")
	}
}
