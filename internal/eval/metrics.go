package eval

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
