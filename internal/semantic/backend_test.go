package semantic

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeVectorBackend is a tenant-UNAWARE, in-memory VectorBackend used to
// exercise the sealed scopedVectorStore decorator (backend.go) without a real
// vector engine. It stores items under an opaque namespace exactly as a
// community backend would, scores search hits by cosine similarity (the same
// metric the built-in InMemoryStore uses), and offers per-method error
// injection so the decorator's error branches run with the stdlib only.
type fakeVectorBackend struct {
	data map[string]map[string]BackendVector // ns -> id -> item

	upsertErr error
	searchErr error
	deleteErr error
	scanErr   error
	closeErr  error
	closed    bool

	// unsorted, when true, makes Search return hits in reverse-similarity order
	// so the decorator's defensive re-sort is exercised.
	unsorted bool
}

func newFakeVectorBackend() *fakeVectorBackend {
	return &fakeVectorBackend{data: map[string]map[string]BackendVector{}}
}

func (b *fakeVectorBackend) Upsert(_ context.Context, ns string, items []BackendVector) error {
	if b.upsertErr != nil {
		return b.upsertErr
	}
	m, ok := b.data[ns]
	if !ok {
		m = map[string]BackendVector{}
		b.data[ns] = m
	}
	for _, it := range items {
		payload := map[string]string{}
		for k, v := range it.Payload {
			payload[k] = v
		}
		vec := append([]float32(nil), it.Vector...)
		m[it.ID] = BackendVector{ID: it.ID, Vector: vec, Payload: payload}
	}
	return nil
}

func (b *fakeVectorBackend) Search(_ context.Context, ns string, query []float32, k int) ([]BackendHit, error) {
	if b.searchErr != nil {
		return nil, b.searchErr
	}
	var hits []BackendHit
	for _, it := range b.data[ns] {
		hits = append(hits, BackendHit{BackendVector: it, Score: cosine(query, it.Vector)})
	}
	sort.Slice(hits, func(i, j int) bool {
		if b.unsorted {
			return hits[i].Score < hits[j].Score // deliberately wrong order
		}
		return hits[i].Score > hits[j].Score
	})
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

func (b *fakeVectorBackend) Delete(_ context.Context, ns string, ids []string) error {
	if b.deleteErr != nil {
		return b.deleteErr
	}
	m := b.data[ns]
	for _, id := range ids {
		delete(m, id)
	}
	return nil
}

func (b *fakeVectorBackend) Scan(_ context.Context, ns string) ([]BackendVector, error) {
	if b.scanErr != nil {
		return nil, b.scanErr
	}
	var out []BackendVector
	for _, it := range b.data[ns] {
		out = append(out, it)
	}
	return out, nil
}

func (b *fakeVectorBackend) Close() error {
	b.closed = true
	return b.closeErr
}

// unit vectors of dimension 3 for deterministic cosine ordering.
func vec3(a, c, d float32) []float32 { return []float32{a, c, d} }

func TestVectorBackendStore_UpsertSearchRoundTrip(t *testing.T) {
	b := newFakeVectorBackend()
	s := NewVectorBackendStore(b, 3)
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()

	created := time.UnixMilli(1_700_000_000_000)
	p := Point{ID: "p1", TenantID: "acme", UserID: "alice", Kind: KindDocument,
		Content: "hello world", Source: "doc-7", Citation: "manual.pdf",
		Vector: vec3(1, 0, 0), CreatedAt: created}
	if err := s.Upsert(ctx, sc, []Point{p}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := s.Search(ctx, sc, vec3(1, 0, 0), 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Search returned %d, want 1", len(got))
	}
	r := got[0].Point
	if r.TenantID != "acme" || r.UserID != "alice" {
		t.Fatalf("identity must be reattached from scope: %+v", r)
	}
	if r.Kind != KindDocument || r.Content != "hello world" || r.Source != "doc-7" || r.Citation != "manual.pdf" {
		t.Fatalf("payload fields not round-tripped: %+v", r)
	}
	if !r.CreatedAt.Equal(created) {
		t.Fatalf("created_at not round-tripped: %v", r.CreatedAt)
	}
	if got[0].Score < 0.999 {
		t.Fatalf("identical vector should score ~1, got %v", got[0].Score)
	}
}

func TestVectorBackendStore_UpsertScopeAndDimErrors(t *testing.T) {
	b := newFakeVectorBackend()
	s := NewVectorBackendStore(b, 3)
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()

	// Wrong tenant.
	if err := s.Upsert(ctx, sc, []Point{{ID: "x", TenantID: "evil", UserID: "alice", Vector: vec3(1, 0, 0)}}); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("wrong tenant: got %v", err)
	}
	// Wrong user.
	if err := s.Upsert(ctx, sc, []Point{{ID: "x", TenantID: "acme", UserID: "mallory", Vector: vec3(1, 0, 0)}}); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("wrong user: got %v", err)
	}
	// Wrong dimension.
	if err := s.Upsert(ctx, sc, []Point{{ID: "x", TenantID: "acme", UserID: "alice", Vector: []float32{1, 0}}}); !errors.Is(err, ErrDimensionMismatch) {
		t.Fatalf("wrong dim: got %v", err)
	}
}

func TestVectorBackendStore_UpsertEmptyIsNoop(t *testing.T) {
	b := newFakeVectorBackend()
	b.upsertErr = errors.New("must not be called")
	s := NewVectorBackendStore(b, 3)
	// No points -> the decorator must not reach the backend.
	if err := s.Upsert(context.Background(), Scope{tenant: "t", user: "u"}, nil); err != nil {
		t.Fatalf("empty Upsert should be a no-op, got %v", err)
	}
}

func TestVectorBackendStore_SearchDimAndLimitGuards(t *testing.T) {
	s := NewVectorBackendStore(newFakeVectorBackend(), 3)
	sc := Scope{tenant: "t", user: "u"}
	if _, err := s.Search(context.Background(), sc, []float32{1, 0}, 5); !errors.Is(err, ErrDimensionMismatch) {
		t.Fatalf("query dim mismatch: got %v", err)
	}
	if got, err := s.Search(context.Background(), sc, vec3(1, 0, 0), 0); err != nil || got != nil {
		t.Fatalf("zero limit should yield nil, got %v (%v)", got, err)
	}
}

func TestVectorBackendStore_SearchKindFilter(t *testing.T) {
	b := newFakeVectorBackend()
	s := NewVectorBackendStore(b, 3)
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()

	pts := []Point{
		{ID: "doc", TenantID: "acme", UserID: "alice", Kind: KindDocument, Content: "doc", Vector: vec3(1, 0, 0)},
		{ID: "turn", TenantID: "acme", UserID: "alice", Kind: KindTurn, Content: "turn", Vector: vec3(1, 0, 0)},
		{ID: "fact", TenantID: "acme", UserID: "alice", Kind: KindFact, Content: "fact", Vector: vec3(1, 0, 0)},
	}
	if err := s.Upsert(ctx, sc, pts); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := s.Search(ctx, sc, vec3(1, 0, 0), 10, KindDocument, KindFact)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, sp := range got {
		if sp.Kind == KindTurn {
			t.Fatalf("KindTurn must be filtered out, got %+v", sp.Point)
		}
	}
	if len(got) != 2 {
		t.Fatalf("kind filter should keep 2, got %d", len(got))
	}
}

func TestVectorBackendStore_SearchDefensiveResortAndTrim(t *testing.T) {
	b := newFakeVectorBackend()
	b.unsorted = true // backend returns hits in the WRONG order
	s := NewVectorBackendStore(b, 3)
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()

	// Three points at varying similarity to the query vec3(1,0,0).
	_ = s.Upsert(ctx, sc, []Point{
		{ID: "near", TenantID: "acme", UserID: "alice", Kind: KindTurn, Vector: vec3(1, 0, 0)},
		{ID: "mid", TenantID: "acme", UserID: "alice", Kind: KindTurn, Vector: vec3(1, 1, 0)},
		{ID: "far", TenantID: "acme", UserID: "alice", Kind: KindTurn, Vector: vec3(0, 1, 0)},
	})
	// A kind filter forces the decorator to over-fetch (k = limit*factor), so
	// the backend returns all three (mis-ordered) and the decorator's own
	// re-sort + trim is what produces the final top-2.
	got, err := s.Search(ctx, sc, vec3(1, 0, 0), 2, KindTurn)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("trim to limit failed: got %d", len(got))
	}
	if got[0].ID != "near" {
		t.Fatalf("defensive re-sort failed: highest-similarity should be first, got %s", got[0].ID)
	}
	if got[0].Score < got[1].Score {
		t.Fatalf("results not in descending score order: %v", got)
	}
}

func TestVectorBackendStore_DeleteAndClose(t *testing.T) {
	b := newFakeVectorBackend()
	s := NewVectorBackendStore(b, 3)
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()

	// Empty ids: must not reach the backend Delete.
	if err := s.Delete(ctx, sc, nil); err != nil {
		t.Fatalf("Delete(nil): %v", err)
	}
	_ = s.Upsert(ctx, sc, []Point{{ID: "d", TenantID: "acme", UserID: "alice", Kind: KindTurn, Vector: vec3(1, 0, 0)}})
	if err := s.Delete(ctx, sc, []string{"d"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ := s.Search(ctx, sc, vec3(1, 0, 0), 5)
	if len(got) != 0 {
		t.Fatalf("point not deleted, found %d", len(got))
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !b.closed {
		t.Fatalf("Close did not reach backend")
	}
}

func TestVectorBackendStore_DeleteBySource(t *testing.T) {
	b := newFakeVectorBackend()
	s := NewVectorBackendStore(b, 3)
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()

	// Two chunks of doc-1, one chunk of doc-2, one Fact with a colliding source.
	_ = s.Upsert(ctx, sc, []Point{
		{ID: "c1", TenantID: "acme", UserID: "alice", Kind: KindDocument, Source: "doc-1", Vector: vec3(1, 0, 0)},
		{ID: "c2", TenantID: "acme", UserID: "alice", Kind: KindDocument, Source: "doc-1", Vector: vec3(0, 1, 0)},
		{ID: "c3", TenantID: "acme", UserID: "alice", Kind: KindDocument, Source: "doc-2", Vector: vec3(0, 0, 1)},
		{ID: "f1", TenantID: "acme", UserID: "alice", Kind: KindFact, Source: "doc-1", Vector: vec3(1, 1, 0)},
	})
	n, err := s.DeleteBySource(ctx, sc, KindDocument, "doc-1")
	if err != nil {
		t.Fatalf("DeleteBySource: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 removed, got %d", n)
	}
	// No-match returns (0, nil).
	n, err = s.DeleteBySource(ctx, sc, KindDocument, "nonexistent")
	if err != nil || n != 0 {
		t.Fatalf("no match: got %d (%v)", n, err)
	}
	// The Fact with the colliding source must still be present.
	remaining, _ := s.Search(ctx, sc, vec3(1, 1, 0), 10, KindFact)
	if len(remaining) != 1 || remaining[0].ID != "f1" {
		t.Fatalf("colliding-source Fact wrongly removed: %+v", remaining)
	}
}

// TestVectorBackendStore_EraseScope proves EraseScope (the sealed decorator's
// GDPR Art.17 primitive over a community VectorBackend) removes every point
// in sc's namespace, leaves other scopes untouched, and is idempotent.
func TestVectorBackendStore_EraseScope(t *testing.T) {
	b := newFakeVectorBackend()
	s := NewVectorBackendStore(b, 3)
	eraser, ok := s.(ScopeEraser)
	if !ok {
		t.Fatalf("scopedVectorStore must implement ScopeEraser")
	}
	sc := Scope{tenant: "acme", user: "alice"}
	other := Scope{tenant: "acme", user: "bob"}
	ctx := context.Background()

	if err := s.Upsert(ctx, sc, []Point{
		{ID: "a1", TenantID: "acme", UserID: "alice", Kind: KindFact, Vector: vec3(1, 0, 0)},
		{ID: "a2", TenantID: "acme", UserID: "alice", Kind: KindTurn, Vector: vec3(0, 1, 0)},
	}); err != nil {
		t.Fatalf("Upsert alice: %v", err)
	}
	if err := s.Upsert(ctx, other, []Point{{ID: "b1", TenantID: "acme", UserID: "bob", Kind: KindFact, Vector: vec3(0, 0, 1)}}); err != nil {
		t.Fatalf("Upsert bob: %v", err)
	}

	n, err := eraser.EraseScope(ctx, sc)
	if err != nil {
		t.Fatalf("EraseScope: %v", err)
	}
	if n != 2 {
		t.Fatalf("EraseScope removed %d, want 2", n)
	}
	if got, _ := s.Search(ctx, sc, vec3(1, 0, 0), 10); len(got) != 0 {
		t.Fatalf("alice should be empty after erase, got %d: %+v", len(got), got)
	}
	if got, _ := s.Search(ctx, other, vec3(0, 0, 1), 10); len(got) != 1 {
		t.Fatalf("bob should be untouched by alice's erase, got %d", len(got))
	}

	// Idempotent: a second erase of an already-empty scope removes nothing.
	if n, err := eraser.EraseScope(ctx, sc); err != nil || n != 0 {
		t.Fatalf("second EraseScope = (%d,%v), want (0,nil)", n, err)
	}
}

// TestVectorBackendStore_EraseScopeErrors covers EraseScope's Scan and Delete
// error-propagation branches, mirroring the other maintenance-op error tests.
func TestVectorBackendStore_EraseScopeErrors(t *testing.T) {
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()
	sentinel := errors.New("erase boom")

	t.Run("scan error", func(t *testing.T) {
		b := newFakeVectorBackend()
		b.scanErr = sentinel
		s := NewVectorBackendStore(b, 3).(ScopeEraser)
		if _, err := s.EraseScope(ctx, sc); !errors.Is(err, sentinel) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("delete error", func(t *testing.T) {
		b := newFakeVectorBackend()
		s := NewVectorBackendStore(b, 3)
		if err := s.Upsert(ctx, sc, []Point{{ID: "x", TenantID: "acme", UserID: "alice", Kind: KindFact, Vector: vec3(1, 0, 0)}}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		b.deleteErr = sentinel
		if _, err := s.(ScopeEraser).EraseScope(ctx, sc); !errors.Is(err, sentinel) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestVectorBackendStore_PruneTurns(t *testing.T) {
	b := newFakeVectorBackend()
	s := NewVectorBackendStore(b, 3)
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()

	// Both policies disabled -> no-op.
	if n, err := s.PruneTurns(ctx, sc, 0, 0); err != nil || n != 0 {
		t.Fatalf("disabled policies: got %d (%v)", n, err)
	}

	now := time.Now()
	old := now.Add(-48 * time.Hour)
	_ = s.Upsert(ctx, sc, []Point{
		{ID: "t-old", TenantID: "acme", UserID: "alice", Kind: KindTurn, Vector: vec3(1, 0, 0), CreatedAt: old},
		{ID: "t-new", TenantID: "acme", UserID: "alice", Kind: KindTurn, Vector: vec3(0, 1, 0), CreatedAt: now},
		{ID: "doc", TenantID: "acme", UserID: "alice", Kind: KindDocument, Vector: vec3(0, 0, 1), CreatedAt: old},
	})

	// Age policy: evict turns older than 24h. The Document, though old, is never touched.
	n, err := s.PruneTurns(ctx, sc, 24*time.Hour, 0)
	if err != nil {
		t.Fatalf("PruneTurns age: %v", err)
	}
	if n != 1 {
		t.Fatalf("age policy should remove 1 old turn, got %d", n)
	}
	// Document survives.
	docs, _ := s.Search(ctx, sc, vec3(0, 0, 1), 5, KindDocument)
	if len(docs) != 1 {
		t.Fatalf("Document wrongly pruned")
	}
}

func TestVectorBackendStore_PruneTurnsByCount(t *testing.T) {
	b := newFakeVectorBackend()
	s := NewVectorBackendStore(b, 3)
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()

	base := time.Now()
	var pts []Point
	for i := 0; i < 5; i++ {
		pts = append(pts, Point{ID: "t" + strconv.Itoa(i), TenantID: "acme", UserID: "alice",
			Kind: KindTurn, Vector: vec3(1, 0, 0), CreatedAt: base.Add(time.Duration(i) * time.Minute)})
	}
	_ = s.Upsert(ctx, sc, pts)

	// Keep only the 2 most recent -> 3 evicted.
	n, err := s.PruneTurns(ctx, sc, 0, 2)
	if err != nil {
		t.Fatalf("PruneTurns count: %v", err)
	}
	if n != 3 {
		t.Fatalf("count policy should remove 3, got %d", n)
	}
	remaining, _ := s.Search(ctx, sc, vec3(1, 0, 0), 10, KindTurn)
	if len(remaining) != 2 {
		t.Fatalf("expected 2 survivors, got %d", len(remaining))
	}
	// Survivors are the newest two (t3, t4).
	ids := map[string]bool{}
	for _, sp := range remaining {
		ids[sp.ID] = true
	}
	if !ids["t3"] || !ids["t4"] {
		t.Fatalf("wrong survivors: %v", ids)
	}
}

func TestVectorBackendStore_BackendErrorsPropagate(t *testing.T) {
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()
	sentinel := errors.New("backend boom")

	t.Run("upsert error", func(t *testing.T) {
		b := newFakeVectorBackend()
		b.upsertErr = sentinel
		if err := NewVectorBackendStore(b, 3).Upsert(ctx, sc, []Point{{ID: "p", TenantID: "acme", UserID: "alice", Vector: vec3(1, 0, 0)}}); !errors.Is(err, sentinel) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("search error", func(t *testing.T) {
		b := newFakeVectorBackend()
		b.searchErr = sentinel
		if _, err := NewVectorBackendStore(b, 3).Search(ctx, sc, vec3(1, 0, 0), 5); !errors.Is(err, sentinel) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("delete error", func(t *testing.T) {
		b := newFakeVectorBackend()
		b.deleteErr = sentinel
		if err := NewVectorBackendStore(b, 3).Delete(ctx, sc, []string{"x"}); !errors.Is(err, sentinel) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("deletebysource scan error", func(t *testing.T) {
		b := newFakeVectorBackend()
		b.scanErr = sentinel
		if _, err := NewVectorBackendStore(b, 3).DeleteBySource(ctx, sc, KindDocument, "s"); !errors.Is(err, sentinel) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("deletebysource delete error", func(t *testing.T) {
		b := newFakeVectorBackend()
		s := NewVectorBackendStore(b, 3)
		_ = s.Upsert(ctx, sc, []Point{{ID: "c", TenantID: "acme", UserID: "alice", Kind: KindDocument, Source: "doc-1", Vector: vec3(1, 0, 0)}})
		b.deleteErr = sentinel
		if _, err := s.DeleteBySource(ctx, sc, KindDocument, "doc-1"); !errors.Is(err, sentinel) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("pruneturns scan error", func(t *testing.T) {
		b := newFakeVectorBackend()
		b.scanErr = sentinel
		if _, err := NewVectorBackendStore(b, 3).PruneTurns(ctx, sc, time.Hour, 0); !errors.Is(err, sentinel) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("pruneturns delete error", func(t *testing.T) {
		b := newFakeVectorBackend()
		s := NewVectorBackendStore(b, 3)
		_ = s.Upsert(ctx, sc, []Point{{ID: "t", TenantID: "acme", UserID: "alice", Kind: KindTurn, Vector: vec3(1, 0, 0), CreatedAt: time.Now().Add(-72 * time.Hour)}})
		b.deleteErr = sentinel
		if _, err := s.PruneTurns(ctx, sc, time.Hour, 0); !errors.Is(err, sentinel) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestVectorBackend_NamespaceIsolation(t *testing.T) {
	pairs := []Scope{
		{tenant: "a", user: "bc"},
		{tenant: "ab", user: "c"},
		{tenant: "acme", user: "alice"},
		{tenant: "acme", user: "bob"},
	}
	seen := map[string]Scope{}
	for _, sc := range pairs {
		ns := namespaceFor(sc)
		if !strings.HasPrefix(ns, "aisotropy-sem-v1-") {
			t.Fatalf("namespace prefix wrong: %q", ns)
		}
		if prev, ok := seen[ns]; ok {
			t.Fatalf("namespace collision between %+v and %+v", prev, sc)
		}
		seen[ns] = sc
	}
	if namespaceFor(pairs[0]) != namespaceFor(Scope{tenant: "a", user: "bc"}) {
		t.Fatalf("namespaceFor is not deterministic")
	}
}

func TestVectorBackend_PayloadRoundTripAndMillis(t *testing.T) {
	created := time.UnixMilli(1_700_000_000_000)
	p := Point{ID: "p", Kind: KindDocument, Content: "c", Source: "s", Citation: "cit", CreatedAt: created}
	pl := payloadFromPoint(p)
	if pl[pkKind] != string(KindDocument) || pl[pkContent] != "c" || pl[pkSource] != "s" || pl[pkCitation] != "cit" {
		t.Fatalf("payloadFromPoint wrong: %+v", pl)
	}

	sc := Scope{tenant: "acme", user: "alice"}
	got := pointFromBackend(sc, BackendVector{ID: "p", Vector: vec3(1, 0, 0), Payload: pl})
	if got.TenantID != "acme" || got.UserID != "alice" {
		t.Fatalf("identity must come from scope, not payload: %+v", got)
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("created_at round-trip wrong: %v", got.CreatedAt)
	}

	// millisToTime edge cases.
	if !millisToTime("").IsZero() {
		t.Fatalf("empty millis should be zero time")
	}
	if !millisToTime("garbage").IsZero() {
		t.Fatalf("malformed millis should be zero time")
	}
}
