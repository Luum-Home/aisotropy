package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/eval"
)

func almostEqual(a, b float64) bool {
	const eps = 1e-9
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < eps
}

func TestSliceReportMetrics(t *testing.T) {
	sr := sliceReport{
		slice: goldSlice{Name: "en_monolingual"},
		report: eval.RetrievalReport{
			MeanRecall: 0.6,
			MeanMRR:    0.5,
			MeanNDCG:   0.55,
			Cases: []eval.RetrievalResult{
				{
					Case:         eval.RetrievalCase{Name: "a", RelevantIDs: []string{"x"}},
					RetrievedIDs: []string{"x", "y", "z"}, // relevant is rank 1: recall@1 = recall@5 = 1
				},
				{
					Case:         eval.RetrievalCase{Name: "b", RelevantIDs: []string{"w"}},
					RetrievedIDs: []string{"y", "z", "w"}, // relevant is rank 3: recall@1 = 0, recall@5 = 1
				},
			},
		},
	}

	r1, r5, r10, mrr10, ndcg10, queries := sr.metrics()
	if !almostEqual(r1, 0.5) {
		t.Errorf("r1 = %v, want 0.5", r1)
	}
	if !almostEqual(r5, 1.0) {
		t.Errorf("r5 = %v, want 1.0", r5)
	}
	if !almostEqual(r10, 0.6) {
		t.Errorf("r10 = %v, want 0.6 (MeanRecall passthrough)", r10)
	}
	if !almostEqual(mrr10, 0.5) {
		t.Errorf("mrr10 = %v, want 0.5", mrr10)
	}
	if !almostEqual(ndcg10, 0.55) {
		t.Errorf("ndcg10 = %v, want 0.55", ndcg10)
	}
	if queries != 2 {
		t.Errorf("queries = %d, want 2", queries)
	}
}

func TestSliceReportMetrics_NoCases(t *testing.T) {
	sr := sliceReport{slice: goldSlice{Name: "empty"}, report: eval.RetrievalReport{}}
	r1, r5, _, _, _, queries := sr.metrics()
	if r1 != 0 || r5 != 0 || queries != 0 {
		t.Errorf("expected all zeros for an empty case set, got r1=%v r5=%v queries=%d", r1, r5, queries)
	}
}

func baseReport() *report {
	return &report{
		gold: &goldSet{
			Name: "mini-test-gold", Source: "synthetic-test-fixture", SourceURL: "https://example.test",
			License: "CC0", LicenseURL: "https://example.test/license",
			CorpusSizes: map[string]int{"en": 2}, AlignedPairs: 2,
		},
		dataPath:   "testdata/mini_gold.json",
		dim:        8,
		startedAt:  time.Now(),
		finishedAt: time.Now(),
		judge:      &judgeReport{skippedReason: "no LLM provider key set"},
	}
}

func TestReportRender_HarnessValidationBanner(t *testing.T) {
	rp := baseReport()
	rp.backend = "HashEmbedder (deterministic feature-hashing — NOT semantic)"
	rp.roleAware = false
	rp.realQuality = false
	rp.slices = []sliceReport{
		{slice: goldSlice{Name: "en_monolingual", QueryLang: "en", CorpusLang: "en", Description: "d"},
			report: eval.RetrievalReport{MeanRecall: 1, MeanMRR: 1, MeanNDCG: 1}},
	}

	got := rp.render()
	if !strings.Contains(got, "HARNESS VALIDATION") {
		t.Error("expected the harness-validation banner for a non-real embedder")
	}
	if strings.Contains(got, "REAL embedder") {
		t.Error("did not expect the real-embedder banner")
	}
	if !strings.Contains(got, "e5 role prefix : no") {
		t.Error("expected the role-prefix-not-engaged line")
	}
	if !strings.Contains(got, "en_monolingual") {
		t.Error("expected the slice row in the results table")
	}
	if !strings.Contains(got, "MACRO-AVG") {
		t.Error("expected the macro-average row when slices are present")
	}
	if !strings.Contains(got, "SKIPPED: no LLM provider key set") {
		t.Error("expected the judge section to show the skip reason")
	}
	if !strings.Contains(got, rp.gold.Name) || !strings.Contains(got, rp.gold.License) {
		t.Error("expected dataset provenance fields in the report")
	}
}

func TestReportRender_RealEmbedderBanner(t *testing.T) {
	rp := baseReport()
	rp.backend = "*main.roleFakeEmbedder"
	rp.roleAware = true
	rp.realQuality = true
	rp.slices = nil // exercises the "no slices" path: no MACRO-AVG row

	got := rp.render()
	if !strings.Contains(got, "REAL embedder") {
		t.Error("expected the real-embedder banner")
	}
	if strings.Contains(got, "HARNESS VALIDATION") {
		t.Error("did not expect the harness-validation banner")
	}
	if !strings.Contains(got, "e5 role prefix : yes") {
		t.Error("expected the role-prefix-engaged line")
	}
	if strings.Contains(got, "MACRO-AVG") {
		t.Error("did not expect a macro-average row with zero slices")
	}
}

func TestReportRender_JudgeRan(t *testing.T) {
	rp := baseReport()
	rp.judge = &judgeReport{ran: true, criterion: "retrieved_context_relevance", provider: "stub-test", samples: 2, mean: 1, pass: 2, threshold: 0.7}
	got := rp.render()
	if !strings.Contains(got, "criterion : retrieved_context_relevance") {
		t.Error("expected the ran judge section to render")
	}
}

func TestReportRender_NilJudge(t *testing.T) {
	rp := baseReport()
	rp.judge = nil
	got := rp.render()
	if !strings.Contains(got, "(not run)") {
		t.Error("expected a nil judge to render as (not run)")
	}
}
