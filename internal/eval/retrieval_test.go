package eval

import (
	"context"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// buildCorpus upserts a small labeled corpus into an in-process semantic store
// under sc, using readable point IDs so they line up with the retrieval golden
// file. Returns the store and embedder ready for EvalRetrieval.
func buildCorpus(t *testing.T, ctx context.Context, sc semantic.Scope, emb semantic.Embedder) semantic.Store {
	t.Helper()
	store := semantic.NewInMemoryStore(emb.Dimension())

	docs := []struct{ id, content string }{
		{"doc-france-capital", "The capital of France is Paris, a country in Western Europe."},
		{"doc-go-concurrency", "Goroutines are lightweight threads managed by the Go runtime for concurrency."},
		{"doc-go-channels", "Channels let goroutines communicate and synchronize in Go."},
		{"doc-photosynthesis", "Photosynthesis is how plants convert sunlight into chemical energy."},
		{"doc-cooking", "A simple recipe for baking bread with flour, yeast, and water."},
		{"doc-weather", "The weather forecast predicts heavy rain and wind tomorrow."},
	}

	points := make([]semantic.Point, 0, len(docs))
	for _, d := range docs {
		vecs, err := emb.Embed(ctx, []string{d.content})
		if err != nil {
			t.Fatalf("embed %q: %v", d.id, err)
		}
		points = append(points, semantic.Point{
			ID:       d.id,
			TenantID: sc.Tenant(),
			UserID:   sc.User(),
			Kind:     semantic.KindDocument,
			Content:  d.content,
			Vector:   vecs[0],
		})
	}
	if err := store.Upsert(ctx, sc, points); err != nil {
		t.Fatalf("upsert corpus: %v", err)
	}
	return store
}

func TestEvalRetrievalAgainstRealStore(t *testing.T) {
	ctx := context.Background()
	emb := semantic.NewHashEmbedder(0)

	sc, err := semantic.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	store := buildCorpus(t, ctx, sc, emb)

	cases, err := LoadRetrievalCasesFile("testdata/retrieval_golden.json")
	if err != nil {
		t.Fatalf("load golden: %v", err)
	}

	report, err := EvalRetrieval(ctx, store, sc, emb, cases, 3)
	if err != nil {
		t.Fatalf("EvalRetrieval: %v", err)
	}

	if len(report.Cases) != len(cases) {
		t.Fatalf("got %d case results, want %d", len(report.Cases), len(cases))
	}

	// Every labeled-relevant doc is lexically distinctive, so the real store
	// ranks it first: recall and reciprocal rank are perfect for each case.
	for _, cr := range report.Cases {
		if len(cr.RetrievedIDs) == 0 {
			t.Fatalf("case %q retrieved nothing from the real store", cr.Case.Name)
		}
		if !almostEqual(cr.Recall, 1.0) {
			t.Errorf("case %q recall = %v, want 1.0 (retrieved=%v)", cr.Case.Name, cr.Recall, cr.RetrievedIDs)
		}
		if !almostEqual(cr.MRR, 1.0) {
			t.Errorf("case %q MRR = %v, want 1.0 (retrieved=%v)", cr.Case.Name, cr.MRR, cr.RetrievedIDs)
		}
	}

	if !almostEqual(report.MeanRecall, 1.0) {
		t.Errorf("MeanRecall = %v, want 1.0", report.MeanRecall)
	}
	if !almostEqual(report.MeanMRR, 1.0) {
		t.Errorf("MeanMRR = %v, want 1.0", report.MeanMRR)
	}

	// The go case has two relevant docs out of three retrieved → precision 2/3;
	// the single-relevant cases → 1/3. This pins the precision math to the real
	// store's output, not a mock.
	byName := map[string]RetrievalResult{}
	for _, cr := range report.Cases {
		byName[cr.Case.Name] = cr
	}
	if got := byName["go-concurrency"].Precision; !almostEqual(got, 2.0/3.0) {
		t.Errorf("go-concurrency precision = %v, want 2/3", got)
	}
	if got := byName["france-capital"].Precision; !almostEqual(got, 1.0/3.0) {
		t.Errorf("france-capital precision = %v, want 1/3", got)
	}
}

// TestEvalRetrievalIsolation proves the harness measures within one scope: a
// distractor with a colliding-topic doc under a DIFFERENT tenant is never
// retrieved, so it cannot inflate (or deflate) the evaluated tenant's metrics.
func TestEvalRetrievalIsolation(t *testing.T) {
	ctx := context.Background()
	emb := semantic.NewHashEmbedder(0)

	acme, err := semantic.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope acme: %v", err)
	}
	globex, err := semantic.NewScope("globex", "bob")
	if err != nil {
		t.Fatalf("NewScope globex: %v", err)
	}

	store := semantic.NewInMemoryStore(emb.Dimension())

	// acme's relevant doc.
	acmeVec, _ := emb.Embed(ctx, []string{"The capital of France is Paris."})
	if err := store.Upsert(ctx, acme, []semantic.Point{{
		ID: "acme-france", TenantID: "acme", UserID: "alice",
		Kind: semantic.KindDocument, Content: "The capital of France is Paris.", Vector: acmeVec[0],
	}}); err != nil {
		t.Fatalf("upsert acme: %v", err)
	}
	// globex's doc on the SAME topic — must stay invisible to acme's eval.
	globexVec, _ := emb.Embed(ctx, []string{"The capital of France is Paris."})
	if err := store.Upsert(ctx, globex, []semantic.Point{{
		ID: "globex-france", TenantID: "globex", UserID: "bob",
		Kind: semantic.KindDocument, Content: "The capital of France is Paris.", Vector: globexVec[0],
	}}); err != nil {
		t.Fatalf("upsert globex: %v", err)
	}

	cases := []RetrievalCase{{Name: "france", Query: "What is the capital of France?", RelevantIDs: []string{"acme-france"}}}
	report, err := EvalRetrieval(ctx, store, acme, emb, cases, 5)
	if err != nil {
		t.Fatalf("EvalRetrieval: %v", err)
	}

	for _, id := range report.Cases[0].RetrievedIDs {
		if id == "globex-france" {
			t.Fatalf("tenant leak: acme eval retrieved globex's point %q", id)
		}
	}
	if !almostEqual(report.Cases[0].Precision, 1.0/5.0) {
		t.Errorf("precision@5 = %v, want 1/5 (only acme-france retrieved)", report.Cases[0].Precision)
	}
	if !almostEqual(report.Cases[0].Recall, 1.0) {
		t.Errorf("recall = %v, want 1.0", report.Cases[0].Recall)
	}
}

func TestEvalRetrievalRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	emb := semantic.NewHashEmbedder(0)
	sc, _ := semantic.NewScope("acme", "alice")
	store := semantic.NewInMemoryStore(emb.Dimension())

	if _, err := EvalRetrieval(ctx, store, sc, emb, nil, 0); err == nil {
		t.Error("expected error for k=0")
	}
	if _, err := EvalRetrieval(ctx, nil, sc, emb, nil, 3); err == nil {
		t.Error("expected error for nil store")
	}
	if _, err := EvalRetrieval(ctx, store, sc, nil, nil, 3); err == nil {
		t.Error("expected error for nil embedder")
	}
}
