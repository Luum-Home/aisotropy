// Command rag-quality is a runnable retrieval-QUALITY eval for aisotropy's
// semantic tier: it embeds a licensed public multilingual gold set with the
// configured embedder, upserts it into the REAL in-process semantic.Store
// (tenant/user-scoped exactly like production), runs search, and reports
// measured recall@{1,5,10}, MRR@10, and nDCG@10 — English, Spanish, and both
// cross-lingual directions.
//
// It is the "measured, not asserted" answer to the open question ADR-0026 left:
// the existing eval foundation (internal/eval) shipped the metrics and the
// harness seam but NO gold dataset and NO numbers. This binary supplies a real
// dataset and produces the numbers, moving retrieval quality from "unmeasured"
// to "measured."
//
// Honesty contract (printed in every report): if the real e5 model is not
// wired (no AISOTROPY_EMBED_BIN + model dir), the harness runs on the
// deterministic HashEmbedder and LABELS its output as HARNESS-VALIDATION — it
// proves the pipeline end-to-end, but the numbers are lexical-overlap artifacts,
// NOT the semantic quality of the multilingual e5 embedder. The cross-lingual
// slices make the difference visible: a hash embedder scores ~0 across
// languages (no shared lexicon), while a real multilingual embedder does not.
//
// Usage:
//
//	# Real multilingual-e5-small (sovereign local subprocess):
//	AISOTROPY_EMBED_BIN=crates/embed/target/release/embed \
//	AISOTROPY_EMBED_MODEL_DIR=/path/to/multilingual-e5-small \
//	AISOTROPY_EMBED_DIM=384 AISOTROPY_EMBED_MODE=subprocess \
//	go run ./bench/rag-quality
//
//	# Deterministic harness validation (no model, no network):
//	go run ./bench/rag-quality
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Luum-Home/aisotropy/internal/eval"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// evalTenant / evalUser are the fixed identity every eval read/write is scoped
// to. The harness measures the retriever the way production serves it — through
// a real semantic.Scope — so the constitutional isolation gate holds during
// evaluation, not just in the request path.
const (
	evalTenant = "eval"
	evalUser   = "rag-quality"
)

// maxCutoff is the deepest rank the harness scores at (recall@10, MRR@10,
// nDCG@10). Shallower recall cutoffs (recall@1, recall@5) are derived from the
// same top-10 ranked list per case, so the retriever is queried once per slice.
const maxCutoff = 10

func main() {
	var (
		dataPath = flag.String("data", defaultDataPath(), "path to the retrieval gold set JSON")
		outPath  = flag.String("out", defaultOutPath(), "path to write the results report")
		judgeN   = flag.Int("judge-samples", 20, "number of queries to sample for the optional LLM-judge pass")
		quiet    = flag.Bool("quiet", false, "suppress embedder slog noise")
	)
	flag.Parse()

	if *quiet {
		slog.SetLogLoggerLevel(slog.LevelError)
	}

	ctx := context.Background()
	if err := run(ctx, *dataPath, *outPath, *judgeN); err != nil {
		fmt.Fprintf(os.Stderr, "rag-quality: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dataPath, outPath string, judgeN int) error {
	gs, err := loadGoldSet(dataPath)
	if err != nil {
		return err
	}

	emb := semantic.EmbedderFromEnv()
	backend, roleAware, realQuality := classifyEmbedder(ctx, emb)
	dim := emb.Dimension()

	rep := &report{
		gold:        gs,
		dataPath:    dataPath,
		backend:     backend,
		roleAware:   roleAware,
		realQuality: realQuality,
		dim:         dim,
		startedAt:   time.Now(),
	}

	// Build one store per corpus language, embedding each corpus ONCE with the
	// e5 "passage: " role (ADR-0054) so a "query: "-prefixed search matches it.
	stores := map[string]semantic.Store{}
	for lang := range gs.Corpora {
		st, err := buildStore(ctx, emb, dim, gs.Corpora[lang])
		if err != nil {
			return fmt.Errorf("build %q corpus store: %w", lang, err)
		}
		stores[lang] = st
		defer func() { _ = st.Close() }()
	}

	// Evaluate every slice against the store for its corpus language.
	for _, s := range gs.Slices {
		store := stores[s.CorpusLang]
		sc, err := semantic.NewScope(evalTenant, evalUser)
		if err != nil {
			return fmt.Errorf("eval scope: %w", err)
		}
		r, err := eval.EvalRetrieval(ctx, store, sc, emb, s.evalCases(), maxCutoff)
		if err != nil {
			return fmt.Errorf("slice %q: %w", s.Name, err)
		}
		rep.slices = append(rep.slices, sliceReport{slice: s, report: r})
	}

	rep.finishedAt = time.Now()

	// Optional, gated answer/context-quality pass — skipped cleanly with no key.
	rep.judge = runJudgePass(ctx, gs, stores, emb, dim, judgeN)

	rendered := rep.render()
	fmt.Print(rendered)
	if err := os.WriteFile(outPath, []byte(rendered), 0o644); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	fmt.Fprintf(os.Stderr, "\nwrote %s\n", outPath)
	return nil
}

// buildStore embeds a corpus with the e5 "passage: " role and upserts it into a
// fresh in-process store under the eval scope. Embedding is batched into one
// call so a warm daemon embedder pays a single round trip for the whole corpus.
func buildStore(ctx context.Context, emb semantic.Embedder, dim int, docs []doc) (semantic.Store, error) {
	sc, err := semantic.NewScope(evalTenant, evalUser)
	if err != nil {
		return nil, err
	}
	texts := make([]string, len(docs))
	for i, d := range docs {
		texts[i] = d.Text
	}
	vecs, err := semantic.EmbedPassage(ctx, emb, texts)
	if err != nil {
		return nil, fmt.Errorf("embed passages: %w", err)
	}
	if len(vecs) != len(docs) {
		return nil, fmt.Errorf("embedder returned %d vectors for %d passages", len(vecs), len(docs))
	}
	store := semantic.NewInMemoryStore(dim)
	points := make([]semantic.Point, len(docs))
	for i, d := range docs {
		points[i] = semantic.Point{
			ID:       d.ID,
			TenantID: sc.Tenant(),
			UserID:   sc.User(),
			Kind:     semantic.KindDocument,
			Content:  d.Text,
			Vector:   vecs[i],
		}
	}
	if err := store.Upsert(ctx, sc, points); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("upsert corpus: %w", err)
	}
	return store, nil
}

// classifyEmbedder names the configured backend and probes — at runtime, by
// comparing a query-role vector against a passage-role vector for the same text
// — whether e5 query:/passage: prefixing is actually active. The probe is the
// honest signal: it does not trust the model dir path, it observes whether the
// two roles produce different embeddings (they only do when the embedder applies
// the e5 asymmetry). realQuality is false only for the deterministic
// HashEmbedder, whose numbers are harness-validation, not semantic quality.
func classifyEmbedder(ctx context.Context, emb semantic.Embedder) (backend string, roleAware, realQuality bool) {
	switch emb.(type) {
	case semantic.HashEmbedder:
		backend, realQuality = "HashEmbedder (deterministic feature-hashing — NOT semantic)", false
	case *semantic.LocalEmbedder:
		backend, realQuality = "LocalEmbedder (sovereign local e5 subprocess, ADR-0023)", true
	case *semantic.DaemonEmbedder:
		backend, realQuality = "DaemonEmbedder (sovereign local e5 warm daemon, ADR-0023)", true
	case *semantic.APIEmbedder:
		backend, realQuality = "APIEmbedder (external OpenAI-compatible /embeddings)", true
	default:
		backend, realQuality = fmt.Sprintf("%T", emb), true
	}

	q, qErr := semantic.EmbedQuery(ctx, emb, []string{"role prefix probe"})
	p, pErr := semantic.EmbedPassage(ctx, emb, []string{"role prefix probe"})
	if qErr == nil && pErr == nil && len(q) == 1 && len(p) == 1 {
		roleAware = !vecEqual(q[0], p[0])
	}
	return backend, roleAware, realQuality
}

// vecEqual reports exact equality of two vectors (used only to detect whether
// the query/passage roles diverged, so an exact compare is what we want).
func vecEqual(a, b []float32) bool {
	if len(a) != len(b) {
		return true
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// defaultDataPath resolves the vendored gold set relative to THIS source file,
// so `go run ./bench/rag-quality` works from any working directory.
func defaultDataPath() string {
	return filepath.Join(sourceDir(), "testdata", "xquad_slice.json")
}

// defaultOutPath resolves the results file next to this source file.
func defaultOutPath() string {
	return filepath.Join(sourceDir(), "results.txt")
}

func sourceDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Dir(file)
}
