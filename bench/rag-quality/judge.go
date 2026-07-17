package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/Luum-Home/aisotropy/internal/eval"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// judgeReport is the outcome of the OPTIONAL, key-gated LLM-judge pass. The
// pass is a small extra layer on top of the objective IR metrics: it asks a
// model whether the TOP-RETRIEVED passage is actually relevant to the query —
// an "LLM-as-a-judge" signal that catches cases the binary qrels cannot (a
// passage that is topically on-point but not the single labeled gold one).
type judgeReport struct {
	ran           bool
	skippedReason string
	criterion     string
	provider      string
	samples       int
	mean          float64
	pass          int
	threshold     float64
	errText       string
}

// retrievedContextRelevance is the criterion the optional judge scores. It is
// deliberately a RETRIEVAL judge (grade the fetched passage), not a generation
// judge — this harness measures the retriever, so no answer is generated.
var retrievedContextRelevance = eval.Criterion{
	Name:        "retrieved_context_relevance",
	Description: "Given a user query and the single passage the retriever ranked first, does that passage contain information that answers or is directly relevant to the query? Score 1.0 when the passage clearly answers or is on-topic for the query, 0.0 when it is unrelated.",
	Scale:       eval.UnitScale,
}

var retrievedContextRelevanceRubric = eval.Rubric{
	{Score: 1.0, Illustration: "the passage directly answers the query"},
	{Score: 0.5, Illustration: "same topic but does not actually answer the query"},
	{Score: 0.0, Illustration: "unrelated to the query"},
}

// judgeThreshold is the pass bar for the optional judge aggregate.
const judgeThreshold = 0.7

// runJudgePass runs the optional judge, gated the PROVIDER-AGNOSTIC way — via
// eval.JudgeClientFromEnv, which mirrors llm.RegistryFromEnv precedence
// (OPENROUTER_API_KEY > OPENAI_API_KEY > ANTHROPIC_API_KEY > stub, ADR-0009),
// NOT a hardcoded ANTHROPIC_API_KEY gate. With no real provider key it is
// skipped cleanly (never an error, never the stub masquerading as a real
// judge). With any provider key it drives the SAME kind of llm.Client the
// product serves with, retrieves the top passage for a sample of queries from
// the en_monolingual slice, and scores retrieved-context relevance via the
// shared internal/eval LLM-judge + RunSuite machinery.
func runJudgePass(ctx context.Context, gs *goldSet, stores map[string]semantic.Store, emb semantic.Embedder, dim, n int) *judgeReport {
	jr := &judgeReport{criterion: retrievedContextRelevance.Name, threshold: judgeThreshold}

	jc := eval.JudgeClientFromEnv()
	jr.provider = jc.Provider
	if !jc.Real {
		jr.skippedReason = "no LLM provider key set (OPENROUTER_API_KEY / OPENAI_API_KEY / ANTHROPIC_API_KEY) — LLM-judge is optional; objective IR metrics above are unaffected"
		return jr
	}

	target := pickJudgeSlice(gs)
	if target == nil {
		jr.skippedReason = "no monolingual slice available to judge"
		return jr
	}
	store := stores[target.CorpusLang]
	textByID := corpusIndex(gs.Corpora[target.CorpusLang])

	sc, err := semantic.NewScope(evalTenant, evalUser)
	if err != nil {
		jr.errText = fmt.Sprintf("eval scope: %v", err)
		return jr
	}

	cases := make([]eval.EvalCase, 0, n)
	for i, c := range target.Cases {
		if i >= n {
			break
		}
		passage, err := topPassage(ctx, store, sc, emb, c.Query, textByID)
		if err != nil {
			jr.errText = fmt.Sprintf("retrieve for %q: %v", c.Name, err)
			return jr
		}
		if passage == "" {
			continue
		}
		cases = append(cases, eval.EvalCase{
			Name:   c.Name,
			Sample: eval.EvalSample{Query: c.Query, Response: passage},
		})
	}
	if len(cases) == 0 {
		jr.skippedReason = "no passages retrieved to judge"
		return jr
	}

	judge, err := eval.NewLLMJudge(jc.Client, retrievedContextRelevance, retrievedContextRelevanceRubric, 0)
	if err != nil {
		jr.errText = fmt.Sprintf("build judge: %v", err)
		return jr
	}

	res, err := eval.RunSuite(ctx, cases, []eval.Judge{judge}, judgeThreshold)
	if err != nil {
		jr.errText = fmt.Sprintf("run suite: %v", err)
		return jr
	}

	agg := res.ByCriterion[retrievedContextRelevance.Name]
	jr.ran = true
	jr.samples = agg.Count
	jr.mean = agg.Mean
	jr.pass = agg.Pass
	return jr
}

// pickJudgeSlice returns the first monolingual slice (query lang == corpus
// lang), the natural setting to judge single-language retrieved relevance.
func pickJudgeSlice(gs *goldSet) *goldSlice {
	for i := range gs.Slices {
		if gs.Slices[i].QueryLang == gs.Slices[i].CorpusLang {
			return &gs.Slices[i]
		}
	}
	if len(gs.Slices) > 0 {
		return &gs.Slices[0]
	}
	return nil
}

// corpusIndex maps doc id -> text for passage lookup after search.
func corpusIndex(docs []doc) map[string]string {
	m := make(map[string]string, len(docs))
	for _, d := range docs {
		m[d.ID] = d.Text
	}
	return m
}

// topPassage embeds the query with the e5 "query: " role, searches the store
// for the single best point, and returns its text.
func topPassage(ctx context.Context, store semantic.Store, sc semantic.Scope, emb semantic.Embedder, query string, textByID map[string]string) (string, error) {
	vecs, err := semantic.EmbedQuery(ctx, emb, []string{query})
	if err != nil {
		return "", err
	}
	hits, err := store.Search(ctx, sc, vecs[0], 1)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return "", nil
	}
	if t, ok := textByID[hits[0].ID]; ok {
		return t, nil
	}
	return hits[0].Content, nil
}

// render formats the judge section for the report.
func (jr *judgeReport) render() string {
	if jr == nil {
		return "  (not run)\n"
	}
	var b strings.Builder
	if jr.errText != "" {
		fmt.Fprintf(&b, "  ERROR: %s\n", jr.errText)
		return b.String()
	}
	if !jr.ran {
		fmt.Fprintf(&b, "  SKIPPED: %s\n", jr.skippedReason)
		return b.String()
	}
	fmt.Fprintf(&b, "  criterion : %s (LLM-as-a-judge, via internal/eval)\n", jr.criterion)
	fmt.Fprintf(&b, "  provider  : %s (provider-agnostic, eval.JudgeClientFromEnv)\n", jr.provider)
	fmt.Fprintf(&b, "  samples   : %d (top-1 retrieved passage per query)\n", jr.samples)
	fmt.Fprintf(&b, "  mean      : %.3f  (pass@%.2f: %d/%d)\n", jr.mean, jr.threshold, jr.pass, jr.samples)
	return b.String()
}
