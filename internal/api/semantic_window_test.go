package api

import (
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/semantic"
)

func pt(id, source string, score float32, created time.Time) semantic.ScoredPoint {
	return semantic.ScoredPoint{
		Point: semantic.Point{ID: id, Source: source, Kind: semantic.KindDocument, CreatedAt: created},
		Score: score,
	}
}

func ids(pts []semantic.ScoredPoint) []string {
	out := make([]string, 0, len(pts))
	for _, p := range pts {
		out = append(out, p.ID)
	}
	return out
}

func equalIDs(got []semantic.ScoredPoint, want []string) bool {
	g := ids(got)
	if len(g) != len(want) {
		return false
	}
	for i := range g {
		if g[i] != want[i] {
			return false
		}
	}
	return true
}

// TestCoalesceSiblings_PullsSameDocumentChunks is the behavior the adoption
// exists for: without it the window is N fragments of N different documents,
// each stripped of context. With it, the best document arrives whole.
func TestCoalesceSiblings_PullsSameDocumentChunks(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	// Ranked hits: three unrelated documents. docA's siblings scored just below
	// the cutoff and would have been discarded.
	primary := []semantic.ScoredPoint{
		pt("a1", "docA", 0.95, base),
		pt("b1", "docB", 0.80, base),
		pt("c1", "docC", 0.70, base),
	}
	pool := append([]semantic.ScoredPoint{}, primary...)
	pool = append(pool,
		pt("a2", "docA", 0.62, base.Add(time.Minute)),
		pt("a3", "docA", 0.61, base.Add(2*time.Minute)),
		pt("b2", "docB", 0.55, base.Add(time.Minute)),
	)

	got := coalesceSiblings(primary, pool, 4)

	// docA arrives complete and first; the remaining budget goes to the next
	// ranked hit rather than to a third unrelated fragment.
	if want := []string{"a1", "a2", "a3", "b1"}; !equalIDs(got, want) {
		t.Errorf("window not composed document-first:\n  got  %v\n  want %v", ids(got), want)
	}
}

// TestCoalesceSiblings_TopHitUnchanged locks the invariant that makes this safe
// to enable by default: the best-scoring point stays first, so any metric
// computed on the top hit (the arena's RAG hit@1) cannot move because of this
// re-composition.
func TestCoalesceSiblings_TopHitUnchanged(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	primary := []semantic.ScoredPoint{
		pt("top", "docA", 0.99, base),
		pt("b1", "docB", 0.80, base),
	}
	pool := append([]semantic.ScoredPoint{}, primary...)
	pool = append(pool, pt("a2", "docA", 0.10, base), pt("b2", "docB", 0.05, base))

	for _, limit := range []int{1, 2, 3, 4, 8} {
		got := coalesceSiblings(primary, pool, limit)
		if len(got) == 0 {
			t.Fatalf("limit %d: returned nothing", limit)
		}
		if got[0].ID != "top" {
			t.Errorf("limit %d: top hit moved to %q — hit@1 would change", limit, got[0].ID)
		}
	}
}

// TestCoalesceSiblings_RespectsBudget — this re-composes the window, it never
// grows the prompt. A sibling pull that overshot the budget would silently
// inflate every hydrated request.
func TestCoalesceSiblings_RespectsBudget(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	primary := []semantic.ScoredPoint{pt("a1", "docA", 0.9, base)}
	pool := []semantic.ScoredPoint{
		pt("a1", "docA", 0.9, base),
		pt("a2", "docA", 0.8, base), pt("a3", "docA", 0.7, base),
		pt("a4", "docA", 0.6, base), pt("a5", "docA", 0.5, base),
	}
	for _, limit := range []int{1, 2, 3, 4} {
		if got := coalesceSiblings(primary, pool, limit); len(got) != limit {
			t.Errorf("limit %d: got %d points (%v) — budget must be exact when the pool can fill it", limit, len(got), ids(got))
		}
	}
}

// TestCoalesceSiblings_NoDuplicates — a point reachable both as a ranked hit and
// as another hit's sibling must be injected once. A duplicated chunk wastes
// budget and reads to the model as emphasis.
func TestCoalesceSiblings_NoDuplicates(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	primary := []semantic.ScoredPoint{
		pt("a1", "docA", 0.9, base),
		pt("a2", "docA", 0.85, base), // same doc, also a ranked hit
	}
	pool := append([]semantic.ScoredPoint{}, primary...)

	got := coalesceSiblings(primary, pool, 4)
	seen := map[string]int{}
	for _, p := range got {
		seen[p.ID]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("point %q injected %d times", id, n)
		}
	}
}

// TestCoalesceSiblings_SourcelessPointsPassThrough — facts and turns carry no
// Source (only KindDocument chunks do). They have no sibling group by
// definition and must survive untouched, in rank order.
func TestCoalesceSiblings_SourcelessPointsPassThrough(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	primary := []semantic.ScoredPoint{
		pt("f1", "", 0.9, base),
		pt("f2", "", 0.8, base),
	}
	got := coalesceSiblings(primary, primary, 4)
	if want := []string{"f1", "f2"}; !equalIDs(got, want) {
		t.Errorf("sourceless points reordered: got %v want %v", ids(got), want)
	}
}

// TestCoalesceSiblings_Deterministic — equally-scoring siblings must not swap
// between runs; the assembled prompt has to be reproducible or the arena's
// deterministic scoring loses its ground truth.
func TestCoalesceSiblings_Deterministic(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	primary := []semantic.ScoredPoint{pt("a1", "docA", 0.9, base)}
	pool := []semantic.ScoredPoint{
		pt("a1", "docA", 0.9, base),
		// identical scores, distinct CreatedAt/ID — the tiebreak must decide.
		pt("a3", "docA", 0.5, base.Add(2*time.Minute)),
		pt("a2", "docA", 0.5, base.Add(time.Minute)),
	}
	first := ids(coalesceSiblings(primary, pool, 3))
	for i := 0; i < 30; i++ {
		got := ids(coalesceSiblings(primary, pool, 3))
		if len(got) != len(first) {
			t.Fatalf("iteration %d: length changed", i)
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("iteration %d: order changed\n  %v\n  %v", i, first, got)
			}
		}
	}
	// CreatedAt ascending is the documented tiebreak, so a2 (earlier) precedes a3.
	if want := []string{"a1", "a2", "a3"}; !equalIDs(coalesceSiblings(primary, pool, 3), want) {
		t.Errorf("tiebreak not applied: got %v want %v", first, want)
	}
}

// TestCoalesceSiblings_EmptyAndDegenerate — hydration must never panic on an
// empty search result; it degrades to "no semantic context", never to a crash.
func TestCoalesceSiblings_EmptyAndDegenerate(t *testing.T) {
	if got := coalesceSiblings(nil, nil, 4); len(got) != 0 {
		t.Errorf("nil input produced %v", ids(got))
	}
	base := time.Unix(1700000000, 0).UTC()
	one := []semantic.ScoredPoint{pt("a1", "docA", 0.9, base)}
	if got := coalesceSiblings(one, one, 0); len(got) != 1 {
		t.Errorf("limit 0 must return the input untouched rather than an empty window, got %v", ids(got))
	}
}
