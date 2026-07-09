package semantic

import (
	"context"
	"sort"
)

// DefaultTopK is the number of fragments a rerank trims to before injection —
// the "≤ 4 fragments" the roadmap's Phase 4 targets (docs/roadmap/milestones.md).
const DefaultTopK = 4

// LexicalReranker reorders ANN candidates by a blend of their vector score and
// lexical (Jaccard) overlap with the query, then trims to topK. It is
// deterministic and dependency-free — a stand-in for the eventual cross-encoder
// reranker (ADR-0006: local BGE-reranker vs Cohere, still open). It "maintains
// relevance" by lifting candidates that literally share query terms, which a
// pure-ANN order can bury under near-duplicate neighbors.
type LexicalReranker struct {
	// Alpha weights lexical overlap against the vector score, in [0,1]:
	// 0 = pure vector order, 1 = pure lexical. Defaults to 0.5 when <= 0.
	Alpha float64
}

// Rerank returns the topK most relevant candidates for query, reordered. It
// never returns more than topK (and never more than len(candidates)).
func (r LexicalReranker) Rerank(_ context.Context, query string, candidates []ScoredPoint, topK int) []ScoredPoint {
	if topK <= 0 || len(candidates) == 0 {
		return nil
	}
	alpha := r.Alpha
	if alpha <= 0 {
		alpha = 0.5
	}
	qterms := termSet(query)

	type ranked struct {
		p     ScoredPoint
		score float64
	}
	rs := make([]ranked, len(candidates))
	for i, c := range candidates {
		overlap := jaccard(qterms, termSet(c.Content))
		rs[i] = ranked{p: c, score: (1-alpha)*float64(c.Score) + alpha*overlap}
	}
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].score != rs[j].score {
			return rs[i].score > rs[j].score
		}
		return rs[i].p.ID < rs[j].p.ID
	})

	n := topK
	if n > len(rs) {
		n = len(rs)
	}
	out := make([]ScoredPoint, n)
	for i := 0; i < n; i++ {
		out[i] = rs[i].p
	}
	return out
}

// termSet is the set of distinct tokens in s (shares tokenize with the embedder).
func termSet(s string) map[string]struct{} {
	m := make(map[string]struct{})
	for _, t := range tokenize(s) {
		m[t] = struct{}{}
	}
	return m
}

// jaccard is |a∩b| / |a∪b|, or 0 if either set is empty.
func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for t := range a {
		if _, ok := b[t]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
