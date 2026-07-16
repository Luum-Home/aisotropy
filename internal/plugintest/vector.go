package plugintest

import (
	"context"
	"math"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// nowMinusDays returns a timestamp d days in the past, for seeding CreatedAt on
// PruneTurns cases.
func nowMinusDays(d int) time.Time { return time.Now().Add(-daysDur(d)) }

// daysDur is d days as a time.Duration.
func daysDur(d int) time.Duration { return time.Duration(d) * 24 * time.Hour }

// VectorBackendFactory builds a fresh, empty semantic.VectorBackend for one
// conformance run. Each subtest that needs isolation calls it again so state
// never leaks between cases.
type VectorBackendFactory func() semantic.VectorBackend

// RawVectorInspector is an OPTIONAL interface a semantic.VectorBackend may
// implement to let the conformance suite demonstrate the isolation SEAL
// directly. Namespaces returns every namespace the backend currently holds
// data under. The suite uses it, when available, to prove that a tenant's
// vectors are physically present and retrievable by RAW (namespace-level)
// backend access — so the ONLY thing stopping another tenant from reaching them
// is the decorator's unexported namespace derivation, not some accident of the
// backend. Implementing it is not required for conformance; it only enables the
// extra "raw would leak" assertion.
type RawVectorInspector interface {
	Namespaces() []string
}

// RunVectorBackendSuite runs the full semantic.VectorBackend conformance suite
// against backends produced by factory. It fails t if the backend, once sealed
// by semantic.NewVectorBackendStore, violates the Store contract or the
// multi-tenant isolation gate.
func RunVectorBackendSuite(t *testing.T, factory VectorBackendFactory) {
	t.Helper()
	t.Run("RoundTrip", func(t *testing.T) { vectorRoundTrip(t, factory) })
	t.Run("KindFilter", func(t *testing.T) { vectorKindFilter(t, factory) })
	t.Run("Delete", func(t *testing.T) { vectorDelete(t, factory) })
	t.Run("DeleteBySource", func(t *testing.T) { vectorDeleteBySource(t, factory) })
	t.Run("PruneTurns", func(t *testing.T) { vectorPruneTurns(t, factory) })
	t.Run("ScopeMismatch", func(t *testing.T) { vectorScopeMismatch(t, factory) })
	t.Run("DimensionMismatch", func(t *testing.T) { vectorDimensionMismatch(t, factory) })
	// The central proof: the decorator seals tenant isolation.
	t.Run("Isolation", func(t *testing.T) { vectorIsolation(t, factory) })
}

func mustScope(t *testing.T, tenant, user string) semantic.Scope {
	t.Helper()
	sc, err := semantic.NewScope(tenant, user)
	if err != nil {
		t.Fatalf("semantic.NewScope(%q,%q): %v", tenant, user, err)
	}
	return sc
}

// unitVec returns a deterministic SuiteDim-width vector whose direction is set
// by seed, so two calls with the same seed are identical (cosine 1) and
// different seeds are near-orthogonal enough for the suite's ranking checks.
func unitVec(seed int) []float32 {
	v := make([]float32, SuiteDim)
	// Put weight on one axis chosen by seed, plus a small spread, then normalize.
	for i := range v {
		if i == seed%SuiteDim {
			v[i] = 1
		} else {
			v[i] = 0.01 * float32((seed+i)%3)
		}
	}
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	norm = math.Sqrt(norm)
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
	return v
}

func point(sc semantic.Scope, id string, kind semantic.Kind, content string, seed int) semantic.Point {
	return semantic.Point{
		ID:       id,
		TenantID: sc.Tenant(),
		UserID:   sc.User(),
		Kind:     kind,
		Content:  content,
		Vector:   unitVec(seed),
	}
}

func vectorRoundTrip(t *testing.T, factory VectorBackendFactory) {
	t.Helper()
	ctx := context.Background()
	store := semantic.NewVectorBackendStore(factory(), SuiteDim)
	defer func() { _ = store.Close() }()
	sc := mustScope(t, "acme", "alice")

	q := unitVec(1)
	p := point(sc, "p1", semantic.KindDocument, "the quarterly report", 1)
	if err := store.Upsert(ctx, sc, []semantic.Point{p}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := store.Search(ctx, sc, q, 4)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Search returned %d points, want 1", len(got))
	}
	if got[0].ID != "p1" || got[0].Content != "the quarterly report" {
		t.Fatalf("round-trip mismatch: got id=%q content=%q", got[0].ID, got[0].Content)
	}
	if got[0].TenantID != "acme" || got[0].UserID != "alice" {
		t.Fatalf("identity not reattached from scope: got tenant=%q user=%q", got[0].TenantID, got[0].UserID)
	}
	// Idempotent upsert on ID: re-writing the same id must not duplicate.
	if err := store.Upsert(ctx, sc, []semantic.Point{p}); err != nil {
		t.Fatalf("re-Upsert: %v", err)
	}
	got, err = store.Search(ctx, sc, q, 4)
	if err != nil {
		t.Fatalf("Search after re-Upsert: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("idempotency broken: %d points after re-upsert, want 1", len(got))
	}
}

func vectorKindFilter(t *testing.T, factory VectorBackendFactory) {
	t.Helper()
	ctx := context.Background()
	store := semantic.NewVectorBackendStore(factory(), SuiteDim)
	defer func() { _ = store.Close() }()
	sc := mustScope(t, "acme", "alice")

	pts := []semantic.Point{
		point(sc, "d1", semantic.KindDocument, "doc content", 1),
		point(sc, "f1", semantic.KindFact, "fact content", 1),
		point(sc, "t1", semantic.KindTurn, "turn content", 1),
	}
	if err := store.Upsert(ctx, sc, pts); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	q := unitVec(1)
	// Document+Fact only: the Turn must be filtered out.
	got, err := store.Search(ctx, sc, q, 10, semantic.KindDocument, semantic.KindFact)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, p := range got {
		if p.Kind == semantic.KindTurn {
			t.Fatalf("kind filter leaked a KindTurn point: %q", p.ID)
		}
	}
	if len(got) != 2 {
		t.Fatalf("kind-filtered Search returned %d, want 2", len(got))
	}
	// No filter: all three kinds returned.
	all, err := store.Search(ctx, sc, q, 10)
	if err != nil {
		t.Fatalf("Search (all kinds): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("unfiltered Search returned %d, want 3", len(all))
	}
}

func vectorDelete(t *testing.T, factory VectorBackendFactory) {
	t.Helper()
	ctx := context.Background()
	store := semantic.NewVectorBackendStore(factory(), SuiteDim)
	defer func() { _ = store.Close() }()
	sc := mustScope(t, "acme", "alice")
	q := unitVec(1)

	if err := store.Upsert(ctx, sc, []semantic.Point{point(sc, "p1", semantic.KindDocument, "c", 1)}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.Delete(ctx, sc, []string{"p1", "unknown"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := store.Search(ctx, sc, q, 4)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Delete left %d points, want 0", len(got))
	}
}

func vectorDeleteBySource(t *testing.T, factory VectorBackendFactory) {
	t.Helper()
	ctx := context.Background()
	store := semantic.NewVectorBackendStore(factory(), SuiteDim)
	defer func() { _ = store.Close() }()
	sc := mustScope(t, "acme", "alice")
	q := unitVec(1)

	mk := func(id, src string) semantic.Point {
		p := point(sc, id, semantic.KindDocument, "chunk "+id, 1)
		p.Source = src
		return p
	}
	pts := []semantic.Point{mk("c1", "docA"), mk("c2", "docA"), mk("c3", "docB")}
	if err := store.Upsert(ctx, sc, pts); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	n, err := store.DeleteBySource(ctx, sc, semantic.KindDocument, "docA")
	if err != nil {
		t.Fatalf("DeleteBySource: %v", err)
	}
	if n != 2 {
		t.Fatalf("DeleteBySource removed %d, want 2", n)
	}
	got, err := store.Search(ctx, sc, q, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].ID != "c3" {
		t.Fatalf("DeleteBySource left the wrong points: %+v", got)
	}
}

func vectorPruneTurns(t *testing.T, factory VectorBackendFactory) {
	t.Helper()
	ctx := context.Background()
	store := semantic.NewVectorBackendStore(factory(), SuiteDim)
	defer func() { _ = store.Close() }()
	sc := mustScope(t, "acme", "alice")
	q := unitVec(1)

	mkTurn := func(id string, ageDays int) semantic.Point {
		p := point(sc, id, semantic.KindTurn, "turn "+id, 1)
		p.CreatedAt = nowMinusDays(ageDays)
		return p
	}
	doc := point(sc, "d1", semantic.KindDocument, "keep me", 1)
	doc.CreatedAt = nowMinusDays(3650)
	pts := []semantic.Point{doc, mkTurn("old", 100), mkTurn("new", 1)}
	if err := store.Upsert(ctx, sc, pts); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	// Age policy: prune turns older than 30 days -> only "old" goes.
	n, err := store.PruneTurns(ctx, sc, daysDur(30), 0)
	if err != nil {
		t.Fatalf("PruneTurns: %v", err)
	}
	if n != 1 {
		t.Fatalf("PruneTurns removed %d, want 1", n)
	}
	got, err := store.Search(ctx, sc, q, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	var haveDoc, haveNew, haveOld bool
	for _, p := range got {
		switch p.ID {
		case "d1":
			haveDoc = true
		case "new":
			haveNew = true
		case "old":
			haveOld = true
		}
	}
	if !haveDoc || !haveNew || haveOld {
		t.Fatalf("PruneTurns wrong survivors: doc=%v new=%v old=%v", haveDoc, haveNew, haveOld)
	}
}

func vectorScopeMismatch(t *testing.T, factory VectorBackendFactory) {
	t.Helper()
	ctx := context.Background()
	store := semantic.NewVectorBackendStore(factory(), SuiteDim)
	defer func() { _ = store.Close() }()
	sc := mustScope(t, "acme", "alice")
	// A point whose identity does not match the operation scope must be rejected.
	bad := semantic.Point{ID: "x", TenantID: "acme", UserID: "mallory", Kind: semantic.KindDocument, Vector: unitVec(1)}
	if err := store.Upsert(ctx, sc, []semantic.Point{bad}); err != semantic.ErrScopeMismatch {
		t.Fatalf("Upsert cross-user point: got %v, want ErrScopeMismatch", err)
	}
}

func vectorDimensionMismatch(t *testing.T, factory VectorBackendFactory) {
	t.Helper()
	ctx := context.Background()
	store := semantic.NewVectorBackendStore(factory(), SuiteDim)
	defer func() { _ = store.Close() }()
	sc := mustScope(t, "acme", "alice")
	bad := semantic.Point{ID: "x", TenantID: "acme", UserID: "alice", Kind: semantic.KindDocument, Vector: []float32{1, 2, 3}}
	if err := store.Upsert(ctx, sc, []semantic.Point{bad}); err != semantic.ErrDimensionMismatch {
		t.Fatalf("Upsert wrong-width vector: got %v, want ErrDimensionMismatch", err)
	}
	if _, err := store.Search(ctx, sc, []float32{1, 2, 3}, 4); err != semantic.ErrDimensionMismatch {
		t.Fatalf("Search wrong-width query: got %v, want ErrDimensionMismatch", err)
	}
}

// vectorIsolation is the CENTRAL proof: the sealed decorator, not the backend,
// is what enforces multi-tenant isolation. It writes a marker for tenant A
// through the decorator, shows tenant B (through the decorator) cannot see it,
// shows an id collision across tenants does not cross-contaminate, and — when
// the backend exposes RawVectorInspector — shows the marker is nonetheless
// physically present in the raw backend and retrievable by raw namespace access.
func vectorIsolation(t *testing.T, factory VectorBackendFactory) {
	t.Helper()
	ctx := context.Background()
	backend := factory()
	store := semantic.NewVectorBackendStore(backend, SuiteDim)
	defer func() { _ = store.Close() }()

	scA := mustScope(t, "acme", "alice")
	scB := mustScope(t, "globex", "bob")
	q := unitVec(1)

	marker := point(scA, "shared-id", semantic.KindDocument, "ACME CONFIDENTIAL salary table", 1)
	if err := store.Upsert(ctx, scA, []semantic.Point{marker}); err != nil {
		t.Fatalf("A Upsert: %v", err)
	}

	// A sees its own marker (write happened + round-trips) ...
	aGot, err := store.Search(ctx, scA, q, 4)
	if err != nil {
		t.Fatalf("A Search: %v", err)
	}
	if len(aGot) != 1 || aGot[0].Content != "ACME CONFIDENTIAL salary table" {
		t.Fatalf("A cannot see its own marker: %+v", aGot)
	}

	// ... but B sees NOTHING. This is the seal: B never forms a query against
	// A's namespace because the derivation is unexported and scope-bound.
	bGot, err := store.Search(ctx, scB, q, 4)
	if err != nil {
		t.Fatalf("B Search: %v", err)
	}
	if len(bGot) != 0 {
		t.Fatalf("ISOLATION BREACH: tenant B read tenant A's data through the decorator: %+v", bGot)
	}

	// Id collision across tenants must not cross-contaminate: B writes a point
	// with the SAME id, then each tenant still reads only its own content.
	bMarker := point(scB, "shared-id", semantic.KindDocument, "GLOBEX private note", 1)
	if err := store.Upsert(ctx, scB, []semantic.Point{bMarker}); err != nil {
		t.Fatalf("B Upsert: %v", err)
	}
	aGot, _ = store.Search(ctx, scA, q, 4)
	bGot, _ = store.Search(ctx, scB, q, 4)
	if len(aGot) != 1 || aGot[0].Content != "ACME CONFIDENTIAL salary table" {
		t.Fatalf("id collision leaked into A: %+v", aGot)
	}
	if len(bGot) != 1 || bGot[0].Content != "GLOBEX private note" {
		t.Fatalf("id collision leaked into B: %+v", bGot)
	}

	// If the backend can be inspected, demonstrate that the raw (unwrapped)
	// backend physically holds A's marker and returns it under raw namespace
	// access — proving the decorator's namespace seal, not the backend, is what
	// kept B out.
	if insp, ok := backend.(RawVectorInspector); ok {
		namespaces := insp.Namespaces()
		if len(namespaces) != 2 {
			t.Fatalf("raw backend holds %d namespaces, want 2 (one per tenant)", len(namespaces))
		}
		var found bool
		for _, ns := range namespaces {
			hits, herr := backend.Search(ctx, ns, q, 4)
			if herr != nil {
				t.Fatalf("raw backend Search(%q): %v", ns, herr)
			}
			for _, h := range hits {
				if h.Payload["aisotropy.content"] == "ACME CONFIDENTIAL salary table" {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("raw backend does not physically hold A's marker; the isolation contrast is meaningless")
		}
	}
}

// RefVectorBackend is an in-memory reference semantic.VectorBackend: a correct,
// minimal, dependency-free implementation a community author can read, and the
// substrate the conformance suite runs against in CI. It is brute-force cosine
// over a per-namespace map, deliberately mirroring semantic.InMemoryStore's
// engine but at the tenant-UNAWARE backend layer (it only ever sees opaque
// namespaces and ids). It implements RawVectorInspector so the isolation proof
// can demonstrate the raw/sealed contrast.
type RefVectorBackend struct {
	mu   sync.RWMutex
	data map[string]map[string]semantic.BackendVector // ns -> id -> item
}

// NewRefVectorBackend returns an empty in-memory reference vector backend.
func NewRefVectorBackend() *RefVectorBackend {
	return &RefVectorBackend{data: make(map[string]map[string]semantic.BackendVector)}
}

// Upsert stores items under ns, keyed by id (idempotent).
func (b *RefVectorBackend) Upsert(_ context.Context, ns string, items []semantic.BackendVector) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	m := b.data[ns]
	if m == nil {
		m = make(map[string]semantic.BackendVector)
		b.data[ns] = m
	}
	for _, it := range items {
		m[it.ID] = it
	}
	return nil
}

// Search returns up to k items under ns by descending cosine similarity.
func (b *RefVectorBackend) Search(_ context.Context, ns string, query []float32, k int) ([]semantic.BackendHit, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var hits []semantic.BackendHit
	for _, it := range b.data[ns] {
		hits = append(hits, semantic.BackendHit{BackendVector: it, Score: cosine(query, it.Vector)})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ID < hits[j].ID
	})
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

// Delete removes ids under ns; unknown ids are ignored.
func (b *RefVectorBackend) Delete(_ context.Context, ns string, ids []string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	m := b.data[ns]
	for _, id := range ids {
		delete(m, id)
	}
	return nil
}

// Scan returns every item under ns.
func (b *RefVectorBackend) Scan(_ context.Context, ns string) ([]semantic.BackendVector, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var out []semantic.BackendVector
	for _, it := range b.data[ns] {
		out = append(out, it)
	}
	return out, nil
}

// Namespaces implements RawVectorInspector.
func (b *RefVectorBackend) Namespaces() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var out []string
	for ns := range b.data {
		out = append(out, ns)
	}
	return out
}

// Close is a no-op for the in-memory reference backend.
func (b *RefVectorBackend) Close() error { return nil }

// cosine returns cosine similarity in [-1,1], 0 for a zero vector; mismatched
// lengths yield 0 (the decorator already guards width, so this only guards a
// hostile/buggy backend caller).
func cosine(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}
