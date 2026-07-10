package semantic

import (
	"context"
	"errors"
	"testing"
)

// helper: embed one text with the shared HashEmbedder.
func embedOne(t *testing.T, emb Embedder, text string) []float32 {
	t.Helper()
	vs, err := emb.Embed(context.Background(), []string{text})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	return vs[0]
}

// helper: build a scoped Point with a UUIDv5 id derived from (kind, messageID).
func point(ctx context.Context, sc Scope, emb Embedder, kind Kind, messageID, content string) Point {
	return Point{
		ID:       PointID(sc, kind, messageID),
		TenantID: sc.Tenant(),
		UserID:   sc.User(),
		Kind:     kind,
		Content:  content,
		Vector:   mustEmbed(ctx, emb, content),
	}
}

func mustEmbed(ctx context.Context, emb Embedder, text string) []float32 {
	vs, _ := emb.Embed(ctx, []string{text})
	return vs[0]
}

func TestPointIDDeterministicAndScoped(t *testing.T) {
	sc := Scope{tenant: "acme", user: "alice"}
	a := PointID(sc, KindTurn, "msg-1")
	if a != PointID(sc, KindTurn, "msg-1") {
		t.Fatal("PointID not deterministic")
	}
	if a == PointID(sc, KindTurn, "msg-2") {
		t.Fatal("different messages share an id")
	}
	if a == PointID(Scope{tenant: "globex", user: "alice"}, KindTurn, "msg-1") {
		t.Fatal("id collides across tenants — isolation risk in the idempotency key")
	}
}

// TestPointIDDistinctPerKind is the ADR-0017 phase-1 DoD: the same sourceID
// under different Kinds must never collide, so a Fact-kind vector and a
// Turn-kind (or Document-kind) vector derived from the same source id can be
// addressed and deleted independently.
func TestPointIDDistinctPerKind(t *testing.T) {
	sc := Scope{tenant: "acme", user: "alice"}
	turn := PointID(sc, KindTurn, "same-source-id")
	fact := PointID(sc, KindFact, "same-source-id")
	doc := PointID(sc, KindDocument, "same-source-id")
	if turn == fact || turn == doc || fact == doc {
		t.Fatalf("PointID collided across kinds for the same sourceID: turn=%s fact=%s doc=%s", turn, fact, doc)
	}
}

func TestHashEmbedderDeterministic(t *testing.T) {
	emb := NewHashEmbedder(0) // default dim
	if emb.Dimension() != defaultEmbedDim {
		t.Fatalf("Dimension = %d, want %d", emb.Dimension(), defaultEmbedDim)
	}
	a := embedOne(t, emb, "the billing database migration")
	b := embedOne(t, emb, "the billing database migration")
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("embedding not deterministic at %d", i)
		}
	}
	if len(a) != emb.Dimension() {
		t.Fatalf("vector len %d, want %d", len(a), emb.Dimension())
	}
}

func TestUpsertAndSearchCosineRanking(t *testing.T) {
	emb := NewHashEmbedder(0)
	st := NewInMemoryStore(emb.Dimension())
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()

	docs := map[string]string{
		"m1": "postgres database migration and schema changes",
		"m2": "kubernetes cluster autoscaling and pods",
		"m3": "invoice billing pdf generation",
	}
	pts := make([]Point, 0, len(docs))
	for id, c := range docs {
		pts = append(pts, point(ctx, sc, emb, KindTurn, id, c))
	}
	if err := st.Upsert(ctx, sc, pts); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	q := embedOne(t, emb, "migrate the database to postgres")
	got, err := st.Search(ctx, sc, q, 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no results")
	}
	// The postgres/database doc must rank first (highest lexical overlap ->
	// highest cosine with the hash embedder).
	if got[0].Content != docs["m1"] {
		t.Fatalf("top = %q, want the postgres/database doc", got[0].Content)
	}
}

// TestTenantAndUserIsolation is the Phase 4 DoD isolation test: a search never
// returns another tenant's or another user's vectors.
func TestTenantAndUserIsolation(t *testing.T) {
	emb := NewHashEmbedder(0)
	st := NewInMemoryStore(emb.Dimension())
	ctx := context.Background()
	const content = "shared confidential runbook content"

	mk := func(tenant, user, msg string) {
		sc := Scope{tenant: tenant, user: user}
		if err := st.Upsert(ctx, sc, []Point{point(ctx, sc, emb, KindTurn, msg, content)}); err != nil {
			t.Fatalf("Upsert %s/%s: %v", tenant, user, err)
		}
	}
	mk("acme", "alice", "a")
	mk("acme", "bob", "b")
	mk("globex", "alice", "c")

	sc := Scope{tenant: "acme", user: "alice"}
	got, err := st.Search(ctx, sc, embedOne(t, emb, content), 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("isolation leak: got %d results, want 1 (only acme/alice)", len(got))
	}
	if got[0].TenantID != "acme" || got[0].UserID != "alice" {
		t.Fatalf("leaked point: %+v", got[0].Point)
	}
}

// TestUpsertIsIdempotent is the Phase 4 DoD dedup test: reprocessing the same
// message (same UUIDv5 id) does not duplicate vectors.
func TestUpsertIsIdempotent(t *testing.T) {
	emb := NewHashEmbedder(0)
	st := NewInMemoryStore(emb.Dimension())
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()

	p1 := point(ctx, sc, emb, KindTurn, "msg-42", "first version of the chunk")
	if err := st.Upsert(ctx, sc, []Point{p1}); err != nil {
		t.Fatal(err)
	}
	// Same message id, updated content — must replace, not duplicate.
	p2 := point(ctx, sc, emb, KindTurn, "msg-42", "updated version of the chunk")
	if err := st.Upsert(ctx, sc, []Point{p2}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Search(ctx, sc, embedOne(t, emb, "version chunk"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d points, want 1 (idempotent upsert must not duplicate)", len(got))
	}
	if got[0].Content != "updated version of the chunk" {
		t.Fatalf("content = %q, want the updated version", got[0].Content)
	}
}

// TestRerankTrimsToTopK is the Phase 4 DoD reranking test: injection is trimmed
// to at most topK fragments.
func TestRerankTrimsToTopK(t *testing.T) {
	rr := LexicalReranker{}
	cands := make([]ScoredPoint, 0, 10)
	for i := range 10 {
		cands = append(cands, ScoredPoint{
			Point: Point{ID: string(rune('a' + i)), Content: "candidate fragment number"},
			Score: float32(i) / 10,
		})
	}
	out := rr.Rerank(context.Background(), "fragment", cands, DefaultTopK)
	if len(out) != DefaultTopK {
		t.Fatalf("rerank returned %d, want %d", len(out), DefaultTopK)
	}
	// topK larger than the candidate set never over-returns.
	if got := rr.Rerank(context.Background(), "x", cands[:2], DefaultTopK); len(got) != 2 {
		t.Fatalf("rerank over-returned: %d, want 2", len(got))
	}
}

func TestRerankBoostsLexicalMatch(t *testing.T) {
	rr := LexicalReranker{Alpha: 0.8} // weight lexical overlap heavily
	cands := []ScoredPoint{
		{Point: Point{ID: "vec-fav", Content: "unrelated neighbor by vector proximity"}, Score: 0.9},
		{Point: Point{ID: "lex-fav", Content: "kubernetes ingress controller tuning"}, Score: 0.5},
	}
	out := rr.Rerank(context.Background(), "kubernetes ingress", cands, 2)
	if out[0].ID != "lex-fav" {
		t.Fatalf("top = %q, want the lexical match lifted above the vector-only neighbor", out[0].ID)
	}
}

func TestUpsertErrorPaths(t *testing.T) {
	emb := NewHashEmbedder(0)
	st := NewInMemoryStore(emb.Dimension())
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()

	// Scope mismatch.
	bad := Point{ID: "x", TenantID: "globex", UserID: "alice", Vector: mustEmbed(ctx, emb, "hi")}
	if err := st.Upsert(ctx, sc, []Point{bad}); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("err = %v, want ErrScopeMismatch", err)
	}
	// Dimension mismatch.
	wrong := Point{ID: "y", TenantID: "acme", UserID: "alice", Vector: []float32{1, 2, 3}}
	if err := st.Upsert(ctx, sc, []Point{wrong}); !errors.Is(err, ErrDimensionMismatch) {
		t.Fatalf("err = %v, want ErrDimensionMismatch", err)
	}
	// Query dimension mismatch.
	if _, err := st.Search(ctx, sc, []float32{1, 2, 3}, 5); !errors.Is(err, ErrDimensionMismatch) {
		t.Fatalf("search err = %v, want ErrDimensionMismatch", err)
	}
}

// TestDeleteRemovesPointsScopeIsolated is the ADR-0017 phase-1 DoD: Delete
// removes points it owns and is scope-isolated — a delete call scoped to a
// different tenant/user must never remove a point with a colliding id that
// belongs to someone else.
func TestDeleteRemovesPointsScopeIsolated(t *testing.T) {
	emb := NewHashEmbedder(0)
	st := NewInMemoryStore(emb.Dimension())
	ctx := context.Background()
	acme := Scope{tenant: "acme", user: "alice"}
	globex := Scope{tenant: "globex", user: "bob"}

	acmePoint := point(ctx, acme, emb, KindFact, "shared-id", "acme's fact")
	if err := st.Upsert(ctx, acme, []Point{acmePoint}); err != nil {
		t.Fatalf("Upsert acme: %v", err)
	}

	// A delete scoped to a DIFFERENT tenant, targeting the SAME id, must be a
	// no-op: the point is not owned by globex/bob.
	if err := st.Delete(ctx, globex, []string{acmePoint.ID}); err != nil {
		t.Fatalf("Delete (wrong scope): %v", err)
	}
	got, err := st.Search(ctx, acme, embedOne(t, emb, "acme's fact"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("scope-isolated delete removed a point it did not own: got %d points, want 1", len(got))
	}

	// Unknown id: no error, no-op.
	if err := st.Delete(ctx, acme, []string{"does-not-exist"}); err != nil {
		t.Fatalf("Delete (unknown id): %v", err)
	}

	// Delete scoped to the OWNING tenant actually removes the point.
	if err := st.Delete(ctx, acme, []string{acmePoint.ID}); err != nil {
		t.Fatalf("Delete (owning scope): %v", err)
	}
	got, err = st.Search(ctx, acme, embedOne(t, emb, "acme's fact"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Delete did not remove the point: got %d, want 0", len(got))
	}
}

// TestSearchKindFilter is the ADR-0017 phase-1 DoD: Search with a kind filter
// returns only points of that kind, while the (tenant,user) isolation filter
// still applies.
func TestSearchKindFilter(t *testing.T) {
	emb := NewHashEmbedder(0)
	st := NewInMemoryStore(emb.Dimension())
	ctx := context.Background()
	acme := Scope{tenant: "acme", user: "alice"}
	globex := Scope{tenant: "globex", user: "bob"}

	const content = "database migration runbook"
	turnPt := point(ctx, acme, emb, KindTurn, "turn-1", content)
	factPt := point(ctx, acme, emb, KindFact, "fact-1", content)
	otherTenantFact := point(ctx, globex, emb, KindFact, "fact-1", content)
	if err := st.Upsert(ctx, acme, []Point{turnPt, factPt}); err != nil {
		t.Fatalf("Upsert acme: %v", err)
	}
	if err := st.Upsert(ctx, globex, []Point{otherTenantFact}); err != nil {
		t.Fatalf("Upsert globex: %v", err)
	}

	q := embedOne(t, emb, content)

	// No kind filter (zero-arg call): both of acme's points come back, never
	// globex's — preserves the pre-ADR-0017 "all kinds" behavior.
	all, err := st.Search(ctx, acme, q, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("unfiltered search = %d points, want 2", len(all))
	}

	// Kind filter to Fact only: exactly acme's Fact point, still isolated from
	// globex's colliding-content Fact point.
	factsOnly, err := st.Search(ctx, acme, q, 10, KindFact)
	if err != nil {
		t.Fatal(err)
	}
	if len(factsOnly) != 1 || factsOnly[0].ID != factPt.ID {
		t.Fatalf("Fact-filtered search = %+v, want exactly acme's fact point", factsOnly)
	}
	if factsOnly[0].TenantID != "acme" {
		t.Fatalf("kind filter leaked another tenant's point: %+v", factsOnly[0])
	}

	// Kind filter to Turn only: exactly the turn point.
	turnsOnly, err := st.Search(ctx, acme, q, 10, KindTurn)
	if err != nil {
		t.Fatal(err)
	}
	if len(turnsOnly) != 1 || turnsOnly[0].ID != turnPt.ID {
		t.Fatalf("Turn-filtered search = %+v, want exactly the turn point", turnsOnly)
	}

	// Kind filter to Document (none exist): empty result, not an error.
	docsOnly, err := st.Search(ctx, acme, q, 10, KindDocument)
	if err != nil {
		t.Fatal(err)
	}
	if len(docsOnly) != 0 {
		t.Fatalf("Document-filtered search = %d points, want 0", len(docsOnly))
	}
}
