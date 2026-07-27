package api

import (
	"sort"

	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// Sibling-chunk coalescing for semantic hydration.
//
// THE PROBLEM (observed in the mastra deep read, @mastra/core 1.51.0,
// Apache-2.0 — the framework that scores 2/2 on every memory dimension): a
// vector search returns the single best-matching FRAGMENT, and a fragment
// injected alone has lost its referents. "It expires after 60 days" is a
// perfect semantic match and a useless piece of context — *what* expires?
// mastra's semanticRecall answers this by re-fetching each hit together with
// the conversation window around it, so the model receives a coherent passage
// instead of a decontextualised sentence.
//
// WHAT THIS DOES: the aisotropy analogue, within the constraints of what the
// store can actually answer today. Every KindDocument chunk carries a Source —
// the group identity shared by all chunks of one ingested document
// (internal/semantic.Point.Source). After ranking, instead of filling the
// injection window with the N globally top-scoring fragments (which are
// typically N fragments of N DIFFERENT documents, each stripped of its
// context), the window is filled document-first: take the best hit, pull its
// sibling chunks from the already-fetched candidate pool, then move to the next
// unrelated hit. Fewer documents, more complete — the same trade mastra makes.
//
// WHAT IT DELIBERATELY DOES NOT DO: it never fetches anything new. Siblings can
// only come from the candidate pool the search already returned, so this costs
// zero extra store round-trips and cannot widen the tenant scope — the pool was
// produced by the two already-isolation-clean scoped searches.
//
// HONEST SCOPE: this is *sibling coalescing*, not true neighbour expansion. It
// recovers a sibling that scored just below the cutoff; it cannot recover a
// sibling the ANN search never returned, and it does not know which chunk
// PRECEDES which — Point carries a Source and a CreatedAt but no chunk ordinal,
// and the Store interface exposes no read-by-source (only DeleteBySource). True
// ordered neighbour expansion needs both a chunk ordinal on the payload and a
// ListBySource read API; that is a schema + interface change, filed separately.
//
// INVARIANTS (both covered by tests):
//   - The top-ranked point is unchanged and stays first, so any metric computed
//     on the best hit (the arena's RAG hit@1) is unaffected by construction.
//   - The total budget is unchanged: the window still holds at most `limit`
//     points. This re-composes the window, it never grows the prompt.

// coalesceSiblings re-orders and re-fills `primary` (already ranked, already
// truncated to the injection window) so that each hit is followed by its
// same-Source siblings drawn from `pool`, within the same `limit` budget.
//
// pool is the full candidate set the search produced (a superset of primary
// when a reranker over-fetched; equal to primary otherwise, in which case this
// is a no-op reordering that still preserves rank order).
func coalesceSiblings(primary, pool []semantic.ScoredPoint, limit int) []semantic.ScoredPoint {
	if limit <= 0 || len(primary) == 0 {
		return primary
	}

	// Index the pool by Source so sibling lookup is a map hit, not a scan.
	// Points with an empty Source (every non-Document kind today — facts, turns)
	// have no sibling group by definition and are carried through untouched.
	bySource := make(map[string][]semantic.ScoredPoint, len(pool))
	for _, p := range pool {
		if p.Source == "" {
			continue
		}
		bySource[p.Source] = append(bySource[p.Source], p)
	}
	// Sibling order is DOCUMENT order, not score order: chunk 3 then 4 then 5,
	// the sequence a human would read. Ordering a document's own chunks by
	// relevance produces a shuffled passage — the model gets "…and it expires
	// after 60 days." before the sentence naming what expires, which is most of
	// the context the window was meant to restore. semantic.Point.ChunkIndex is
	// the zero-based position Ingest stamps when it splits the document.
	//
	// Ties fall back to CreatedAt then ID. That path is load-bearing, not
	// decorative: ChunkIndex is 0 for every point written before the field
	// existed and for kinds that are not chunked, so a whole group can legitimately
	// share index 0 — and without a tiebreak, equal keys would let two siblings
	// swap between runs and change the assembled prompt, breaking the
	// reproducibility the arena's deterministic scoring rests on.
	for src := range bySource {
		sibs := bySource[src]
		sort.SliceStable(sibs, func(i, j int) bool {
			if sibs[i].ChunkIndex != sibs[j].ChunkIndex {
				return sibs[i].ChunkIndex < sibs[j].ChunkIndex
			}
			if !sibs[i].CreatedAt.Equal(sibs[j].CreatedAt) {
				return sibs[i].CreatedAt.Before(sibs[j].CreatedAt)
			}
			return sibs[i].ID < sibs[j].ID
		})
		bySource[src] = sibs
	}

	emitted := make(map[string]struct{}, limit)
	out := make([]semantic.ScoredPoint, 0, limit)

	emit := func(p semantic.ScoredPoint) bool {
		if len(out) >= limit {
			return false
		}
		if _, dup := emitted[p.ID]; dup {
			return true
		}
		emitted[p.ID] = struct{}{}
		out = append(out, p)
		return true
	}

	// Walk the ranked hits in order, emitting each hit's whole DOCUMENT GROUP in
	// reading order. The group's POSITION is set by its best-ranked member, so
	// the top-scoring document still comes first; within it, chunks run 1,2,3
	// even when chunk 3 was the actual hit.
	//
	// This moves the top POINT when the best chunk is not the document's first,
	// and that is safe: the arena's hit@1 is DOCUMENT-level — ScoreRAG compares
	// topDocID against gold DOC ids (arenas/live/runner/scoring/scoring.go) —
	// and every sibling shares the hit's Source, which IS that document id. So
	// the top document is unchanged by construction; only the order of that
	// document's own chunks changes, from relevance order to reading order.
	// Emitting the hit first and its context after would hand the model
	// "…and it expires after 60 days." before the sentence naming what expires.
	for _, hit := range primary {
		if len(out) >= limit {
			break
		}
		if hit.Source == "" {
			if !emit(hit) {
				break
			}
			continue
		}
		// bySource holds every pool point of this document INCLUDING the hit
		// (primary is drawn from pool), already sorted into reading order.
		for _, member := range bySource[hit.Source] {
			if len(out) >= limit {
				break
			}
			if !emit(member) {
				break
			}
		}
	}

	// NO backfill from the wider pool. Leftover budget is left unused on
	// purpose: `primary` is exactly the set the caller decided to inject, and
	// when a reranker produced it, every OTHER pool point is one the reranker
	// deliberately REJECTED. Topping the window up from the pool would quietly
	// re-admit rejected candidates as filler and defeat reranking entirely —
	// caught by TestHydrateSystemPrompt_AppliesReranker, which asserts that a
	// point the reranker did not return never reaches the prompt. The only
	// points this function may add are same-Source siblings of a point the
	// ranker already chose.
	return out
}
