package facts

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRqlite_SearchPoolFloor covers Search's candidate-pool floor: with a small
// limit (limit*candidatePoolFactor < 20) the store must still over-fetch at
// least 20 candidates before the Go-side re-rank, so a strong-but-old fact
// isn't cut by the raw bm25 order. The fake echoes the pool it was asked for,
// so we assert the store requested the floor, not limit*4.
func TestRqlite_SearchPoolFloor(t *testing.T) {
	f := newFakeRqlite(t)
	var gotPool int
	// Wrap the query handler to capture the pool argument (4th arg of searchSQL).
	f.onSearchPool = func(pool int) { gotPool = pool }
	st := openFake(t, f)
	sc := Scope{tenant: "acme", user: "alice"}
	if _, err := st.Search(context.Background(), sc, "anything", 1); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotPool != 20 {
		t.Fatalf("candidate pool = %d, want the floor 20 (limit 1 * factor 4 = 4 < 20)", gotPool)
	}
}

// TestRqlite_SearchTruncatesToLimit covers Search's len(scored) > limit
// truncation: with more matching facts than the requested limit, only the
// top-ranked `limit` are returned.
func TestRqlite_SearchTruncatesToLimit(t *testing.T) {
	f := newFakeRqlite(t)
	st := openFake(t, f)
	ctx := context.Background()
	sc := Scope{tenant: "acme", user: "alice"}
	now := time.Now()
	for _, id := range []string{"k1", "k2", "k3"} {
		if err := st.Save(ctx, sc, Fact{ID: id, TenantID: "acme", UserID: "alice",
			Content: "shared keyword " + id, Importance: 0.5, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatalf("Save %s: %v", id, err)
		}
	}
	got, err := st.Search(ctx, sc, "keyword", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Search(limit=2) returned %d facts, want 2 (truncated from 3)", len(got))
	}
}

// TestRqlite_SearchScanError covers Search's row-scan error branch: a backend
// that returns a row with a non-numeric timestamp must surface a scan error,
// not a silent zero-value fact.
func TestRqlite_SearchScanError(t *testing.T) {
	f := newFakeRqlite(t)
	f.malformedRows = true
	st := openFake(t, f)
	sc := Scope{tenant: "acme", user: "alice"}
	if _, err := st.Search(context.Background(), sc, "keyword", 5); err == nil || !strings.Contains(err.Error(), "scan") {
		t.Fatalf("Search over a malformed row = %v, want a scan error", err)
	}
}

// TestRqlite_ExportAllScanError covers ExportAll's row-scan error branch.
func TestRqlite_ExportAllScanError(t *testing.T) {
	f := newFakeRqlite(t)
	f.malformedRows = true
	st := openFake(t, f)
	if _, err := st.ExportAll(context.Background()); err == nil || !strings.Contains(err.Error(), "scan") {
		t.Fatalf("ExportAll over a malformed row = %v, want a scan error", err)
	}
}

// TestSQLiteFileBackedWALPath covers OpenSQLite's non-in-memory branch, where a
// file-backed store enables WAL journaling before applying the schema — the
// production path distinct from the ":memory:" single-connection path.
func TestSQLiteFileBackedWALPath(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "facts.db")
	st, err := OpenSQLite(dsn)
	if err != nil {
		t.Fatalf("OpenSQLite(file): %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	sc := mustScope(t, "acme", "alice")
	if err := st.Save(ctx, sc, fact(sc, "f1", "durable keyword on disk")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := st.Search(ctx, sc, "keyword", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].ID != "f1" {
		t.Fatalf("Search over file-backed store = %+v, want the saved fact", got)
	}
}
