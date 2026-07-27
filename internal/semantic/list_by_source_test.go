package semantic

import (
	"context"
	"testing"
	"time"
)

// ListBySource is the read that lets retrieval reach a chunk the ANN search
// never returned. Its two load-bearing properties are therefore: it returns the
// WHOLE document in reading order (including chunks no query would surface),
// and it is scoped exactly as tightly as Search — a positional read that could
// cross a tenant would be a far worse hole than a missing chunk.

func chunkPoint(id, tenant, user, source string, idx int, content string) Point {
	return Point{
		ID:         id,
		TenantID:   tenant,
		UserID:     user,
		Kind:       KindDocument,
		Source:     source,
		Content:    content,
		ChunkIndex: idx,
		Vector:     []float32{float32(idx), 1, 0, 0},
		CreatedAt:  time.Unix(1700000000, 0).UTC(),
	}
}

func TestListBySource_ReturnsWholeDocumentInReadingOrder(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryStore(4)
	sc, err := NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}

	// Deliberately upserted out of order — the read must impose reading order,
	// not echo insertion order.
	if err := store.Upsert(ctx, sc, []Point{
		chunkPoint("c2", "acme", "alice", "docA", 2, "third"),
		chunkPoint("c0", "acme", "alice", "docA", 0, "first"),
		chunkPoint("c1", "acme", "alice", "docA", 1, "second"),
		chunkPoint("other", "acme", "alice", "docB", 0, "different document"),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := store.ListBySource(ctx, sc, KindDocument, "docA")
	if err != nil {
		t.Fatalf("ListBySource: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d chunks, want 3 (docB must not leak in)", len(got))
	}
	for i, p := range got {
		if p.ChunkIndex != i {
			t.Errorf("position %d holds chunk %d — not reading order: %+v", i, p.ChunkIndex, ids(got))
		}
		if p.Source != "docA" {
			t.Errorf("position %d belongs to %q, not the requested document", i, p.Source)
		}
	}
}

// TestListBySource_NeverCrossesScope — the isolation gate. A positional read
// bypasses the similarity ranking entirely, so if its scope filter were weaker
// than Search's it would be the easiest possible cross-tenant leak.
func TestListBySource_NeverCrossesScope(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryStore(4)

	victim, err := NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	if err := store.Upsert(ctx, victim, []Point{
		chunkPoint("v0", "acme", "alice", "shared-doc-id", 0, "alice's secret chunk"),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Same document id, different scopes — the id alone must grant nothing.
	for _, tc := range []struct{ name, tenant, user string }{
		{"other tenant", "globex", "alice"},
		{"other user, same tenant", "acme", "bob"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attacker, err := NewScope(tc.tenant, tc.user)
			if err != nil {
				t.Fatalf("NewScope: %v", err)
			}
			got, err := store.ListBySource(ctx, attacker, KindDocument, "shared-doc-id")
			if err != nil {
				t.Fatalf("ListBySource: %v", err)
			}
			if len(got) != 0 {
				t.Errorf("LEAK: %s read %d point(s) of another scope's document: %+v", tc.name, len(got), got)
			}
		})
	}
}

// TestListBySource_OmitsVectors — a positional read wants the text and its
// place; shipping embeddings back would multiply the response for nothing.
func TestListBySource_OmitsVectors(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryStore(4)
	sc, err := NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	if err := store.Upsert(ctx, sc, []Point{chunkPoint("c0", "acme", "alice", "docA", 0, "text")}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := store.ListBySource(ctx, sc, KindDocument, "docA")
	if err != nil {
		t.Fatalf("ListBySource: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if got[0].Vector != nil {
		t.Errorf("vector was returned (%d dims) — a positional read should omit it", len(got[0].Vector))
	}
	if got[0].Content == "" {
		t.Error("content is empty — the text is the point of this read")
	}
}

// TestListBySource_UnknownSourceIsEmptyNotError — asking for a document that is
// not there is a normal outcome (it may have been pruned), not a failure the
// hydration path should surface as an error.
func TestListBySource_UnknownSourceIsEmptyNotError(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryStore(4)
	sc, err := NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	got, err := store.ListBySource(ctx, sc, KindDocument, "never-ingested")
	if err != nil {
		t.Fatalf("unknown source should not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d points for an unknown source", len(got))
	}
}

func ids(pts []Point) []string {
	out := make([]string, 0, len(pts))
	for _, p := range pts {
		out = append(out, p.ID)
	}
	return out
}
