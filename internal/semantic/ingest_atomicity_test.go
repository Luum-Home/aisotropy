package semantic

import (
	"context"
	"errors"
	"testing"
)

// What these tests protect, and why the old order could not.
//
// Ingest deletes a document's previously-stored chunks before writing the fresh
// ones. That delete is NOT gratuitous: it is what makes re-ingesting a SHRINKING
// document correct, because PointID collisions only overwrite a same-or-larger
// chunk count and would otherwise leave orphans from the longer prior version
// (the gap ADR-0018 flagged).
//
// The defect was its ORDER. The delete ran first, then the fallible work —
// chunking, embedding, a vector-count check — and the Upsert ran only after the
// whole loop. So any failure left the store stripped and rewrote nothing, and a
// failure on document N destroyed documents 1..N-1 as well, since their points
// were still sitting in an accumulator that the early return discarded.
//
// The invariant both tests assert is the one a caller can actually rely on:
// **when Ingest returns an error, the store is exactly as it was.** Found by
// scripts/robustness-audit.sh (BLOCKER DESTRUCTIVE_REINGEST) on 2026-08-08.

// countPoints returns how many stored points for sc carry the given source,
// via Search over the whole scope — enough for these tests, which store a
// handful of points.
func countPoints(t *testing.T, st Store, sc Scope, emb Embedder, source string) int {
	t.Helper()
	q, err := emb.Embed(context.Background(), []string{"anything"})
	if err != nil || len(q) == 0 {
		t.Fatalf("probe embed: %v", err)
	}
	hits, err := st.Search(context.Background(), sc, q[0], 1000)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	n := 0
	for _, h := range hits {
		if h.Source == source {
			n++
		}
	}
	return n
}

// TestIngestFailureLeavesEarlierDocumentsIntact is the batch case, and the one
// that makes this a data-loss bug rather than a retry annoyance: a single bad
// document used to take the whole batch's previously-ingested content with it.
func TestIngestFailureLeavesEarlierDocumentsIntact(t *testing.T) {
	ctx := context.Background()
	sc := Scope{tenant: "acme", user: "alice"}
	emb := countMismatchEmbedder{dim: 8}
	store := NewInMemoryStore(8)

	docs := []Document{
		{ID: "keeper", Content: "the first document, which must survive a later failure"},
		{ID: "breaker", Content: "the second document, whose embedding will fail"},
	}
	if _, err := Ingest(ctx, store, emb, sc, DefaultChunkOptions(), docs); err != nil {
		t.Fatalf("baseline Ingest: %v", err)
	}
	before := countPoints(t, store, sc, emb, "keeper")
	if before == 0 {
		t.Fatal("baseline stored no chunks for \"keeper\" — the test cannot prove anything")
	}

	// Re-ingest the same batch with an embedder that fails outright. "keeper" is
	// processed first and successfully; "breaker" then fails.
	boom := errors.New("embedder unavailable")
	_, err := Ingest(ctx, store, countMismatchEmbedder{dim: 8, err: boom}, sc, DefaultChunkOptions(), docs)
	if !errors.Is(err, boom) {
		t.Fatalf("expected the embedder failure to surface, got %v", err)
	}

	if after := countPoints(t, store, sc, emb, "keeper"); after != before {
		t.Errorf("a failed re-ingest destroyed an unrelated document in the same batch: "+
			"\"keeper\" had %d chunks before and %d after — Ingest must leave the store "+
			"untouched when it returns an error", before, after)
	}
}

// TestIngestFailureLeavesTheFailingDocumentIntact is the single-document case:
// the old content is the only content there is, so deleting it before knowing a
// replacement can be produced trades a stale answer for no answer at all.
func TestIngestFailureLeavesTheFailingDocumentIntact(t *testing.T) {
	ctx := context.Background()
	sc := Scope{tenant: "acme", user: "alice"}
	emb := countMismatchEmbedder{dim: 8}
	store := NewInMemoryStore(8)

	docs := []Document{{ID: "solo", Content: "content that is already ingested and useful"}}
	if _, err := Ingest(ctx, store, emb, sc, DefaultChunkOptions(), docs); err != nil {
		t.Fatalf("baseline Ingest: %v", err)
	}
	before := countPoints(t, store, sc, emb, "solo")
	if before == 0 {
		t.Fatal("baseline stored no chunks for \"solo\"")
	}

	boom := errors.New("embedder unavailable")
	if _, err := Ingest(ctx, store, countMismatchEmbedder{dim: 8, err: boom}, sc,
		DefaultChunkOptions(), docs); !errors.Is(err, boom) {
		t.Fatalf("expected the embedder failure to surface, got %v", err)
	}

	if after := countPoints(t, store, sc, emb, "solo"); after != before {
		t.Errorf("a failed re-ingest destroyed the document it was updating: %d chunks "+
			"before, %d after. The stale version is strictly better than nothing.", before, after)
	}
}
