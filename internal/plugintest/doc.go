package plugintest

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"unicode"

	"github.com/Luum-Home/aisotropy/internal/facts"
)

// DocBackendFactory builds a fresh, empty facts.DocBackend for one conformance
// run.
type DocBackendFactory func() facts.DocBackend

// RawDocInspector is an OPTIONAL interface a facts.DocBackend may implement to
// let the conformance suite demonstrate the isolation SEAL directly, exactly as
// RawVectorInspector does for the semantic tier. Namespaces returns every
// namespace the backend currently holds documents under.
type RawDocInspector interface {
	Namespaces() []string
}

// RunDocBackendSuite runs the full facts.DocBackend conformance suite against
// backends produced by factory. It fails t if the backend, once sealed by
// facts.NewDocBackendStore, violates the Store contract or the multi-tenant
// isolation gate.
func RunDocBackendSuite(t *testing.T, factory DocBackendFactory) {
	t.Helper()
	t.Run("RoundTrip", func(t *testing.T) { docRoundTrip(t, factory) })
	t.Run("Supersede", func(t *testing.T) { docSupersede(t, factory) })
	t.Run("Delete", func(t *testing.T) { docDelete(t, factory) })
	t.Run("ExportAll", func(t *testing.T) { docExportAll(t, factory) })
	t.Run("ScopeMismatch", func(t *testing.T) { docScopeMismatch(t, factory) })
	// The central proof: the decorator seals tenant isolation.
	t.Run("Isolation", func(t *testing.T) { docIsolation(t, factory) })
}

func factsScope(t *testing.T, tenant, user string) facts.Scope {
	t.Helper()
	sc, err := facts.NewScope(tenant, user)
	if err != nil {
		t.Fatalf("facts.NewScope(%q,%q): %v", tenant, user, err)
	}
	return sc
}

func fact(sc facts.Scope, id, content string, importance float64) facts.Fact {
	return facts.Fact{
		ID:         id,
		TenantID:   sc.Tenant(),
		UserID:     sc.User(),
		Content:    content,
		Importance: importance,
	}
}

func docRoundTrip(t *testing.T, factory DocBackendFactory) {
	t.Helper()
	ctx := context.Background()
	store := facts.NewDocBackendStore(factory())
	defer func() { _ = store.Close() }()
	sc := factsScope(t, "acme", "alice")

	if err := store.Save(ctx, sc, fact(sc, "f1", "user prefers dark mode", 0.9)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Search(ctx, sc, "dark mode", 4)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].ID != "f1" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got[0].TenantID != "acme" || got[0].UserID != "alice" {
		t.Fatalf("identity not reattached from scope: got tenant=%q user=%q", got[0].TenantID, got[0].UserID)
	}
	// Empty query yields nothing.
	if empty, _ := store.Search(ctx, sc, "   ", 4); len(empty) != 0 {
		t.Fatalf("empty query returned %d facts, want 0", len(empty))
	}
}

func docSupersede(t *testing.T, factory DocBackendFactory) {
	t.Helper()
	ctx := context.Background()
	store := facts.NewDocBackendStore(factory())
	defer func() { _ = store.Close() }()
	sc := factsScope(t, "acme", "alice")

	if err := store.Save(ctx, sc, fact(sc, "old", "user likes tea", 0.8)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Superseding an unknown fact fails closed.
	if err := store.Supersede(ctx, sc, "does-not-exist", fact(sc, "new0", "x", 0.5)); err != facts.ErrFactNotFound {
		t.Fatalf("Supersede unknown: got %v, want ErrFactNotFound", err)
	}
	if err := store.Supersede(ctx, sc, "old", fact(sc, "new", "user likes coffee", 0.8)); err != nil {
		t.Fatalf("Supersede: %v", err)
	}
	// The superseded fact is excluded from Search; the new one is present.
	tea, _ := store.Search(ctx, sc, "tea", 4)
	for _, f := range tea {
		if f.ID == "old" {
			t.Fatalf("superseded fact still returned by Search")
		}
	}
	coffee, _ := store.Search(ctx, sc, "coffee", 4)
	if len(coffee) != 1 || coffee[0].ID != "new" {
		t.Fatalf("new fact not searchable after supersede: %+v", coffee)
	}
	// Superseding an already-superseded fact fails closed too.
	if err := store.Supersede(ctx, sc, "old", fact(sc, "new2", "y", 0.5)); err != facts.ErrFactNotFound {
		t.Fatalf("re-Supersede: got %v, want ErrFactNotFound", err)
	}
}

func docDelete(t *testing.T, factory DocBackendFactory) {
	t.Helper()
	ctx := context.Background()
	store := facts.NewDocBackendStore(factory())
	defer func() { _ = store.Close() }()
	sc := factsScope(t, "acme", "alice")

	if err := store.Save(ctx, sc, fact(sc, "f1", "delete me please", 0.5)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Delete(ctx, sc, []string{"f1", "unknown"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ := store.Search(ctx, sc, "delete", 4)
	if len(got) != 0 {
		t.Fatalf("Delete left %d facts, want 0", len(got))
	}
}

func docExportAll(t *testing.T, factory DocBackendFactory) {
	t.Helper()
	ctx := context.Background()
	store := facts.NewDocBackendStore(factory())
	defer func() { _ = store.Close() }()
	scA := factsScope(t, "acme", "alice")
	scB := factsScope(t, "globex", "bob")

	if err := store.Save(ctx, scA, fact(scA, "a1", "acme fact one", 0.5)); err != nil {
		t.Fatalf("Save A: %v", err)
	}
	if err := store.Save(ctx, scB, fact(scB, "b1", "globex fact one", 0.5)); err != nil {
		t.Fatalf("Save B: %v", err)
	}
	// Supersede one so ExportAll's "current only" filter is exercised.
	if err := store.Save(ctx, scA, fact(scA, "a2", "acme fact two", 0.5)); err != nil {
		t.Fatalf("Save A2: %v", err)
	}
	if err := store.Supersede(ctx, scA, "a2", fact(scA, "a3", "acme fact three", 0.5)); err != nil {
		t.Fatalf("Supersede: %v", err)
	}

	all, err := store.ExportAll(ctx)
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	byID := map[string]facts.Fact{}
	for _, f := range all {
		byID[f.ID] = f
	}
	// Cross-tenant: BOTH tenants' current facts appear, with identity
	// reconstructed from the stored fields.
	if _, ok := byID["a1"]; !ok {
		t.Fatalf("ExportAll missing acme fact a1")
	}
	if _, ok := byID["b1"]; !ok {
		t.Fatalf("ExportAll missing globex fact b1")
	}
	if byID["a1"].TenantID != "acme" || byID["b1"].TenantID != "globex" {
		t.Fatalf("ExportAll reconstructed wrong identity: %+v", byID)
	}
	if _, ok := byID["a2"]; ok {
		t.Fatalf("ExportAll returned a superseded fact (a2)")
	}
	if _, ok := byID["a3"]; !ok {
		t.Fatalf("ExportAll missing the superseding fact a3")
	}
}

func docScopeMismatch(t *testing.T, factory DocBackendFactory) {
	t.Helper()
	ctx := context.Background()
	store := facts.NewDocBackendStore(factory())
	defer func() { _ = store.Close() }()
	sc := factsScope(t, "acme", "alice")
	bad := facts.Fact{ID: "x", TenantID: "acme", UserID: "mallory", Content: "cross-user"}
	if err := store.Save(ctx, sc, bad); err != facts.ErrScopeMismatch {
		t.Fatalf("Save cross-user fact: got %v, want ErrScopeMismatch", err)
	}
}

// docIsolation is the CENTRAL proof for the facts tier: the sealed decorator,
// not the backend, enforces isolation. Mirrors vectorIsolation.
func docIsolation(t *testing.T, factory DocBackendFactory) {
	t.Helper()
	ctx := context.Background()
	backend := factory()
	store := facts.NewDocBackendStore(backend)
	defer func() { _ = store.Close() }()

	scA := factsScope(t, "acme", "alice")
	scB := factsScope(t, "globex", "bob")

	if err := store.Save(ctx, scA, fact(scA, "shared-id", "ACME CONFIDENTIAL merger plan", 0.9)); err != nil {
		t.Fatalf("A Save: %v", err)
	}
	// A sees its own fact ...
	aGot, err := store.Search(ctx, scA, "merger plan", 4)
	if err != nil {
		t.Fatalf("A Search: %v", err)
	}
	if len(aGot) != 1 || aGot[0].Content != "ACME CONFIDENTIAL merger plan" {
		t.Fatalf("A cannot see its own fact: %+v", aGot)
	}
	// ... but B sees NOTHING (the seal).
	bGot, err := store.Search(ctx, scB, "merger plan", 4)
	if err != nil {
		t.Fatalf("B Search: %v", err)
	}
	if len(bGot) != 0 {
		t.Fatalf("ISOLATION BREACH: tenant B read tenant A's fact through the decorator: %+v", bGot)
	}
	// Id collision across tenants must not cross-contaminate.
	if err := store.Save(ctx, scB, fact(scB, "shared-id", "GLOBEX private memo", 0.9)); err != nil {
		t.Fatalf("B Save: %v", err)
	}
	aGot, _ = store.Search(ctx, scA, "merger", 4)
	bGot, _ = store.Search(ctx, scB, "memo", 4)
	if len(aGot) != 1 || aGot[0].Content != "ACME CONFIDENTIAL merger plan" {
		t.Fatalf("id collision leaked into A: %+v", aGot)
	}
	if len(bGot) != 1 || bGot[0].Content != "GLOBEX private memo" {
		t.Fatalf("id collision leaked into B: %+v", bGot)
	}

	// Raw contrast, when the backend can be inspected: the marker is physically
	// present and retrievable by raw namespace access; only the decorator's
	// unexported namespace derivation kept B out.
	if insp, ok := backend.(RawDocInspector); ok {
		namespaces := insp.Namespaces()
		if len(namespaces) != 2 {
			t.Fatalf("raw backend holds %d namespaces, want 2", len(namespaces))
		}
		var found bool
		for _, ns := range namespaces {
			hits, herr := backend.Search(ctx, ns, "merger plan", 4)
			if herr != nil {
				t.Fatalf("raw backend Search(%q): %v", ns, herr)
			}
			for _, h := range hits {
				if h.Content == "ACME CONFIDENTIAL merger plan" {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("raw backend does not physically hold A's fact; the isolation contrast is meaningless")
		}
	}
}

// RefDocBackend is an in-memory reference facts.DocBackend: a correct, minimal,
// dependency-free implementation a community author can read, and the substrate
// the conformance suite runs against in CI. Keyword relevance is a simple token
// overlap count — enough to rank the suite's cases and to model what a real
// FTS/keyword engine returns, at the tenant-UNAWARE backend layer (it only ever
// sees opaque namespaces and keys). It implements RawDocInspector.
type RefDocBackend struct {
	mu   sync.RWMutex
	data map[string]map[string]facts.BackendDoc // ns -> key -> doc
}

// NewRefDocBackend returns an empty in-memory reference doc backend.
func NewRefDocBackend() *RefDocBackend {
	return &RefDocBackend{data: make(map[string]map[string]facts.BackendDoc)}
}

// Put inserts or replaces the doc keyed (ns, key).
func (b *RefDocBackend) Put(_ context.Context, ns, key string, doc facts.BackendDoc) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	m := b.data[ns]
	if m == nil {
		m = make(map[string]facts.BackendDoc)
		b.data[ns] = m
	}
	m[key] = doc
	return nil
}

// Get returns the doc keyed (ns, key).
func (b *RefDocBackend) Get(_ context.Context, ns, key string) (facts.BackendDoc, bool, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	d, ok := b.data[ns][key]
	return d, ok, nil
}

// Search returns up to k docs under ns by descending token-overlap relevance.
func (b *RefDocBackend) Search(_ context.Context, ns, query string, k int) ([]facts.BackendDocHit, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	qterms := tokenSet(query)
	if len(qterms) == 0 {
		return nil, nil
	}
	var hits []facts.BackendDocHit
	for _, d := range b.data[ns] {
		overlap := 0
		for term := range tokenSet(d.Content) {
			if _, ok := qterms[term]; ok {
				overlap++
			}
		}
		if overlap == 0 {
			continue
		}
		hits = append(hits, facts.BackendDocHit{BackendDoc: d, Score: float64(overlap)})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Key < hits[j].Key
	})
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

// Delete removes keys under ns; unknown keys are ignored.
func (b *RefDocBackend) Delete(_ context.Context, ns string, keys []string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	m := b.data[ns]
	for _, key := range keys {
		delete(m, key)
	}
	return nil
}

// Scan returns every doc under ns.
func (b *RefDocBackend) Scan(_ context.Context, ns string) ([]facts.BackendDoc, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var out []facts.BackendDoc
	for _, d := range b.data[ns] {
		out = append(out, d)
	}
	return out, nil
}

// ExportAll returns every doc across all namespaces (the cross-tenant path).
func (b *RefDocBackend) ExportAll(_ context.Context) ([]facts.BackendDoc, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var out []facts.BackendDoc
	for _, m := range b.data {
		for _, d := range m {
			out = append(out, d)
		}
	}
	return out, nil
}

// Namespaces implements RawDocInspector.
func (b *RefDocBackend) Namespaces() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var out []string
	for ns := range b.data {
		out = append(out, ns)
	}
	return out
}

// Close is a no-op for the in-memory reference backend.
func (b *RefDocBackend) Close() error { return nil }

// tokenSet lowercases s and returns its distinct word tokens.
func tokenSet(s string) map[string]struct{} {
	m := make(map[string]struct{})
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		m[f] = struct{}{}
	}
	return m
}
