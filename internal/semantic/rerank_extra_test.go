package semantic

import (
	"context"
	"testing"
)

// TestRerankEmptyInputsReturnNil verifies the two short-circuit guards: a
// non-positive topK and an empty candidate slice both yield nil (never a
// panic, never a partial slice).
func TestRerankEmptyInputsReturnNil(t *testing.T) {
	r := LexicalReranker{}
	ctx := context.Background()

	if got := r.Rerank(ctx, "query", nil, 4); got != nil {
		t.Fatalf("Rerank(empty candidates) = %#v, want nil", got)
	}
	cands := []ScoredPoint{{Point: Point{ID: "a"}, Score: 1}}
	if got := r.Rerank(ctx, "query", cands, 0); got != nil {
		t.Fatalf("Rerank(topK=0) = %#v, want nil", got)
	}
	if got := r.Rerank(ctx, "query", cands, -3); got != nil {
		t.Fatalf("Rerank(topK<0) = %#v, want nil", got)
	}
}

// TestRerankTieBreaksByID verifies that when two candidates receive an
// identical blended score, the stable sort falls back to ascending ID — a
// deterministic order independent of input order. An empty query drives every
// lexical overlap to 0 (jaccard's empty-set branch), so equal vector Scores
// alone decide the tie.
func TestRerankTieBreaksByID(t *testing.T) {
	r := LexicalReranker{Alpha: 0.5}
	// Same Score, empty query -> same overlap (0) -> equal blended score.
	// Deliberately supply them out of ID order to prove the reranker reorders.
	cands := []ScoredPoint{
		{Point: Point{ID: "zeta", Content: "irrelevant text"}, Score: 0.5},
		{Point: Point{ID: "alpha", Content: "other text"}, Score: 0.5},
	}
	got := r.Rerank(context.Background(), "", cands, 2)
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	if got[0].ID != "alpha" || got[1].ID != "zeta" {
		t.Fatalf("tie-break order = [%s %s], want [alpha zeta] (ascending ID)", got[0].ID, got[1].ID)
	}
}

// TestJaccardEmptySetIsZero verifies the empty-set guard directly: an overlap
// against an empty term set is 0, never a divide-by-zero or NaN.
func TestJaccardEmptySetIsZero(t *testing.T) {
	nonEmpty := termSet("hello world")
	if got := jaccard(nil, nonEmpty); got != 0 {
		t.Fatalf("jaccard(empty, nonEmpty) = %v, want 0", got)
	}
	if got := jaccard(nonEmpty, map[string]struct{}{}); got != 0 {
		t.Fatalf("jaccard(nonEmpty, empty) = %v, want 0", got)
	}
}
