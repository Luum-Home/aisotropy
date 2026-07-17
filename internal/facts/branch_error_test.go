package facts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// --- SQLiteStore error branches via a closed database ---------------------

// openClosedSQLite returns a SQLiteStore whose underlying DB has been closed,
// so every subsequent query/exec/begin fails — the cheapest faithful way to
// drive the store's DB-error branches without a broken schema.
func openClosedSQLite(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return s
}

func TestSQLiteSaveDBError(t *testing.T) {
	s := openClosedSQLite(t)
	sc := mustScope(t, "acme", "alice")
	err := s.Save(context.Background(), sc, fact(sc, "f1", "hello world"))
	if err == nil || !strings.Contains(err.Error(), "facts: save") {
		t.Fatalf("Save on a closed DB = %v, want a wrapped save error", err)
	}
}

func TestSQLiteSearchDBError(t *testing.T) {
	s := openClosedSQLite(t)
	sc := mustScope(t, "acme", "alice")
	_, err := s.Search(context.Background(), sc, "hello", 5)
	if err == nil || !strings.Contains(err.Error(), "facts: search") {
		t.Fatalf("Search on a closed DB = %v, want a wrapped search error", err)
	}
}

func TestSQLiteSupersedeBeginError(t *testing.T) {
	s := openClosedSQLite(t)
	sc := mustScope(t, "acme", "alice")
	err := s.Supersede(context.Background(), sc, "old", fact(sc, "new", "c"))
	if err == nil || !strings.Contains(err.Error(), "begin supersede tx") {
		t.Fatalf("Supersede on a closed DB = %v, want a begin-tx error", err)
	}
}

func TestSQLiteExportAllDBError(t *testing.T) {
	s := openClosedSQLite(t)
	_, err := s.ExportAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "facts: export") {
		t.Fatalf("ExportAll on a closed DB = %v, want a wrapped export error", err)
	}
}

func TestSQLiteDeleteBeginError(t *testing.T) {
	s := openClosedSQLite(t)
	sc := mustScope(t, "acme", "alice")
	err := s.Delete(context.Background(), sc, []string{"f1"})
	if err == nil || !strings.Contains(err.Error(), "begin delete tx") {
		t.Fatalf("Delete on a closed DB = %v, want a begin-tx error", err)
	}
}

// TestSQLiteDeleteEmptyIDs covers the len(ids)==0 short-circuit: an empty id
// slice is a no-op success and must not even open a transaction.
func TestSQLiteDeleteEmptyIDs(t *testing.T) {
	s, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Delete(context.Background(), mustScope(t, "acme", "alice"), nil); err != nil {
		t.Fatalf("Delete(nil ids) = %v, want nil", err)
	}
}

// TestSQLiteMigrateOnClosedDB covers migrate's ExecContext-error branch: a
// schema-apply against a closed handle must surface a wrapped migrate error.
func TestSQLiteMigrateOnClosedDB(t *testing.T) {
	s := openClosedSQLite(t)
	if err := s.migrate(context.Background()); err == nil || !strings.Contains(err.Error(), "migrate schema") {
		t.Fatalf("migrate on a closed DB = %v, want a wrapped migrate error", err)
	}
}

// insertCorruptRow bypasses Save to store a facts row whose importance cell is
// non-numeric text (SQLite's dynamic typing keeps a non-convertible string as
// TEXT under REAL affinity), so a later float64 Scan of that row fails — the
// guard against a corrupt row silently scanning to a zero-value fact. The
// AFTER INSERT trigger indexes the content into FTS so Search can find it.
func insertCorruptRow(t *testing.T, s *SQLiteStore) {
	t.Helper()
	_, err := s.db.ExecContext(context.Background(),
		`INSERT INTO facts (id, tenant_id, user_id, content, importance, created_at, updated_at, superseded_by)
		 VALUES ('bad', 'acme', 'alice', 'corrupt keyword row', 'not-a-number', 0, 0, '')`)
	if err != nil {
		t.Fatalf("seed corrupt row: %v", err)
	}
}

// TestSQLiteSearchScanError covers Search's row-scan error branch.
func TestSQLiteSearchScanError(t *testing.T) {
	s, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	insertCorruptRow(t, s)
	if _, err := s.Search(context.Background(), mustScope(t, "acme", "alice"), "keyword", 5); err == nil || !strings.Contains(err.Error(), "scan") {
		t.Fatalf("Search over a corrupt row = %v, want a scan error", err)
	}
}

// TestSQLiteExportAllScanError covers ExportAll's row-scan error branch.
func TestSQLiteExportAllScanError(t *testing.T) {
	s, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	insertCorruptRow(t, s)
	if _, err := s.ExportAll(context.Background()); err == nil || !strings.Contains(err.Error(), "scan") {
		t.Fatalf("ExportAll over a corrupt row = %v, want a scan error", err)
	}
}

// TestScoreClockSkewGuard covers score's future-timestamp guard: a fact whose
// UpdatedAt is in the future (negative age) is treated as "now" (recency 1),
// so a clock skew can never inflate a score toward +inf via a negative decay.
func TestScoreClockSkewGuard(t *testing.T) {
	// bm25 = -1 => relevance 1; importance 0.5; age negative (future).
	got := score(-1.0, 0.5, -time.Hour)
	if got != 0.5 {
		t.Fatalf("score(future age) = %v, want 0.5 (recency clamped to 1)", got)
	}
}

// --- HeuristicExtractor branches ------------------------------------------

// TestExtractRuleMatchButEmptyContent drives the branch where a rule matches
// but cleanContent strips the capture to empty: the extractor must skip that
// rule (continue) rather than emit an empty fact.
func TestExtractRuleMatchButEmptyContent(t *testing.T) {
	sc := mustScope(t, "acme", "alice")
	// "remember that !!!" matches the "remember that" rule, whose capture
	// " !!!" cleans to "" — forcing the continue — before the looser
	// "remember" rule yields the usable "that".
	got := HeuristicExtractor{}.Extract(sc, Turn{UserMessage: "remember that !!!"})
	if len(got) != 1 || got[0].Content != "that" {
		t.Fatalf("Extract = %+v, want a single fact with content %q via the fallback rule", got, "that")
	}
}

// TestExtractCapsContentLength covers cleanContent's length cap: a fact
// distilled from an over-long statement is truncated to maxFactContent.
func TestExtractCapsContentLength(t *testing.T) {
	sc := mustScope(t, "acme", "alice")
	long := "remember that " + strings.Repeat("a", maxFactContent+50)
	got := HeuristicExtractor{}.Extract(sc, Turn{UserMessage: long})
	if len(got) != 1 {
		t.Fatalf("Extract returned %d facts, want 1", len(got))
	}
	if len(got[0].Content) != maxFactContent {
		t.Fatalf("content length = %d, want the cap %d", len(got[0].Content), maxFactContent)
	}
}

// --- sortScoredDesc tie-break (backend.go) --------------------------------

// TestSortScoredDescUpdatedAtTieBreak covers the equal-score tie-break: with
// identical scores, the more recently updated fact must sort first.
func TestSortScoredDescUpdatedAtTieBreak(t *testing.T) {
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	fs := []ScoredFact{
		{Fact: Fact{ID: "a", UpdatedAt: older}, Score: 1.0},
		{Fact: Fact{ID: "b", UpdatedAt: newer}, Score: 1.0},
	}
	sortScoredDesc(fs)
	if fs[0].ID != "b" {
		t.Fatalf("tie-break order = %s,%s, want the newer fact (b) first", fs[0].ID, fs[1].ID)
	}
}

// --- RoutingStore branches ------------------------------------------------

// errStore is a Store double whose ExportAll and Close can be made to fail,
// for RoutingStore's cross-store aggregation error paths.
type errStore struct {
	exportErr error
	closeErr  error
}

func (e *errStore) Save(context.Context, Scope, Fact) error { return nil }
func (e *errStore) Search(context.Context, Scope, string, int) ([]ScoredFact, error) {
	return nil, nil
}
func (e *errStore) Supersede(context.Context, Scope, string, Fact) error { return nil }
func (e *errStore) ExportAll(context.Context) ([]Fact, error)            { return nil, e.exportErr }
func (e *errStore) Delete(context.Context, Scope, []string) error        { return nil }
func (e *errStore) Close() error                                         { return e.closeErr }

// TestProvisionedOpenAfterShut covers the closed-holder guard in
// provisioned.open, and shut()'s nil return on a never-opened holder.
func TestProvisionedOpenAfterShut(t *testing.T) {
	p := &provisioned{}
	// shut before any open: the store was never opened, so shut is a clean nil.
	if err := p.shut(); err != nil {
		t.Fatalf("shut on a fresh holder = %v, want nil", err)
	}
	// A subsequent open must fail closed rather than run the factory.
	factoryRan := false
	_, err := p.open("res", func(string) (Store, error) {
		factoryRan = true
		return &errStore{}, nil
	})
	if !errors.Is(err, ErrRoutingStoreClosed) {
		t.Fatalf("open after shut = %v, want ErrRoutingStoreClosed", err)
	}
	if factoryRan {
		t.Fatal("factory ran after the holder was shut")
	}
}

// TestNewRoutingStoreFactoryError covers NewRoutingStore's provisioning-failure
// branch: a factory error during up-front provisioning tears down and returns.
func TestNewRoutingStoreFactoryError(t *testing.T) {
	want := errors.New("cannot open resource")
	_, err := NewRoutingStore(fakeRouter{}, "facts", nil, func(string) (Store, error) {
		return nil, want
	})
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("NewRoutingStore with a failing factory = %v, want wrap of %v", err, want)
	}
}

// TestRoutingStoreDispatchAfterClose covers Supersede/Delete's storeFor-error
// propagation: once the RoutingStore is closed, dispatch fails with
// ErrRoutingStoreClosed instead of reaching a shut-down backend.
func TestRoutingStoreDispatchAfterClose(t *testing.T) {
	rs, err := NewRoutingStore(fakeRouter{}, "facts", nil, memoryFactory)
	if err != nil {
		t.Fatal(err)
	}
	if err := rs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	sc := mustScope(t, "acme", "alice")
	if err := rs.Supersede(context.Background(), sc, "old", fact(sc, "n", "c")); !errors.Is(err, ErrRoutingStoreClosed) {
		t.Fatalf("Supersede after Close = %v, want ErrRoutingStoreClosed", err)
	}
	if err := rs.Delete(context.Background(), sc, []string{"x"}); !errors.Is(err, ErrRoutingStoreClosed) {
		t.Fatalf("Delete after Close = %v, want ErrRoutingStoreClosed", err)
	}
}

// TestRoutingStoreExportAllPropagatesStoreError covers ExportAll's per-store
// error branch: if any provisioned backend's ExportAll fails, the union fails.
func TestRoutingStoreExportAllPropagatesStoreError(t *testing.T) {
	want := errors.New("backend export down")
	rs, err := NewRoutingStore(fakeRouter{}, "facts", nil, func(string) (Store, error) {
		return &errStore{exportErr: want}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rs.Close() })
	if _, err := rs.ExportAll(context.Background()); err == nil || !errors.Is(err, want) {
		t.Fatalf("ExportAll = %v, want wrap of %v", err, want)
	}
}

// TestRoutingStoreCloseAggregatesError covers Close's first-error aggregation:
// a backend whose Close fails surfaces that error from RoutingStore.Close.
func TestRoutingStoreCloseAggregatesError(t *testing.T) {
	want := errors.New("backend close failed")
	rs, err := NewRoutingStore(fakeRouter{}, "facts", nil, func(string) (Store, error) {
		return &errStore{closeErr: want}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rs.Close(); err == nil || !errors.Is(err, want) {
		t.Fatalf("Close = %v, want the backend close error %v", err, want)
	}
}
