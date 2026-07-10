//go:build manual

// Live Qdrant validation for the multi-kind paths added after the original
// TestQdrantLive: Delete (by id), DeleteBySource (by document group key),
// PruneTurns (age + count eviction), and tenant-shared scope round-trip. These
// exercise the gRPC filter-delete / scroll paths that the in-memory store test
// double cannot cover. Run with a live node:
//
//	QDRANT_HOST=localhost QDRANT_PORT=6334 go test -tags manual \
//	  -run TestQdrantLiveNewPaths -v ./internal/semantic
package semantic

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestQdrantLiveNewPaths(t *testing.T) {
	ctx := context.Background()
	emb := NewHashEmbedder(0)
	st, err := OpenFromEnv(ctx, emb.Dimension())
	if err != nil {
		t.Fatalf("OpenFromEnv (qdrant): %v", err)
	}
	defer func() { _ = st.Close() }()

	tenant := fmt.Sprintf("manual-np-%d", os.Getpid())
	sc, err := NewScope(tenant, "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	other, err := NewScope(tenant, "bob")
	if err != nil {
		t.Fatalf("NewScope other: %v", err)
	}

	doc := func(scope Scope, kind Kind, id, source, content string, age time.Duration) Point {
		return Point{
			ID: PointID(scope, kind, id), TenantID: scope.Tenant(), UserID: scope.User(),
			Kind: kind, Source: source, Content: content, Vector: mustEmbed(ctx, emb, content),
			CreatedAt: time.Now().Add(-age).UTC(),
		}
	}

	// --- Delete (by id): a KindDocument point is removed for its scope only.
	p := doc(sc, KindDocument, "d1#0", "d1", "alpha beta gamma", 0)
	if err := st.Upsert(ctx, sc, []Point{p}); err != nil {
		t.Fatalf("Upsert d1: %v", err)
	}
	if err := st.Delete(ctx, sc, []string{p.ID}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ := st.Search(ctx, sc, mustEmbed(ctx, emb, "alpha beta"), 10, KindDocument)
	if len(got) != 0 {
		t.Fatalf("Delete left %d points, want 0", len(got))
	}
	t.Log("live Delete OK")

	// --- DeleteBySource: all chunks of a document go, regardless of count.
	chunks := []Point{
		doc(sc, KindDocument, "handbook#0", "handbook", "onboarding chapter one", 0),
		doc(sc, KindDocument, "handbook#1", "handbook", "onboarding chapter two", 0),
		doc(sc, KindDocument, "handbook#2", "handbook", "onboarding chapter three", 0),
	}
	if err := st.Upsert(ctx, sc, chunks); err != nil {
		t.Fatalf("Upsert handbook: %v", err)
	}
	if n, err := st.DeleteBySource(ctx, sc, KindDocument, "handbook"); err != nil || n < 1 {
		t.Fatalf("DeleteBySource = (%d,%v), want (>=1,nil)", n, err)
	}
	got, _ = st.Search(ctx, sc, mustEmbed(ctx, emb, "onboarding chapter"), 10, KindDocument)
	if len(got) != 0 {
		t.Fatalf("DeleteBySource left %d chunks, want 0", len(got))
	}
	t.Log("live DeleteBySource OK")

	// --- PruneTurns: an old KindTurn is evicted by age; a fresh one survives;
	// Document/Fact are never pruned; another scope is untouched.
	if err := st.Upsert(ctx, sc, []Point{
		doc(sc, KindTurn, "old", "", "stale conversation turn", 48*time.Hour),
		doc(sc, KindTurn, "new", "", "recent conversation turn", 0),
		doc(sc, KindDocument, "keep#0", "keep", "curated knowledge to keep", 48*time.Hour),
	}); err != nil {
		t.Fatalf("Upsert prune set: %v", err)
	}
	if err := st.Upsert(ctx, other, []Point{doc(other, KindTurn, "bobold", "", "bob stale turn", 48*time.Hour)}); err != nil {
		t.Fatalf("Upsert bob turn: %v", err)
	}
	if _, err := st.PruneTurns(ctx, sc, 24*time.Hour, 0); err != nil {
		t.Fatalf("PruneTurns: %v", err)
	}
	turns, _ := st.Search(ctx, sc, mustEmbed(ctx, emb, "conversation turn"), 10, KindTurn)
	if len(turns) != 1 || turns[0].Content != "recent conversation turn" {
		t.Fatalf("PruneTurns age: got %+v, want only the recent turn", turns)
	}
	docs, _ := st.Search(ctx, sc, mustEmbed(ctx, emb, "curated knowledge"), 10, KindDocument)
	if len(docs) != 1 {
		t.Fatalf("PruneTurns wrongly touched a Document: got %d, want 1", len(docs))
	}
	bobTurns, _ := st.Search(ctx, other, mustEmbed(ctx, emb, "bob stale"), 10, KindTurn)
	if len(bobTurns) != 1 {
		t.Fatalf("PruneTurns crossed scope: bob's turn was evicted")
	}
	t.Log("live PruneTurns OK (age eviction, kind- and scope-isolated)")

	// --- Tenant-shared scope round-trip: a tenant-only Document is stored and
	// retrieved under the tenant scope.
	ts, err := NewTenantScope(tenant)
	if err != nil {
		t.Fatalf("NewTenantScope: %v", err)
	}
	if err := st.Upsert(ctx, ts, []Point{doc(ts, KindDocument, "shared#0", "shared", "tenant wide policy document", 0)}); err != nil {
		t.Fatalf("Upsert tenant-shared: %v", err)
	}
	shared, _ := st.Search(ctx, ts, mustEmbed(ctx, emb, "tenant policy"), 10, KindDocument)
	if len(shared) != 1 || shared[0].UserID != "" {
		t.Fatalf("tenant-shared search: got %+v, want one empty-user Document", shared)
	}
	t.Log("live tenant-shared scope OK")

	// Cleanup: drop everything this test wrote for the two user scopes + shared.
	for _, s := range []Scope{sc, other, ts} {
		_, _ = st.DeleteBySource(ctx, s, KindDocument, "keep")
		_, _ = st.DeleteBySource(ctx, s, KindDocument, "shared")
		if turns, _ := st.Search(ctx, s, mustEmbed(ctx, emb, "turn"), 50, KindTurn); len(turns) > 0 {
			ids := make([]string, len(turns))
			for i, tp := range turns {
				ids[i] = tp.ID
			}
			_ = st.Delete(ctx, s, ids)
		}
	}
}
