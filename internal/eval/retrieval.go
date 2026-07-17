package eval

import (
	"context"
	"fmt"

	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// Retrieval evaluation (step 1 wired to aisotropy's REAL retriever, not a
// toy): run a set of labeled (query -> relevant IDs) cases through the actual
// semantic.Store search — tenant-scoped exactly like production — and compute
// per-case and aggregate IR metrics. Swapping the in-process InMemoryStore for
// a real QdrantStore changes nothing here: EvalRetrieval talks to the Store
// interface, so it measures whatever retriever the product actually serves.

// RetrievalCase is one labeled retrieval test: a Query and the set of point
// IDs a human (or a trusted reference) marked relevant for it. The IDs must be
// the semantic.Point IDs the store returns (semantic.PointID) so retrieved and
// relevant sets are directly comparable.
type RetrievalCase struct {
	Name        string
	Query       string
	RelevantIDs []string
}

// RetrievalResult is the scored outcome of one case at cutoff k.
type RetrievalResult struct {
	Case         RetrievalCase
	RetrievedIDs []string
	Precision    float64
	Recall       float64
	F1           float64
	MRR          float64
	// NDCG is the case's Normalized Discounted Cumulative Gain at cutoff k
	// (binary relevance) — position-aware, so it captures ranking quality the
	// order-blind Recall and single-hit MRR miss. See NDCGAtK.
	NDCG float64
}

// RetrievalReport is the aggregate over every case, at cutoff k. The Mean*
// fields are the arithmetic means across cases (MeanMRR is the Mean Reciprocal
// Rank proper — the mean of each case's reciprocal rank). Empty case set
// yields a zero-valued report with no cases, not an error.
type RetrievalReport struct {
	K             int
	Cases         []RetrievalResult
	MeanPrecision float64
	MeanRecall    float64
	MeanF1        float64
	MeanMRR       float64
	// MeanNDCG is the arithmetic mean of each case's nDCG@k — the aggregate
	// ranking-quality number for the suite.
	MeanNDCG float64
}

// EvalRetrieval runs each case through store.Search within scope sc, embeds
// the query with emb, and scores the retrieved IDs against the labeled
// relevant IDs at cutoff k. It fails closed: an embedder or store error on any
// case aborts and returns that error (a retrieval harness that silently skips
// failed cases would report a falsely rosy mean). k must be positive.
//
// The scope is threaded straight into every Search, so the isolation gate
// holds during evaluation exactly as it does in production: a case is measured
// within one tenant/user, never against a widened cross-tenant view.
func EvalRetrieval(ctx context.Context, store semantic.Store, sc semantic.Scope, emb semantic.Embedder, cases []RetrievalCase, k int) (RetrievalReport, error) {
	if store == nil {
		return RetrievalReport{}, fmt.Errorf("eval: EvalRetrieval requires a non-nil semantic.Store")
	}
	if emb == nil {
		return RetrievalReport{}, fmt.Errorf("eval: EvalRetrieval requires a non-nil semantic.Embedder")
	}
	if k <= 0 {
		return RetrievalReport{}, fmt.Errorf("eval: EvalRetrieval requires a positive k, got %d", k)
	}

	report := RetrievalReport{K: k, Cases: make([]RetrievalResult, 0, len(cases))}
	var sumP, sumR, sumF1, sumMRR, sumNDCG float64

	for _, c := range cases {
		if err := ctx.Err(); err != nil {
			return RetrievalReport{}, err
		}

		// Embed the query with the e5 "query: " role (ADR-0054) via the shared
		// choke point: on an e5 RoleEmbedder it applies the query prefix so the
		// search matches a "passage: "-prefixed corpus (the very asymmetry e5 is
		// trained on); on a non-e5/Hash embedder EmbedQuery degrades to a plain
		// Embed, so this is byte-identical for the deterministic test embedder and
		// strictly correct for the real one — measuring the retriever the way
		// production (chat_stream's hydrateSystemPrompt) actually queries it.
		vecs, err := semantic.EmbedQuery(ctx, emb, []string{c.Query})
		if err != nil {
			return RetrievalReport{}, fmt.Errorf("eval: embed query for case %q: %w", c.Name, err)
		}
		if len(vecs) != 1 {
			return RetrievalReport{}, fmt.Errorf("eval: embedder returned %d vectors for 1 query in case %q", len(vecs), c.Name)
		}

		hits, err := store.Search(ctx, sc, vecs[0], k)
		if err != nil {
			return RetrievalReport{}, fmt.Errorf("eval: search for case %q: %w", c.Name, err)
		}

		retrieved := make([]string, len(hits))
		for i, h := range hits {
			retrieved[i] = h.ID
		}

		p := PrecisionAtK(retrieved, c.RelevantIDs, k)
		r := RecallAtK(retrieved, c.RelevantIDs, k)
		f1 := F1(p, r)
		mrr := MRR(retrieved, c.RelevantIDs)
		ndcg := NDCGAtK(retrieved, c.RelevantIDs, k)

		report.Cases = append(report.Cases, RetrievalResult{
			Case:         c,
			RetrievedIDs: retrieved,
			Precision:    p,
			Recall:       r,
			F1:           f1,
			MRR:          mrr,
			NDCG:         ndcg,
		})
		sumP += p
		sumR += r
		sumF1 += f1
		sumMRR += mrr
		sumNDCG += ndcg
	}

	if n := float64(len(report.Cases)); n > 0 {
		report.MeanPrecision = sumP / n
		report.MeanRecall = sumR / n
		report.MeanF1 = sumF1 / n
		report.MeanMRR = sumMRR / n
		report.MeanNDCG = sumNDCG / n
	}

	return report, nil
}
