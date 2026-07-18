package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/eval"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// --- pickJudgeSlice --------------------------------------------------------

func TestPickJudgeSlice_PrefersMonolingual(t *testing.T) {
	gs := &goldSet{Slices: []goldSlice{
		{Name: "cross_en_to_es", QueryLang: "en", CorpusLang: "es"},
		{Name: "es_monolingual", QueryLang: "es", CorpusLang: "es"},
	}}
	got := pickJudgeSlice(gs)
	if got == nil || got.Name != "es_monolingual" {
		t.Fatalf("pickJudgeSlice = %+v, want es_monolingual", got)
	}
}

func TestPickJudgeSlice_FallsBackToFirst(t *testing.T) {
	gs := &goldSet{Slices: []goldSlice{
		{Name: "cross_en_to_es", QueryLang: "en", CorpusLang: "es"},
		{Name: "cross_es_to_en", QueryLang: "es", CorpusLang: "en"},
	}}
	got := pickJudgeSlice(gs)
	if got == nil || got.Name != "cross_en_to_es" {
		t.Fatalf("pickJudgeSlice = %+v, want fallback to first slice", got)
	}
}

func TestPickJudgeSlice_NilWhenNoSlices(t *testing.T) {
	if got := pickJudgeSlice(&goldSet{}); got != nil {
		t.Fatalf("pickJudgeSlice = %+v, want nil", got)
	}
}

// --- corpusIndex -------------------------------------------------------

func TestCorpusIndex(t *testing.T) {
	idx := corpusIndex([]doc{{ID: "a", Text: "alpha"}, {ID: "b", Text: "beta"}})
	if idx["a"] != "alpha" || idx["b"] != "beta" {
		t.Fatalf("corpusIndex = %v, unexpected content", idx)
	}
	if len(corpusIndex(nil)) != 0 {
		t.Fatal("corpusIndex(nil) should be empty")
	}
}

// --- topPassage --------------------------------------------------------

func evalScopeT(t *testing.T) semantic.Scope {
	t.Helper()
	sc, err := semantic.NewScope(evalTenant, evalUser)
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	return sc
}

func TestTopPassage_FoundViaTextByID(t *testing.T) {
	ctx := context.Background()
	emb := semantic.NewHashEmbedder(8)
	sc := evalScopeT(t)
	store := semantic.NewInMemoryStore(emb.Dimension())

	vecs, _ := emb.Embed(ctx, []string{"the quick brown fox"})
	if err := store.Upsert(ctx, sc, []semantic.Point{{
		ID: "doc-1", TenantID: sc.Tenant(), UserID: sc.User(),
		Kind: semantic.KindDocument, Content: "stored content, not the index text", Vector: vecs[0],
	}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	textByID := map[string]string{"doc-1": "index text wins over stored content"}
	got, err := topPassage(ctx, store, sc, emb, "the quick brown fox", textByID)
	if err != nil {
		t.Fatalf("topPassage: %v", err)
	}
	if got != "index text wins over stored content" {
		t.Errorf("topPassage = %q, want the textByID projection", got)
	}
}

func TestTopPassage_FallsBackToHitContent(t *testing.T) {
	ctx := context.Background()
	emb := semantic.NewHashEmbedder(8)
	sc := evalScopeT(t)
	store := semantic.NewInMemoryStore(emb.Dimension())

	vecs, _ := emb.Embed(ctx, []string{"unique query text"})
	if err := store.Upsert(ctx, sc, []semantic.Point{{
		ID: "doc-unindexed", TenantID: sc.Tenant(), UserID: sc.User(),
		Kind: semantic.KindDocument, Content: "hit content used as fallback", Vector: vecs[0],
	}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// textByID does NOT contain "doc-unindexed", forcing the hits[0].Content
	// fallback branch.
	got, err := topPassage(ctx, store, sc, emb, "unique query text", map[string]string{})
	if err != nil {
		t.Fatalf("topPassage: %v", err)
	}
	if got != "hit content used as fallback" {
		t.Errorf("topPassage = %q, want fallback to hit content", got)
	}
}

func TestTopPassage_NoHitsReturnsEmpty(t *testing.T) {
	ctx := context.Background()
	emb := semantic.NewHashEmbedder(8)
	sc := evalScopeT(t)
	store := semantic.NewInMemoryStore(emb.Dimension())

	got, err := topPassage(ctx, store, sc, emb, "nothing indexed", map[string]string{})
	if err != nil {
		t.Fatalf("topPassage: %v", err)
	}
	if got != "" {
		t.Errorf("topPassage = %q, want empty string for an empty store", got)
	}
}

func TestTopPassage_EmbedError(t *testing.T) {
	ctx := context.Background()
	sc := evalScopeT(t)
	store := semantic.NewInMemoryStore(8)
	wantErr := errors.New("embed boom")
	emb := &fakeEmbedder{dim: 8, embed: func(context.Context, []string) ([][]float32, error) {
		return nil, wantErr
	}}

	_, err := topPassage(ctx, store, sc, emb, "q", map[string]string{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("topPassage error = %v, want wrapping %v", err, wantErr)
	}
}

func TestTopPassage_SearchError(t *testing.T) {
	ctx := context.Background()
	emb := semantic.NewHashEmbedder(8)
	sc := evalScopeT(t)
	wantErr := errors.New("search boom")
	store := &searchErrStore{InMemoryStore: semantic.NewInMemoryStore(8), err: wantErr}

	_, err := topPassage(ctx, store, sc, emb, "q", map[string]string{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("topPassage error = %v, want wrapping %v", err, wantErr)
	}
}

// --- judgeReport.render --------------------------------------------------

func TestJudgeReportRender_Nil(t *testing.T) {
	var jr *judgeReport
	if got := jr.render(); got != "  (not run)\n" {
		t.Errorf("render() = %q", got)
	}
}

func TestJudgeReportRender_Error(t *testing.T) {
	jr := &judgeReport{errText: "boom"}
	if got := jr.render(); !strings.Contains(got, "ERROR: boom") {
		t.Errorf("render() = %q, want ERROR: boom", got)
	}
}

func TestJudgeReportRender_Skipped(t *testing.T) {
	jr := &judgeReport{skippedReason: "no key"}
	if got := jr.render(); !strings.Contains(got, "SKIPPED: no key") {
		t.Errorf("render() = %q, want SKIPPED: no key", got)
	}
}

func TestJudgeReportRender_Ran(t *testing.T) {
	jr := &judgeReport{
		ran: true, criterion: "retrieved_context_relevance", provider: "stub-test",
		samples: 3, mean: 0.9, pass: 2, threshold: 0.7,
	}
	got := jr.render()
	for _, want := range []string{"retrieved_context_relevance", "stub-test", "samples   : 3", "0.900", "2/3"} {
		if !strings.Contains(got, want) {
			t.Errorf("render() = %q, missing %q", got, want)
		}
	}
}

// --- runJudgePass --------------------------------------------------------

func miniGold(t *testing.T) *goldSet {
	t.Helper()
	gs, err := loadGoldSet(miniGoldPath)
	if err != nil {
		t.Fatalf("loadGoldSet: %v", err)
	}
	return gs
}

// buildJudgeStores embeds and upserts every corpus of gs (via the real,
// deterministic HashEmbedder) into fresh in-memory stores, exactly like
// buildStore/runHarness — the setup runJudgePass needs to retrieve real
// passages for its sample.
func buildJudgeStores(t *testing.T, ctx context.Context, gs *goldSet, emb semantic.Embedder) map[string]semantic.Store {
	t.Helper()
	stores := map[string]semantic.Store{}
	for lang, docs := range gs.Corpora {
		st, err := buildStore(ctx, emb, emb.Dimension(), docs)
		if err != nil {
			t.Fatalf("buildStore(%q): %v", lang, err)
		}
		stores[lang] = st
	}
	return stores
}

func TestRunJudgePass_SkippedNoKey(t *testing.T) {
	ctx := context.Background()
	gs := miniGold(t)
	emb := semantic.NewHashEmbedder(8)
	stores := buildJudgeStores(t, ctx, gs, emb)

	jr := runJudgePass(ctx, gs, stores, emb, 5, eval.JudgeClient{Provider: "stub", Real: false})
	if jr.ran {
		t.Fatal("expected the judge pass to be skipped, not run")
	}
	if jr.skippedReason == "" {
		t.Error("expected a non-empty skippedReason")
	}
}

func TestRunJudgePass_NoMonolingualSlice(t *testing.T) {
	ctx := context.Background()
	gs := &goldSet{} // no slices at all -> pickJudgeSlice returns nil
	jr := runJudgePass(ctx, gs, map[string]semantic.Store{}, semantic.NewHashEmbedder(8), 5,
		eval.JudgeClient{Client: llm.NewStubClient(), Real: true})
	if jr.ran {
		t.Fatal("expected skip, not run")
	}
	if jr.skippedReason != "no monolingual slice available to judge" {
		t.Errorf("skippedReason = %q", jr.skippedReason)
	}
}

func TestRunJudgePass_NoPassagesRetrieved(t *testing.T) {
	ctx := context.Background()
	gs := miniGold(t)
	emb := semantic.NewHashEmbedder(8)
	// Empty stores for every corpus language: every topPassage call finds zero
	// hits, so every case is skipped and the judge pass reports "no passages".
	stores := map[string]semantic.Store{
		"en": semantic.NewInMemoryStore(emb.Dimension()),
		"es": semantic.NewInMemoryStore(emb.Dimension()),
	}

	jr := runJudgePass(ctx, gs, stores, emb, 5, eval.JudgeClient{Client: llm.NewStubClient(), Real: true})
	if jr.ran {
		t.Fatal("expected skip, not run")
	}
	if jr.skippedReason != "no passages retrieved to judge" {
		t.Errorf("skippedReason = %q", jr.skippedReason)
	}
}

func TestRunJudgePass_TopPassageError(t *testing.T) {
	ctx := context.Background()
	gs := miniGold(t)
	emb := semantic.NewHashEmbedder(8)
	stores := map[string]semantic.Store{
		"en": &searchErrStore{InMemoryStore: semantic.NewInMemoryStore(emb.Dimension()), err: errors.New("search boom")},
		"es": semantic.NewInMemoryStore(emb.Dimension()),
	}

	jr := runJudgePass(ctx, gs, stores, emb, 5, eval.JudgeClient{Client: llm.NewStubClient(), Real: true})
	if jr.errText == "" {
		t.Fatal("expected errText to be set")
	}
	if !strings.Contains(jr.errText, "retrieve for") {
		t.Errorf("errText = %q", jr.errText)
	}
}

func TestRunJudgePass_BuildJudgeError(t *testing.T) {
	ctx := context.Background()
	gs := miniGold(t)
	emb := semantic.NewHashEmbedder(8)
	stores := buildJudgeStores(t, ctx, gs, emb)

	// Real == true but Client is nil: NewLLMJudge rejects a nil client.
	jr := runJudgePass(ctx, gs, stores, emb, 5, eval.JudgeClient{Client: nil, Real: true})
	if jr.errText == "" || !strings.Contains(jr.errText, "build judge") {
		t.Errorf("errText = %q, want a build judge error", jr.errText)
	}
}

func TestRunJudgePass_RunSuiteError(t *testing.T) {
	ctx := context.Background()
	gs := miniGold(t)
	emb := semantic.NewHashEmbedder(8)
	stores := buildJudgeStores(t, ctx, gs, emb)

	jr := runJudgePass(ctx, gs, stores, emb, 5, eval.JudgeClient{Client: errClient{}, Real: true})
	if jr.errText == "" || !strings.Contains(jr.errText, "run suite") {
		t.Errorf("errText = %q, want a run suite error", jr.errText)
	}
}

func TestRunJudgePass_RanWithStub(t *testing.T) {
	ctx := context.Background()
	gs := miniGold(t)
	emb := semantic.NewHashEmbedder(8)
	stores := buildJudgeStores(t, ctx, gs, emb)

	jr := runJudgePass(ctx, gs, stores, emb, 5, eval.JudgeClient{Client: llm.NewStubClient(), Provider: "stub-test", Real: true})
	if jr.errText != "" {
		t.Fatalf("unexpected errText: %s", jr.errText)
	}
	if !jr.ran {
		t.Fatal("expected the judge pass to have run")
	}
	if jr.samples == 0 {
		t.Error("expected at least one sample scored")
	}
	if jr.mean < 0 || jr.mean > 1 {
		t.Errorf("mean = %v, want within [0,1]", jr.mean)
	}
}

func TestRunJudgePass_SampleCap(t *testing.T) {
	ctx := context.Background()
	gs := miniGold(t)
	emb := semantic.NewHashEmbedder(8)
	stores := buildJudgeStores(t, ctx, gs, emb)

	// en_monolingual has 2 cases; capping n=1 must bound the sample count.
	jr := runJudgePass(ctx, gs, stores, emb, 1, eval.JudgeClient{Client: llm.NewStubClient(), Real: true})
	if !jr.ran {
		t.Fatalf("expected the judge pass to run, errText=%q skipped=%q", jr.errText, jr.skippedReason)
	}
	if jr.samples != 1 {
		t.Errorf("samples = %d, want 1 (the -judge-samples cap)", jr.samples)
	}
}
