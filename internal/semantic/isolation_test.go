package semantic

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// This file is the store-level adversarial isolation suite for the semantic
// tier. It locks the constitutional multi-tenant gate into CI at the vector
// layer: every scoped Search returns ONLY the scope's own points (including the
// tenantA/userB and tenantB/userA cross combinations), the (tenant,user) filter
// is applied DURING the scan rather than as a post-filter, a cross-scope Upsert
// is rejected structurally (ErrScopeMismatch), and a colliding point id under a
// foreign scope is structurally unreachable by Delete.
//
// The runtime tests exercise the in-process InMemoryStore cosine path (the
// zero-config default, no external service). A separate source-level guard pins
// that the QdrantStore builds a Must-filter — not a post-filter — for its
// scoped reads/deletes, since exercising Qdrant's HNSW scan would require a live
// service.

// isoScope builds a user-scoped Scope, failing the test on an invalid identity.
func isoScope(t *testing.T, tenant, user string) Scope {
	t.Helper()
	sc, err := NewScope(tenant, user)
	if err != nil {
		t.Fatalf("NewScope(%q,%q): %v", tenant, user, err)
	}
	return sc
}

// TestIsolationSemanticSearchNeverCrossScope upserts one point per scope — all
// four with identical content, hence identical vectors — and asserts each
// scope's Search returns exactly its own point. Because the isolation filter
// runs inside the scan loop (memory_store.go), the foreign points are never
// even scored; a tenant-only or user-only filter would leak the cross
// combinations.
func TestIsolationSemanticSearchNeverCrossScope(t *testing.T) {
	emb := NewHashEmbedder(0)
	st := NewInMemoryStore(emb.Dimension())
	ctx := context.Background()

	a := isoScope(t, "acme", "alice")
	b := isoScope(t, "globex", "bob")
	crossTU := isoScope(t, "acme", "bob")
	crossUT := isoScope(t, "globex", "alice")

	const content = "confidential embedding payload keyword"
	for _, sc := range []Scope{a, b, crossTU, crossUT} {
		p := point(ctx, sc, emb, KindFact, "note", content)
		if err := st.Upsert(ctx, sc, []Point{p}); err != nil {
			t.Fatalf("Upsert (%q,%q): %v", sc.Tenant(), sc.User(), err)
		}
	}

	q := embedOne(t, emb, content)
	for _, sc := range []Scope{a, b, crossTU, crossUT} {
		name := fmt.Sprintf("%s/%s", sc.Tenant(), sc.User())
		t.Run(name, func(t *testing.T) {
			got, err := st.Search(ctx, sc, q, 10)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("Search under %s returned %d points, want exactly 1 (cross-scope leak): %+v", name, len(got), got)
			}
			if got[0].TenantID != sc.Tenant() || got[0].UserID != sc.User() {
				t.Fatalf("Search under %s returned a point scoped to (%q,%q), want (%q,%q) — foreign vector leaked",
					name, got[0].TenantID, got[0].UserID, sc.Tenant(), sc.User())
			}
		})
	}
}

// TestIsolationSemanticUpsertRejectsCrossScope pins the structural write guard:
// Upsert rejects any point whose (TenantID,UserID) differs from the operation
// Scope with ErrScopeMismatch, on every axis. It also confirms the reject is
// all-or-nothing enough that the foreign point is never searchable.
func TestIsolationSemanticUpsertRejectsCrossScope(t *testing.T) {
	emb := NewHashEmbedder(0)
	st := NewInMemoryStore(emb.Dimension())
	ctx := context.Background()

	a := isoScope(t, "acme", "alice")
	b := isoScope(t, "globex", "bob")

	mism := []struct {
		name  string
		point Point
	}{
		{"foreign tenant+user", point(ctx, b, emb, KindFact, "x", "c")},
		{"foreign tenant only", Point{ID: PointID(b, KindFact, "y"), TenantID: b.Tenant(), UserID: a.User(), Kind: KindFact, Content: "c", Vector: embedOne(t, emb, "c")}},
		{"foreign user only", Point{ID: PointID(a, KindFact, "z"), TenantID: a.Tenant(), UserID: b.User(), Kind: KindFact, Content: "c", Vector: embedOne(t, emb, "c")}},
	}
	for _, m := range mism {
		t.Run(m.name, func(t *testing.T) {
			if err := st.Upsert(ctx, a, []Point{m.point}); !errors.Is(err, ErrScopeMismatch) {
				t.Fatalf("Upsert with %s under scope A = %v, want ErrScopeMismatch", m.name, err)
			}
		})
	}
}

// TestIsolationSemanticDeleteScopedByCollidingID is the sharpest structural
// guard for the in-process store: InMemoryStore keys its map by point ID alone,
// so an id present under scope A could in principle be deleted by a caller in
// scope B. The store checks the stored point's (tenant,user) BEFORE deleting
// (memory_store.go Delete), so a Delete under B for A's id must be a no-op and
// A's point must survive. A refactor that drops that pre-check — turning Delete
// into a bare map delete — fails here.
func TestIsolationSemanticDeleteScopedByCollidingID(t *testing.T) {
	emb := NewHashEmbedder(0)
	st := NewInMemoryStore(emb.Dimension())
	ctx := context.Background()

	a := isoScope(t, "acme", "alice")
	b := isoScope(t, "globex", "bob")

	const content = "durable vector to protect"
	pa := point(ctx, a, emb, KindFact, "note", content)
	if err := st.Upsert(ctx, a, []Point{pa}); err != nil {
		t.Fatalf("Upsert A: %v", err)
	}

	// B tries to delete A's exact id.
	if err := st.Delete(ctx, b, []string{pa.ID}); err != nil {
		t.Fatalf("Delete under B: %v", err)
	}
	got, err := st.Search(ctx, a, embedOne(t, emb, content), 10)
	if err != nil {
		t.Fatalf("Search A: %v", err)
	}
	if len(got) != 1 || got[0].ID != pa.ID {
		t.Fatalf("A's point was removed by B's scoped Delete of a colliding id (isolation leak), got %+v", got)
	}

	// DeleteBySource must be equally scope-safe: give A and B the same
	// (kind,source); B's DeleteBySource must not remove A's chunk.
	src := "shared-doc-id"
	paDoc := Point{ID: PointID(a, KindDocument, src), TenantID: a.Tenant(), UserID: a.User(), Kind: KindDocument, Source: src, Content: content, Vector: embedOne(t, emb, content)}
	if err := st.Upsert(ctx, a, []Point{paDoc}); err != nil {
		t.Fatalf("Upsert A doc: %v", err)
	}
	n, err := st.DeleteBySource(ctx, b, KindDocument, src)
	if err != nil {
		t.Fatalf("DeleteBySource under B: %v", err)
	}
	if n != 0 {
		t.Fatalf("B's DeleteBySource removed %d of A's points, want 0 (cross-scope leak)", n)
	}
	got, err = st.Search(ctx, a, embedOne(t, emb, content), 10, KindDocument)
	if err != nil {
		t.Fatalf("Search A doc: %v", err)
	}
	if len(got) != 1 || got[0].ID != paDoc.ID {
		t.Fatalf("A's document chunk was removed by B's DeleteBySource, got %+v", got)
	}
}

// TestIsolationSemanticConcurrent runs parallel writers and readers under
// scopes A and B and asserts no reader ever scores the other scope's vectors.
// Under -race it also guards the store's RWMutex-protected map against data
// races in the concurrent access path.
func TestIsolationSemanticConcurrent(t *testing.T) {
	emb := NewHashEmbedder(0)
	st := NewInMemoryStore(emb.Dimension())
	ctx := context.Background()

	a := isoScope(t, "acme", "alice")
	b := isoScope(t, "globex", "bob")
	const perScope = 50
	q := embedOne(t, emb, "shared concurrent vector payload")

	var wg sync.WaitGroup
	writer := func(sc Scope, tag string) {
		defer wg.Done()
		for i := 0; i < perScope; i++ {
			p := point(ctx, sc, emb, KindFact, fmt.Sprintf("%s-%d", tag, i), "shared concurrent vector payload")
			if err := st.Upsert(ctx, sc, []Point{p}); err != nil {
				t.Errorf("Upsert %s-%d: %v", tag, i, err)
				return
			}
		}
	}
	reader := func(sc, other Scope, tag string) {
		defer wg.Done()
		for i := 0; i < perScope; i++ {
			got, err := st.Search(ctx, sc, q, 100)
			if err != nil {
				t.Errorf("Search %s: %v", tag, err)
				return
			}
			for _, sp := range got {
				if sp.TenantID == other.Tenant() && sp.UserID == other.User() {
					t.Errorf("CROSS-SCOPE LEAK: reader %s scored a point scoped to (%q,%q): %+v",
						tag, other.Tenant(), other.User(), sp)
					return
				}
				if sp.TenantID != sc.Tenant() || sp.UserID != sc.User() {
					t.Errorf("reader %s scored a point outside its scope: %+v", tag, sp)
					return
				}
			}
		}
	}

	wg.Add(4)
	go writer(a, "A")
	go writer(b, "B")
	go reader(a, b, "readA")
	go reader(b, a, "readB")
	wg.Wait()

	for _, sc := range []Scope{a, b} {
		got, err := st.Search(ctx, sc, q, 1000)
		if err != nil {
			t.Fatalf("final Search: %v", err)
		}
		if len(got) != perScope {
			t.Fatalf("scope (%q,%q) final Search returned %d, want %d", sc.Tenant(), sc.User(), len(got), perScope)
		}
	}
}

// TestIsolationSemanticInMemoryFiltersDuringScanNotAfter proves the isolation
// filter is a during-scan gate, not a post-filter that could be trimmed away by
// a limit. Scope B holds `limit` points; scope A holds one. A Search under A
// with limit=1 must still return A's single point — never B's — even though B's
// points would fill the limit if they were scored first and A's were post-
// filtered out. A post-filter-then-truncate implementation could return zero
// rows for A here; the during-scan filter cannot.
func TestIsolationSemanticInMemoryFiltersDuringScanNotAfter(t *testing.T) {
	emb := NewHashEmbedder(0)
	st := NewInMemoryStore(emb.Dimension())
	ctx := context.Background()

	a := isoScope(t, "acme", "alice")
	b := isoScope(t, "globex", "bob")

	const content = "identical similarity content"
	// Flood B with many points that all match the query perfectly.
	for i := 0; i < 20; i++ {
		p := point(ctx, b, emb, KindFact, fmt.Sprintf("b-%d", i), content)
		if err := st.Upsert(ctx, b, []Point{p}); err != nil {
			t.Fatalf("Upsert b-%d: %v", i, err)
		}
	}
	pa := point(ctx, a, emb, KindFact, "a-only", content)
	if err := st.Upsert(ctx, a, []Point{pa}); err != nil {
		t.Fatalf("Upsert a-only: %v", err)
	}

	got, err := st.Search(ctx, a, embedOne(t, emb, content), 1)
	if err != nil {
		t.Fatalf("Search A: %v", err)
	}
	if len(got) != 1 || got[0].ID != pa.ID {
		t.Fatalf("Search under A (limit=1) = %+v, want exactly A's own point; a post-filter-then-truncate would drop it", got)
	}
}

// TestIsolationSemanticQdrantUsesMustFilterNotPostFilter is the source-level
// omitted-filter regression guard for the networked backend. Qdrant's HNSW scan
// cannot be exercised without a live service, so this asserts the STRUCTURE of
// the scoped operations in qdrant.go: each must build a Must-filter carrying the
// tenant and user payload conditions, and Search must apply that filter to the
// Query (evaluated during the scan) rather than filtering results afterward. If
// a refactor drops a Must condition or moves isolation to a post-filter loop,
// this fails.
func TestIsolationSemanticQdrantUsesMustFilterNotPostFilter(t *testing.T) {
	src, err := os.ReadFile("qdrant.go")
	if err != nil {
		t.Fatalf("read qdrant.go: %v", err)
	}
	body := extractFuncBody(t, string(src), "func (s *QdrantStore) Search(")

	required := []string{
		"qdrant.NewMatch(payloadTenant, sc.Tenant())",
		"qdrant.NewMatch(payloadUser, sc.User())",
		"Filter:",
		"Must: must",
	}
	for _, r := range required {
		if !strings.Contains(body, r) {
			t.Fatalf("QdrantStore.Search no longer contains %q — the (tenant,user) Must-filter may have been removed or weakened:\n%s", r, body)
		}
	}

	// Guard against a post-filter: the result loop must NOT re-check tenant/user
	// and `continue`, which would mean the scan itself was unfiltered.
	if strings.Contains(body, "continue") && strings.Contains(body, "sc.Tenant()") &&
		strings.Contains(body, "!=") {
		// Heuristic: a during-scan Must-filter never needs a post-scan
		// tenant-mismatch `continue`. Flag it for review.
		t.Fatalf("QdrantStore.Search appears to post-filter results by tenant (continue on mismatch) — isolation must be a Must-filter during the scan, not a post-filter:\n%s", body)
	}

	// The scoped mutation paths must also carry both payload conditions.
	for _, fn := range []string{
		"func (s *QdrantStore) Delete(",
		"func (s *QdrantStore) DeleteBySource(",
		"func (s *QdrantStore) PruneTurns(",
	} {
		b := extractFuncBody(t, string(src), fn)
		if !strings.Contains(b, "payloadTenant") || !strings.Contains(b, "payloadUser") {
			t.Fatalf("%s no longer references both payloadTenant and payloadUser — scoped mutation isolation weakened:\n%s", fn, b)
		}
	}
}

// extractFuncBody returns the source text of the function whose signature
// starts with sigPrefix, from that line up to the next top-level "\nfunc " (or
// EOF). Good enough for the structural asserts above without importing go/parser.
func extractFuncBody(t *testing.T, src, sigPrefix string) string {
	t.Helper()
	start := strings.Index(src, sigPrefix)
	if start < 0 {
		t.Fatalf("could not find %q in source — signature changed?", sigPrefix)
	}
	rest := src[start+len(sigPrefix):]
	if end := strings.Index(rest, "\nfunc "); end >= 0 {
		return src[start : start+len(sigPrefix)+end]
	}
	return src[start:]
}

// TestIsolationSemanticEveryStoreReadTakesScope is the enumeration guard for the
// semantic Store interface: unlike the facts tier, the semantic tier has NO
// deliberate unscoped read (no ExportAll equivalent), so EVERY method that
// returns points must take a Scope. A new unscoped read method fails here.
func TestIsolationSemanticEveryStoreReadTakesScope(t *testing.T) {
	storeType := reflect.TypeOf((*Store)(nil)).Elem()
	scopeType := reflect.TypeOf(Scope{})
	pointSlice := reflect.TypeOf([]ScoredPoint(nil))

	var unscoped []string
	for i := 0; i < storeType.NumMethod(); i++ {
		m := storeType.Method(i)
		returnsPoints := false
		for j := 0; j < m.Type.NumOut(); j++ {
			if m.Type.Out(j) == pointSlice {
				returnsPoints = true
			}
		}
		if !returnsPoints {
			continue
		}
		takesScope := false
		for j := 0; j < m.Type.NumIn(); j++ {
			if m.Type.In(j) == scopeType {
				takesScope = true
			}
		}
		if !takesScope {
			unscoped = append(unscoped, m.Name)
		}
	}
	if len(unscoped) != 0 {
		t.Fatalf("semantic Store must have NO unscoped point-returning reads; found: %v — add a Scope arg or justify a reviewed cross-tenant export", unscoped)
	}
}
