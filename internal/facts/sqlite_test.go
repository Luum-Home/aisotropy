package facts

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fixedClock returns a store whose clock is pinned to base, plus a setter to
// advance it, so recency-dependent behavior is deterministic (no sleeps).
func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func saveAt(t *testing.T, st *SQLiteStore, at time.Time, sc Scope, f Fact) {
	t.Helper()
	prev := st.now
	st.now = func() time.Time { return at }
	defer func() { st.now = prev }()
	if err := st.Save(context.Background(), sc, f); err != nil {
		t.Fatalf("Save(%q): %v", f.ID, err)
	}
}

func TestSaveAndSearchFTS5Match(t *testing.T) {
	st := newTestStore(t)
	sc := Scope{TenantID: "acme", UserID: "alice"}
	f := Fact{ID: "f1", TenantID: "acme", UserID: "alice",
		Content: "the user is refactoring the billing database", Importance: 0.5}
	if err := st.Save(context.Background(), sc, f); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Porter stemming: querying "refactor" must match stored "refactoring".
	got, err := st.Search(context.Background(), sc, "refactor the DB", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].ID != "f1" {
		t.Fatalf("Search = %+v, want single f1", got)
	}
	if got[0].Score <= 0 {
		t.Fatalf("Score = %v, want > 0", got[0].Score)
	}
}

func TestSearchEmptyQueryReturnsNothing(t *testing.T) {
	st := newTestStore(t)
	sc := Scope{TenantID: "acme", UserID: "alice"}
	_ = st.Save(context.Background(), sc, Fact{ID: "f1", TenantID: "acme", UserID: "alice", Content: "hello world", Importance: 0.5})

	for _, q := range []string{"", "   ", "!?.,", "a"} { // "a" is below the 2-char token floor
		got, err := st.Search(context.Background(), sc, q, 5)
		if err != nil {
			t.Fatalf("Search(%q): %v", q, err)
		}
		if len(got) != 0 {
			t.Fatalf("Search(%q) = %+v, want empty", q, got)
		}
	}
}

func TestSearchRanksByImportance(t *testing.T) {
	st := newTestStore(t)
	sc := Scope{TenantID: "acme", UserID: "alice"}
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }

	// Two equally-fresh facts matching "database"; higher importance must win.
	saveAt(t, st, now, sc, Fact{ID: "low", TenantID: "acme", UserID: "alice", Content: "database note one", Importance: 0.2})
	saveAt(t, st, now, sc, Fact{ID: "high", TenantID: "acme", UserID: "alice", Content: "database note two", Importance: 0.9})

	got, err := st.Search(context.Background(), sc, "database", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d facts, want 2", len(got))
	}
	if got[0].ID != "high" {
		t.Fatalf("top = %q, want high-importance fact first", got[0].ID)
	}
}

func TestSearchRanksByRecency(t *testing.T) {
	st := newTestStore(t)
	sc := Scope{TenantID: "acme", UserID: "alice"}
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)

	// Equal importance; "old" updated two half-lives ago (recency 0.25),
	// "fresh" updated now (recency 1.0). Fresh must rank first.
	saveAt(t, st, now.Add(-2*recencyHalfLife), sc, Fact{ID: "old", TenantID: "acme", UserID: "alice", Content: "kubernetes cluster note", Importance: 0.5})
	saveAt(t, st, now, sc, Fact{ID: "fresh", TenantID: "acme", UserID: "alice", Content: "kubernetes cluster note also", Importance: 0.5})

	st.now = func() time.Time { return now }
	got, err := st.Search(context.Background(), sc, "kubernetes", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 || got[0].ID != "fresh" {
		t.Fatalf("got %+v, want 'fresh' ranked first", got)
	}
}

func TestSearchRespectsLimit(t *testing.T) {
	st := newTestStore(t)
	sc := Scope{TenantID: "acme", UserID: "alice"}
	for i, c := range []string{"alpha signal", "alpha noise", "alpha trace", "alpha beam"} {
		id := "f" + string(rune('0'+i))
		if err := st.Save(context.Background(), sc, Fact{ID: id, TenantID: "acme", UserID: "alice", Content: c, Importance: 0.5}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.Search(context.Background(), sc, "alpha", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (limit)", len(got))
	}
}

// TestTenantAndUserIsolation is the constitutional-gate test: no query ever
// returns another tenant's or another user's facts.
func TestTenantAndUserIsolation(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	mk := func(tenant, user, id string) {
		sc := Scope{TenantID: tenant, UserID: user}
		if err := st.Save(ctx, sc, Fact{ID: id, TenantID: tenant, UserID: user, Content: "shared secret keyword", Importance: 0.5}); err != nil {
			t.Fatalf("Save %s: %v", id, err)
		}
	}
	mk("acme", "alice", "a-alice")
	mk("acme", "bob", "a-bob")
	mk("globex", "alice", "g-alice")

	got, err := st.Search(ctx, Scope{TenantID: "acme", UserID: "alice"}, "secret keyword", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].ID != "a-alice" {
		t.Fatalf("isolation leak: got %+v, want only a-alice", got)
	}
}

func TestSaveScopeMismatchRejected(t *testing.T) {
	st := newTestStore(t)
	// Fact claims a different tenant than the operation scope.
	err := st.Save(context.Background(), Scope{TenantID: "acme", UserID: "alice"},
		Fact{ID: "x", TenantID: "globex", UserID: "alice", Content: "hi", Importance: 0.5})
	if !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("err = %v, want ErrScopeMismatch", err)
	}
}

func TestSaveIsIdempotentUpsert(t *testing.T) {
	st := newTestStore(t)
	sc := Scope{TenantID: "acme", UserID: "alice"}
	ctx := context.Background()
	base := Fact{ID: "dup", TenantID: "acme", UserID: "alice", Content: "unique marker token", Importance: 0.3}
	if err := st.Save(ctx, sc, base); err != nil {
		t.Fatal(err)
	}
	base.Content = "unique marker token updated"
	base.Importance = 0.9
	if err := st.Save(ctx, sc, base); err != nil {
		t.Fatal(err)
	}
	got, err := st.Search(ctx, sc, "marker", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1 (upsert must not duplicate)", len(got))
	}
	if got[0].Importance != 0.9 || got[0].Content != "unique marker token updated" {
		t.Fatalf("row not updated: %+v", got[0].Fact)
	}
}

// TestSupersede is the Phase 3 DoD supersede test: a new fact archives the old
// one, which then disappears from Search while the new one is returned.
func TestSupersede(t *testing.T) {
	st := newTestStore(t)
	sc := Scope{TenantID: "acme", UserID: "alice"}
	ctx := context.Background()

	old := Fact{ID: "pref-old", TenantID: "acme", UserID: "alice", Content: "prefers light theme", Importance: 0.7}
	if err := st.Save(ctx, sc, old); err != nil {
		t.Fatal(err)
	}
	newF := Fact{ID: "pref-new", TenantID: "acme", UserID: "alice", Content: "prefers dark theme", Importance: 0.7}
	if err := st.Supersede(ctx, sc, "pref-old", newF); err != nil {
		t.Fatalf("Supersede: %v", err)
	}

	// The old fact must no longer surface.
	got, err := st.Search(ctx, sc, "theme", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "pref-new" {
		t.Fatalf("after supersede got %+v, want only pref-new", got)
	}

	// Superseding a non-existent (or already-superseded) fact fails cleanly.
	if err := st.Supersede(ctx, sc, "pref-old", Fact{ID: "z", TenantID: "acme", UserID: "alice", Content: "x", Importance: 0.5}); !errors.Is(err, ErrFactNotFound) {
		t.Fatalf("re-supersede err = %v, want ErrFactNotFound", err)
	}
}

func TestSupersedeIsScopeIsolated(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	scA := Scope{TenantID: "acme", UserID: "alice"}
	if err := st.Save(ctx, scA, Fact{ID: "f", TenantID: "acme", UserID: "alice", Content: "a fact", Importance: 0.5}); err != nil {
		t.Fatal(err)
	}
	// A different tenant cannot supersede acme/alice's fact.
	scB := Scope{TenantID: "globex", UserID: "alice"}
	err := st.Supersede(ctx, scB, "f", Fact{ID: "g", TenantID: "globex", UserID: "alice", Content: "hijack", Importance: 0.9})
	if !errors.Is(err, ErrFactNotFound) {
		t.Fatalf("cross-tenant supersede err = %v, want ErrFactNotFound", err)
	}
}
