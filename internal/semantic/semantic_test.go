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

// helper: build a scoped Point with a UUIDv5 id derived from messageID.
func point(sc Scope, emb Embedder, messageID, content string) Point {
	return Point{
		ID:       PointID(sc, messageID),
		TenantID: sc.TenantID,
		UserID:   sc.UserID,
		Content:  content,
		Vector:   mustEmbed(emb, content),
	}
}

func mustEmbed(emb Embedder, text string) []float32 {
	vs, _ := emb.Embed(context.Background(), []string{text})
	return vs[0]
}

func TestPointIDDeterministicAndScoped(t *testing.T) {
	sc := Scope{TenantID: "acme", UserID: "alice"}
	a := PointID(sc, "msg-1")
	if a != PointID(sc, "msg-1") {
		t.Fatal("PointID not deterministic")
	}
	if a == PointID(sc, "msg-2") {
		t.Fatal("different messages share an id")
	}
	if a == PointID(Scope{TenantID: "globex", UserID: "alice"}, "msg-1") {
		t.Fatal("id collides across tenants — isolation risk in the idempotency key")
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
	sc := Scope{TenantID: "acme", UserID: "alice"}
	ctx := context.Background()

	docs := map[string]string{
		"m1": "postgres database migration and schema changes",
		"m2": "kubernetes cluster autoscaling and pods",
		"m3": "invoice billing pdf generation",
	}
	var pts []Point
	for id, c := range docs {
		pts = append(pts, point(sc, emb, id, c))
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
		sc := Scope{TenantID: tenant, UserID: user}
		if err := st.Upsert(ctx, sc, []Point{point(sc, emb, msg, content)}); err != nil {
			t.Fatalf("Upsert %s/%s: %v", tenant, user, err)
		}
	}
	mk("acme", "alice", "a")
	mk("acme", "bob", "b")
	mk("globex", "alice", "c")

	sc := Scope{TenantID: "acme", UserID: "alice"}
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
	sc := Scope{TenantID: "acme", UserID: "alice"}
	ctx := context.Background()

	p1 := point(sc, emb, "msg-42", "first version of the chunk")
	if err := st.Upsert(ctx, sc, []Point{p1}); err != nil {
		t.Fatal(err)
	}
	// Same message id, updated content — must replace, not duplicate.
	p2 := point(sc, emb, "msg-42", "updated version of the chunk")
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
	var cands []ScoredPoint
	for i := 0; i < 10; i++ {
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
	sc := Scope{TenantID: "acme", UserID: "alice"}
	ctx := context.Background()

	// Scope mismatch.
	bad := Point{ID: "x", TenantID: "globex", UserID: "alice", Vector: mustEmbed(emb, "hi")}
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
