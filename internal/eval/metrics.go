package eval

import "math"

// Retrieval metrics — pure, deterministic, model-free (step 1 of the pipeline:
// evaluate the RETRIEVER component on its own terms). Each function takes the
// ordered list of retrieved IDs (rank 1 first) and the labeled set of relevant
// IDs, and returns a score in [0,1]. They make no I/O and no LLM call, so they
// are the objective ground the rest of the harness stands on: a retriever
// regression shows up here before it ever reaches a judge.
//
// Convention shared by all four: `retrieved` is deduplicated in first-seen
// order before scoring (a store returning the same id twice must not inflate a
// hit), and an empty relevant set makes recall/precision undefined — handled
// per function below, never a divide-by-zero.

// idSet builds a membership set from a slice of ids.
func idSet(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// dedupTopK returns the first-seen unique ids of `retrieved`, truncated to the
// top k (k <= 0 yields the empty slice). This is the exact ranked prefix the
// @k metrics score.
func dedupTopK(retrieved []string, k int) []string {
	if k <= 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(retrieved))
	out := make([]string, 0, k)
	for _, id := range retrieved {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if len(out) == k {
			break
		}
	}
	return out
}

// PrecisionAtK is the fraction of the top-k retrieved results that are
// relevant: (# relevant in top k) / k. The denominator is k (not the number
// actually returned), so a retriever that returns only 2 results for k=5 is
// correctly penalized for the 3 it failed to fill. k <= 0 returns 0.
func PrecisionAtK(retrieved, relevant []string, k int) float64 {
	if k <= 0 {
		return 0
	}
	rel := idSet(relevant)
	hits := 0
	for _, id := range dedupTopK(retrieved, k) {
		if _, ok := rel[id]; ok {
			hits++
		}
	}
	return float64(hits) / float64(k)
}

// RecallAtK is the fraction of all relevant items that appear in the top k:
// (# relevant in top k) / (total relevant). An empty relevant set returns 0
// (recall is undefined with nothing to recall; the caller decides whether such
// a case belongs in the suite at all).
func RecallAtK(retrieved, relevant []string, k int) float64 {
	rel := idSet(relevant)
	if len(rel) == 0 {
		return 0
	}
	hits := 0
	for _, id := range dedupTopK(retrieved, k) {
		if _, ok := rel[id]; ok {
			hits++
		}
	}
	return float64(hits) / float64(len(rel))
}

// F1 is the harmonic mean of precision and recall, the single number that
// rewards a retriever only when BOTH are high. Returns 0 when either input is
// 0 (their harmonic mean is 0, and this also guards the p+r==0 divide).
func F1(precision, recall float64) float64 {
	if precision <= 0 || recall <= 0 {
		return 0
	}
	return 2 * precision * recall / (precision + recall)
}

// NDCGAtK is the Normalized Discounted Cumulative Gain over the top k, with
// BINARY relevance (an id is relevant with gain 1, or irrelevant with gain 0 —
// the labeling this harness's gold sets carry). It rewards a retriever for
// placing relevant items EARLY, not merely for including them: the gain at rank
// r (1-based) is discounted by 1/log2(r+1), so rank 1 is worth 1.0, rank 2 ~0.63,
// rank 3 ~0.5, etc.
//
//	DCG@k  = Σ over the top-k ranks r whose id is relevant of 1/log2(r+1)
//	IDCG@k = the DCG of the ideal ordering (all relevant items packed at the top),
//	         i.e. Σ_{r=1..min(k, #relevant)} 1/log2(r+1)
//	nDCG@k = DCG@k / IDCG@k, in [0,1]
//
// It complements RecallAtK (which is order-blind within the cutoff) and MRR
// (which only sees the FIRST hit): nDCG credits every relevant item AND its
// position, so it is the right primary number when a query can have several
// relevant passages. `retrieved` is deduped first-seen exactly like the other
// @k metrics. An empty relevant set — nothing ideal to normalize against —
// returns 0, never a divide-by-zero. k <= 0 returns 0.
func NDCGAtK(retrieved, relevant []string, k int) float64 {
	if k <= 0 {
		return 0
	}
	rel := idSet(relevant)
	if len(rel) == 0 {
		return 0
	}

	var dcg float64
	for i, id := range dedupTopK(retrieved, k) {
		if _, ok := rel[id]; ok {
			dcg += 1 / math.Log2(float64(i+2)) // rank r = i+1 ⇒ log2(r+1) = log2(i+2)
		}
	}

	ideal := len(rel)
	if ideal > k {
		ideal = k
	}
	var idcg float64
	for r := 1; r <= ideal; r++ {
		idcg += 1 / math.Log2(float64(r+1))
	}
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

// MRR returns the reciprocal rank of the FIRST relevant item in the ranked
// results: 1/rank (rank counted from 1), or 0 if no relevant item is
// retrieved. Averaging this across a set of queries yields the Mean Reciprocal
// Rank proper (see RetrievalReport.MeanMRR) — this is the per-query term, kept
// as its own function so it composes with the other three over one case.
func MRR(retrieved, relevant []string) float64 {
	rel := idSet(relevant)
	if len(rel) == 0 {
		return 0
	}
	seen := make(map[string]struct{}, len(retrieved))
	rank := 0
	for _, id := range retrieved {
		if _, ok := seen[id]; ok {
			continue // dedup: a repeated id does not advance the rank
		}
		seen[id] = struct{}{}
		rank++
		if _, ok := rel[id]; ok {
			return 1 / float64(rank)
		}
	}
	return 0
}
