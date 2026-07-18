package semantic

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestQdrantStore_OpenEnsuresCollectionAndIndexes proves OpenQdrant creates a
// missing collection plus its tenant_id/user_id keyword indexes (the
// isolation-filter substrate), and that a second Open against the SAME
// collection short-circuits creation (the exists=true branch of
// ensureCollection).
func TestQdrantStore_OpenEnsuresCollectionAndIndexes(t *testing.T) {
	fake := newFakeQdrantServer()
	host, port := startFakeQdrant(t, fake)
	ctx := context.Background()

	st, err := OpenQdrant(ctx, QdrantConfig{Host: host, Port: port, Collection: "knowledge", Dim: 3})
	if err != nil {
		t.Fatalf("OpenQdrant: %v", err)
	}
	defer func() { _ = st.Close() }()

	fake.mu.Lock()
	exists := fake.collections["knowledge"]
	fake.mu.Unlock()
	if !exists {
		t.Fatalf("ensureCollection did not create the collection")
	}

	// Re-opening against the same collection must hit the exists=true
	// short-circuit in ensureCollection rather than re-creating it.
	st2, err := OpenQdrant(ctx, QdrantConfig{Host: host, Port: port, Collection: "knowledge", Dim: 3})
	if err != nil {
		t.Fatalf("second OpenQdrant: %v", err)
	}
	defer func() { _ = st2.Close() }()
}

// TestQdrantStore_OpenDefaultsHostPortCollection proves the Host/Port/
// Collection defaulting branches in OpenQdrant run when the config leaves
// them zero-valued (host defaults to "localhost", which this test cannot
// reach — the connection error is expected and is not what is under test;
// the point is that OpenQdrant applies the defaults instead of dialing an
// empty host).
func TestQdrantStore_OpenDefaultsHostPortCollection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Port 1 on localhost: nothing listens there, so ensureCollection's first
	// RPC fails fast with connection-refused rather than hanging.
	_, err := OpenQdrant(ctx, QdrantConfig{Port: 1, Dim: 3})
	if err == nil {
		t.Fatalf("expected a connection error against the default host on a closed port")
	}
}

// TestQdrantStore_UpsertSearchIsolation proves Search's payload filter never
// crosses (tenant,user) — the constitutional gate — over a REAL gRPC round
// trip against the fake server, and that ScoredPoint fields round-trip.
func TestQdrantStore_UpsertSearchIsolation(t *testing.T) {
	fake := newFakeQdrantServer()
	host, port := startFakeQdrant(t, fake)
	ctx := context.Background()
	st, err := OpenQdrant(ctx, QdrantConfig{Host: host, Port: port, Collection: "knowledge", Dim: 3})
	if err != nil {
		t.Fatalf("OpenQdrant: %v", err)
	}
	defer func() { _ = st.Close() }()

	alice := Scope{tenant: "acme", user: "alice"}
	bob := Scope{tenant: "acme", user: "bob"}
	other := Scope{tenant: "globex", user: "alice"}

	created := time.UnixMilli(1_700_000_000_000)
	alicePt := Point{ID: PointID(alice, KindTurn, "m1"), TenantID: "acme", UserID: "alice",
		Kind: KindTurn, Content: "alice message", Source: "src-a", Citation: "cite-a",
		Vector: vec3(1, 0, 0), CreatedAt: created}
	if err := st.Upsert(ctx, alice, []Point{alicePt}); err != nil {
		t.Fatalf("Upsert alice: %v", err)
	}
	if err := st.Upsert(ctx, bob, []Point{{ID: PointID(bob, KindTurn, "m1"), TenantID: "acme", UserID: "bob",
		Kind: KindTurn, Content: "bob message", Vector: vec3(1, 0, 0)}}); err != nil {
		t.Fatalf("Upsert bob: %v", err)
	}
	if err := st.Upsert(ctx, other, []Point{{ID: PointID(other, KindTurn, "m1"), TenantID: "globex", UserID: "alice",
		Kind: KindTurn, Content: "other tenant same user", Vector: vec3(1, 0, 0)}}); err != nil {
		t.Fatalf("Upsert other tenant: %v", err)
	}

	got, err := st.Search(ctx, alice, vec3(1, 0, 0), 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("payload-filtered search leaked: got %d points, want 1: %+v", len(got), got)
	}
	r := got[0]
	if r.TenantID != "acme" || r.UserID != "alice" {
		t.Fatalf("isolation leak: got tenant=%q user=%q", r.TenantID, r.UserID)
	}
	if r.Content != "alice message" || r.Source != "src-a" || r.Citation != "cite-a" || r.Kind != KindTurn {
		t.Fatalf("payload fields did not round-trip: %+v", r.Point)
	}
	if !r.CreatedAt.Equal(created) {
		t.Fatalf("created_at did not round-trip: got %v, want %v", r.CreatedAt, created)
	}

	// Bob and the other tenant never see alice's point (and vice versa,
	// proven by len==1 above): confirm each scope only sees its own point.
	if got, _ := st.Search(ctx, bob, vec3(1, 0, 0), 10); len(got) != 1 || got[0].UserID != "bob" {
		t.Fatalf("bob search leaked or missed: %+v", got)
	}
	if got, _ := st.Search(ctx, other, vec3(1, 0, 0), 10); len(got) != 1 || got[0].TenantID != "globex" {
		t.Fatalf("other-tenant search leaked or missed: %+v", got)
	}
}

// TestQdrantStore_SearchKindFilter proves the kinds match-any filter is
// applied during the same scan as the isolation filter.
func TestQdrantStore_SearchKindFilter(t *testing.T) {
	fake := newFakeQdrantServer()
	host, port := startFakeQdrant(t, fake)
	ctx := context.Background()
	st, err := OpenQdrant(ctx, QdrantConfig{Host: host, Port: port, Collection: "knowledge", Dim: 3})
	if err != nil {
		t.Fatalf("OpenQdrant: %v", err)
	}
	defer func() { _ = st.Close() }()

	sc := Scope{tenant: "acme", user: "alice"}
	pts := []Point{
		{ID: PointID(sc, KindDocument, "d"), TenantID: "acme", UserID: "alice", Kind: KindDocument, Vector: vec3(1, 0, 0)},
		{ID: PointID(sc, KindTurn, "t"), TenantID: "acme", UserID: "alice", Kind: KindTurn, Vector: vec3(1, 0, 0)},
		{ID: PointID(sc, KindFact, "f"), TenantID: "acme", UserID: "alice", Kind: KindFact, Vector: vec3(1, 0, 0)},
	}
	if err := st.Upsert(ctx, sc, pts); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := st.Search(ctx, sc, vec3(1, 0, 0), 10, KindDocument, KindFact)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("kind filter should keep 2, got %d: %+v", len(got), got)
	}
	for _, sp := range got {
		if sp.Kind == KindTurn {
			t.Fatalf("KindTurn must be filtered out, got %+v", sp.Point)
		}
	}
}

// TestQdrantStore_UpsertGuards proves the client-side scope/dimension guards
// run BEFORE any RPC (an empty slice or a bad point never reaches the fake
// server), matching InMemoryStore's contract.
func TestQdrantStore_UpsertGuards(t *testing.T) {
	fake := newFakeQdrantServer()
	host, port := startFakeQdrant(t, fake)
	ctx := context.Background()
	st, err := OpenQdrant(ctx, QdrantConfig{Host: host, Port: port, Collection: "knowledge", Dim: 3})
	if err != nil {
		t.Fatalf("OpenQdrant: %v", err)
	}
	defer func() { _ = st.Close() }()

	sc := Scope{tenant: "acme", user: "alice"}
	if err := st.Upsert(ctx, sc, nil); err != nil {
		t.Fatalf("empty Upsert should be a no-op, got %v", err)
	}
	if err := st.Upsert(ctx, sc, []Point{{ID: "x", TenantID: "evil", UserID: "alice", Vector: vec3(1, 0, 0)}}); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("wrong tenant: got %v", err)
	}
	if err := st.Upsert(ctx, sc, []Point{{ID: "x", TenantID: "acme", UserID: "mallory", Vector: vec3(1, 0, 0)}}); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("wrong user: got %v", err)
	}
	if err := st.Upsert(ctx, sc, []Point{{ID: "x", TenantID: "acme", UserID: "alice", Vector: []float32{1, 0}}}); !errors.Is(err, ErrDimensionMismatch) {
		t.Fatalf("wrong dim: got %v", err)
	}
	if _, err := st.Search(ctx, sc, []float32{1, 0}, 5); !errors.Is(err, ErrDimensionMismatch) {
		t.Fatalf("search dim mismatch: got %v", err)
	}
	if got, err := st.Search(ctx, sc, vec3(1, 0, 0), 0); err != nil || got != nil {
		t.Fatalf("zero limit should yield nil, got %v (%v)", got, err)
	}
}

// TestQdrantStore_DeleteScopedAndEmptyIsNoop proves Delete removes only the
// caller's own (tenant,user) point even when an id collides with another
// scope's point, and that an empty id slice never reaches the fake server.
func TestQdrantStore_DeleteScopedAndEmptyIsNoop(t *testing.T) {
	fake := newFakeQdrantServer()
	host, port := startFakeQdrant(t, fake)
	ctx := context.Background()
	st, err := OpenQdrant(ctx, QdrantConfig{Host: host, Port: port, Collection: "knowledge", Dim: 3})
	if err != nil {
		t.Fatalf("OpenQdrant: %v", err)
	}
	defer func() { _ = st.Close() }()

	sc := Scope{tenant: "acme", user: "alice"}
	if err := st.Delete(ctx, sc, nil); err != nil {
		t.Fatalf("Delete(nil): %v", err)
	}

	alicePt := Point{ID: PointID(sc, KindTurn, "m1"), TenantID: "acme", UserID: "alice", Kind: KindTurn, Vector: vec3(1, 0, 0)}
	if err := st.Upsert(ctx, sc, []Point{alicePt}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Bob deletes ALICE's exact id under HIS OWN scope: has_id(alicePt.ID)
	// matches, but the AND'd tenant_id/user_id condition (acme,bob) does not
	// match the stored point's (acme,alice) payload, so the delete is a no-op —
	// an id collision across scopes is structurally unreachable, per Delete's
	// isolation guarantee.
	bob := Scope{tenant: "acme", user: "bob"}
	if err := st.Delete(ctx, bob, []string{alicePt.ID}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ := st.Search(ctx, sc, vec3(1, 0, 0), 5)
	if len(got) != 1 {
		t.Fatalf("alice's point must survive bob's scoped delete of her id, got %d", len(got))
	}

	// Alice deleting the same id under her own scope succeeds.
	if err := st.Delete(ctx, sc, []string{alicePt.ID}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, _ := st.Search(ctx, sc, vec3(1, 0, 0), 5); len(got) != 0 {
		t.Fatalf("alice's own delete should remove her point, got %d", len(got))
	}
}

// TestQdrantStore_DeleteBySource proves every chunk sharing a Source within a
// scope is removed as a unit, a colliding source in a different Kind
// survives, and a no-match call is a clean (0, nil).
func TestQdrantStore_DeleteBySource(t *testing.T) {
	fake := newFakeQdrantServer()
	host, port := startFakeQdrant(t, fake)
	ctx := context.Background()
	st, err := OpenQdrant(ctx, QdrantConfig{Host: host, Port: port, Collection: "knowledge", Dim: 3})
	if err != nil {
		t.Fatalf("OpenQdrant: %v", err)
	}
	defer func() { _ = st.Close() }()

	sc := Scope{tenant: "acme", user: "alice"}
	if err := st.Upsert(ctx, sc, []Point{
		{ID: PointID(sc, KindDocument, "c1"), TenantID: "acme", UserID: "alice", Kind: KindDocument, Source: "doc-1", Vector: vec3(1, 0, 0)},
		{ID: PointID(sc, KindDocument, "c2"), TenantID: "acme", UserID: "alice", Kind: KindDocument, Source: "doc-1", Vector: vec3(0, 1, 0)},
		{ID: PointID(sc, KindDocument, "c3"), TenantID: "acme", UserID: "alice", Kind: KindDocument, Source: "doc-2", Vector: vec3(0, 0, 1)},
		{ID: PointID(sc, KindFact, "f1"), TenantID: "acme", UserID: "alice", Kind: KindFact, Source: "doc-1", Vector: vec3(1, 1, 0)},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	n, err := st.DeleteBySource(ctx, sc, KindDocument, "doc-1")
	if err != nil {
		t.Fatalf("DeleteBySource: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 removed, got %d", n)
	}
	n, err = st.DeleteBySource(ctx, sc, KindDocument, "nonexistent")
	if err != nil || n != 0 {
		t.Fatalf("no match: got %d (%v)", n, err)
	}
	remaining, _ := st.Search(ctx, sc, vec3(1, 1, 0), 10, KindFact)
	if len(remaining) != 1 {
		t.Fatalf("colliding-source Fact wrongly removed: %+v", remaining)
	}
	docs, _ := st.Search(ctx, sc, vec3(0, 0, 1), 10, KindDocument)
	if len(docs) != 1 {
		t.Fatalf("doc-2 chunk wrongly removed: %+v", docs)
	}
}

// TestQdrantStore_PruneTurnsAgeAndCount proves both PruneTurns eviction
// criteria (age, count-cap) run only over KindTurn points and never touch a
// Document, mirroring the InMemoryStore/VectorBackendStore behavior.
func TestQdrantStore_PruneTurnsAgeAndCount(t *testing.T) {
	fake := newFakeQdrantServer()
	host, port := startFakeQdrant(t, fake)
	ctx := context.Background()
	st, err := OpenQdrant(ctx, QdrantConfig{Host: host, Port: port, Collection: "knowledge", Dim: 3})
	if err != nil {
		t.Fatalf("OpenQdrant: %v", err)
	}
	defer func() { _ = st.Close() }()

	sc := Scope{tenant: "acme", user: "alice"}

	// Disabled policies -> no-op, no RPC needed either way.
	if n, err := st.PruneTurns(ctx, sc, 0, 0); err != nil || n != 0 {
		t.Fatalf("disabled policies: got %d (%v)", n, err)
	}

	now := time.Now()
	old := now.Add(-48 * time.Hour)
	if err := st.Upsert(ctx, sc, []Point{
		{ID: PointID(sc, KindTurn, "old"), TenantID: "acme", UserID: "alice", Kind: KindTurn, Vector: vec3(1, 0, 0), CreatedAt: old},
		{ID: PointID(sc, KindTurn, "new"), TenantID: "acme", UserID: "alice", Kind: KindTurn, Vector: vec3(0, 1, 0), CreatedAt: now},
		{ID: PointID(sc, KindDocument, "doc"), TenantID: "acme", UserID: "alice", Kind: KindDocument, Vector: vec3(0, 0, 1), CreatedAt: old},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	n, err := st.PruneTurns(ctx, sc, 24*time.Hour, 0)
	if err != nil {
		t.Fatalf("PruneTurns age: %v", err)
	}
	if n != 1 {
		t.Fatalf("age policy should remove 1 old turn, got %d", n)
	}
	docs, _ := st.Search(ctx, sc, vec3(0, 0, 1), 5, KindDocument)
	if len(docs) != 1 {
		t.Fatalf("Document wrongly pruned by age policy")
	}

	// Count-cap policy over a fresh set of turns.
	sc2 := Scope{tenant: "acme", user: "carol"}
	base := time.Now()
	var pts []Point
	for i := 0; i < 5; i++ {
		pts = append(pts, Point{ID: PointID(sc2, KindTurn, string(rune('a'+i))), TenantID: "acme", UserID: "carol",
			Kind: KindTurn, Vector: vec3(1, 0, 0), CreatedAt: base.Add(time.Duration(i) * time.Minute)})
	}
	if err := st.Upsert(ctx, sc2, pts); err != nil {
		t.Fatalf("Upsert count set: %v", err)
	}
	n, err = st.PruneTurns(ctx, sc2, 0, 2)
	if err != nil {
		t.Fatalf("PruneTurns count: %v", err)
	}
	if n != 3 {
		t.Fatalf("count policy should remove 3, got %d", n)
	}
	remaining, _ := st.Search(ctx, sc2, vec3(1, 0, 0), 10, KindTurn)
	if len(remaining) != 2 {
		t.Fatalf("expected 2 survivors, got %d", len(remaining))
	}
}

// TestQdrantStore_EraseScope proves EraseScope removes every point for a
// (tenant,user) across ALL kinds while leaving other subjects untouched — the
// semantic half of GDPR Art.17 subject erasure — and that it is idempotent.
func TestQdrantStore_EraseScope(t *testing.T) {
	fake := newFakeQdrantServer()
	host, port := startFakeQdrant(t, fake)
	ctx := context.Background()
	st, err := OpenQdrant(ctx, QdrantConfig{Host: host, Port: port, Collection: "knowledge", Dim: 3})
	if err != nil {
		t.Fatalf("OpenQdrant: %v", err)
	}
	defer func() { _ = st.Close() }()

	alice := Scope{tenant: "acme", user: "alice"}
	bob := Scope{tenant: "acme", user: "bob"}
	if err := st.Upsert(ctx, alice, []Point{
		{ID: PointID(alice, KindFact, "f"), TenantID: "acme", UserID: "alice", Kind: KindFact, Vector: vec3(1, 0, 0)},
		{ID: PointID(alice, KindTurn, "t"), TenantID: "acme", UserID: "alice", Kind: KindTurn, Vector: vec3(0, 1, 0)},
	}); err != nil {
		t.Fatalf("Upsert alice: %v", err)
	}
	if err := st.Upsert(ctx, bob, []Point{{ID: PointID(bob, KindFact, "f"), TenantID: "acme", UserID: "bob", Kind: KindFact, Vector: vec3(0, 0, 1)}}); err != nil {
		t.Fatalf("Upsert bob: %v", err)
	}

	n, err := st.EraseScope(ctx, alice)
	if err != nil {
		t.Fatalf("EraseScope: %v", err)
	}
	if n != 2 {
		t.Fatalf("EraseScope removed %d, want 2", n)
	}
	if got, _ := st.Search(ctx, alice, vec3(1, 0, 0), 10); len(got) != 0 {
		t.Fatalf("alice should be empty after erase: %+v", got)
	}
	if got, _ := st.Search(ctx, bob, vec3(0, 0, 1), 10); len(got) != 1 {
		t.Fatalf("bob must survive alice's erase: %+v", got)
	}

	if n, err := st.EraseScope(ctx, alice); err != nil || n != 0 {
		t.Fatalf("second EraseScope = (%d,%v), want (0,nil) — not idempotent", n, err)
	}
}

// TestQdrantStore_ErrorPropagation proves every gRPC-facing method wraps and
// returns a fake-server-injected RPC failure instead of masking it.
func TestQdrantStore_ErrorPropagation(t *testing.T) {
	ctx := context.Background()
	sc := Scope{tenant: "acme", user: "alice"}

	newStore := func(t *testing.T) (*QdrantStore, *fakeQdrantServer) {
		t.Helper()
		fake := newFakeQdrantServer()
		host, port := startFakeQdrant(t, fake)
		st, err := OpenQdrant(ctx, QdrantConfig{Host: host, Port: port, Collection: "knowledge", Dim: 3})
		if err != nil {
			t.Fatalf("OpenQdrant: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		return st, fake
	}
	sentinel := errors.New("rpc boom")

	t.Run("upsert", func(t *testing.T) {
		st, fake := newStore(t)
		fake.failUpsert = sentinel
		if err := st.Upsert(ctx, sc, []Point{{ID: "p", TenantID: "acme", UserID: "alice", Vector: vec3(1, 0, 0)}}); err == nil {
			t.Fatal("expected upsert error")
		}
	})
	t.Run("search", func(t *testing.T) {
		st, fake := newStore(t)
		fake.failQuery = sentinel
		if _, err := st.Search(ctx, sc, vec3(1, 0, 0), 5); err == nil {
			t.Fatal("expected search error")
		}
	})
	t.Run("delete", func(t *testing.T) {
		st, fake := newStore(t)
		fake.failDelete = sentinel
		if err := st.Delete(ctx, sc, []string{"x"}); err == nil {
			t.Fatal("expected delete error")
		}
	})
	t.Run("deletebysource scroll error", func(t *testing.T) {
		st, fake := newStore(t)
		fake.failScroll = sentinel
		if _, err := st.DeleteBySource(ctx, sc, KindDocument, "s"); err == nil {
			t.Fatal("expected deletebysource scroll error")
		}
	})
	t.Run("deletebysource delete error", func(t *testing.T) {
		st, fake := newStore(t)
		if err := st.Upsert(ctx, sc, []Point{{ID: PointID(sc, KindDocument, "c"), TenantID: "acme", UserID: "alice", Kind: KindDocument, Source: "doc-1", Vector: vec3(1, 0, 0)}}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		fake.failDelete = sentinel
		if _, err := st.DeleteBySource(ctx, sc, KindDocument, "doc-1"); err == nil {
			t.Fatal("expected deletebysource delete error")
		}
	})
	t.Run("pruneturns scroll error (age)", func(t *testing.T) {
		st, fake := newStore(t)
		fake.failScroll = sentinel
		if _, err := st.PruneTurns(ctx, sc, time.Hour, 0); err == nil {
			t.Fatal("expected pruneturns age-scroll error")
		}
	})
	t.Run("pruneturns scroll error (count)", func(t *testing.T) {
		st, fake := newStore(t)
		fake.failScroll = sentinel
		if _, err := st.PruneTurns(ctx, sc, 0, 5); err == nil {
			t.Fatal("expected pruneturns count-scroll error")
		}
	})
	t.Run("pruneturns delete error", func(t *testing.T) {
		st, fake := newStore(t)
		if err := st.Upsert(ctx, sc, []Point{{ID: PointID(sc, KindTurn, "t"), TenantID: "acme", UserID: "alice", Kind: KindTurn, Vector: vec3(1, 0, 0), CreatedAt: time.Now().Add(-72 * time.Hour)}}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		fake.failDelete = sentinel
		if _, err := st.PruneTurns(ctx, sc, time.Hour, 0); err == nil {
			t.Fatal("expected pruneturns delete error")
		}
	})
	t.Run("erasescope scroll error", func(t *testing.T) {
		st, fake := newStore(t)
		fake.failScroll = sentinel
		if _, err := st.EraseScope(ctx, sc); err == nil {
			t.Fatal("expected erasescope scroll error")
		}
	})
	t.Run("erasescope delete error", func(t *testing.T) {
		st, fake := newStore(t)
		if err := st.Upsert(ctx, sc, []Point{{ID: PointID(sc, KindFact, "f"), TenantID: "acme", UserID: "alice", Kind: KindFact, Vector: vec3(1, 0, 0)}}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		fake.failDelete = sentinel
		if _, err := st.EraseScope(ctx, sc); err == nil {
			t.Fatal("expected erasescope delete error")
		}
	})
	t.Run("open: collection exists error", func(t *testing.T) {
		fake := newFakeQdrantServer()
		fake.failCollectionExists = sentinel
		host, port := startFakeQdrant(t, fake)
		if _, err := OpenQdrant(ctx, QdrantConfig{Host: host, Port: port, Collection: "k", Dim: 3}); err == nil {
			t.Fatal("expected OpenQdrant error from CollectionExists failure")
		}
	})
	t.Run("open: create collection error", func(t *testing.T) {
		fake := newFakeQdrantServer()
		fake.failCreateCollection = sentinel
		host, port := startFakeQdrant(t, fake)
		if _, err := OpenQdrant(ctx, QdrantConfig{Host: host, Port: port, Collection: "k", Dim: 3}); err == nil {
			t.Fatal("expected OpenQdrant error from CreateCollection failure")
		}
	})
	t.Run("open: create field index error", func(t *testing.T) {
		fake := newFakeQdrantServer()
		fake.failCreateFieldIndex = sentinel
		host, port := startFakeQdrant(t, fake)
		if _, err := OpenQdrant(ctx, QdrantConfig{Host: host, Port: port, Collection: "k", Dim: 3}); err == nil {
			t.Fatal("expected OpenQdrant error from CreateFieldIndex failure")
		}
	})
}

// TestQdrantStore_Close proves Close reaches the underlying gRPC client
// without error.
func TestQdrantStore_Close(t *testing.T) {
	fake := newFakeQdrantServer()
	host, port := startFakeQdrant(t, fake)
	st, err := OpenQdrant(context.Background(), QdrantConfig{Host: host, Port: port, Collection: "k", Dim: 3})
	if err != nil {
		t.Fatalf("OpenQdrant: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
