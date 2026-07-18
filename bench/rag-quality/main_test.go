package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/eval"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// --- small pure helpers ----------------------------------------------------

func TestDefaultPaths(t *testing.T) {
	if dp := defaultDataPath(); !strings.HasSuffix(dp, filepath.Join("testdata", "xquad_slice.json")) {
		t.Errorf("defaultDataPath() = %q", dp)
	}
	if op := defaultOutPath(); !strings.HasSuffix(op, "results.txt") {
		t.Errorf("defaultOutPath() = %q", op)
	}
	if sourceDir() == "." {
		t.Error("sourceDir() unexpectedly fell back to \".\"")
	}
}

func TestVecEqual(t *testing.T) {
	if !vecEqual([]float32{1, 2, 3}, []float32{1, 2, 3}) {
		t.Error("identical vectors should compare equal")
	}
	if vecEqual([]float32{1, 2, 3}, []float32{1, 2, 4}) {
		t.Error("differing vectors should compare unequal")
	}
	// A length mismatch is documented (and preserved) as "equal" — vecEqual
	// exists only to detect divergence between two same-length role probes.
	if !vecEqual([]float32{1, 2}, []float32{1, 2, 3}) {
		t.Error("length-mismatched vectors are treated as equal by this helper")
	}
}

// --- classifyEmbedder --------------------------------------------------

func TestClassifyEmbedder_Hash(t *testing.T) {
	backend, roleAware, realQuality := classifyEmbedder(context.Background(), semantic.NewHashEmbedder(8))
	if !strings.Contains(backend, "HashEmbedder") {
		t.Errorf("backend = %q, want it to mention HashEmbedder", backend)
	}
	if roleAware {
		t.Error("HashEmbedder is not role-aware")
	}
	if realQuality {
		t.Error("HashEmbedder numbers are not real semantic quality")
	}
}

func TestClassifyEmbedder_DefaultBranchNotRoleAware(t *testing.T) {
	emb := &fakeEmbedder{dim: 8, embed: constVector(8, 0.5)}
	backend, roleAware, realQuality := classifyEmbedder(context.Background(), emb)
	if !strings.Contains(backend, "fakeEmbedder") {
		t.Errorf("backend = %q, want the %%T fallback naming", backend)
	}
	if roleAware {
		t.Error("a plain (non-RoleEmbedder) fake must not be reported role-aware")
	}
	if !realQuality {
		t.Error("an unrecognized (non-Hash) embedder type defaults to realQuality=true")
	}
}

func TestClassifyEmbedder_RoleAware(t *testing.T) {
	emb := &roleFakeEmbedder{dim: 8}
	backend, roleAware, realQuality := classifyEmbedder(context.Background(), emb)
	if !strings.Contains(backend, "roleFakeEmbedder") {
		t.Errorf("backend = %q", backend)
	}
	if !roleAware {
		t.Error("expected roleAware=true: query/passage probes diverge")
	}
	if !realQuality {
		t.Error("expected realQuality=true for the default switch branch")
	}
}

// TestClassifyEmbedder_NamedTypes exercises the three named-type switch cases
// (LocalEmbedder, DaemonEmbedder, APIEmbedder) that a plain fake can never
// reach, since classifyEmbedder type-switches on the CONCRETE type. Each
// embedder is pointed at a deliberately unreachable backend (a binary path
// that does not exist / a closed loopback port) so the probe's Embed calls
// fail immediately and deterministically — no real subprocess ever starts,
// no real network call ever leaves the loopback interface, and no test can
// hang: exec of a missing binary and a refused loopback connection both fail
// synchronously. classifyEmbedder is written to swallow probe errors (roleAware
// stays false), so only the type-name/realQuality assignment is under test.
func TestClassifyEmbedder_NamedTypes(t *testing.T) {
	local, err := semantic.NewLocalEmbedder(semantic.LocalEmbedderConfig{
		BinPath: "/nonexistent/aisotropy-test-embed-bin", ModelDir: "/nonexistent/model-dir", Dimension: 4,
	})
	if err != nil {
		t.Fatalf("NewLocalEmbedder: %v", err)
	}
	daemon, err := semantic.NewDaemonEmbedder(semantic.DaemonEmbedderConfig{
		BinPath: "/nonexistent/aisotropy-test-embed-daemon-bin", ModelDir: "/nonexistent/model-dir", Dimension: 4,
	})
	if err != nil {
		t.Fatalf("NewDaemonEmbedder: %v", err)
	}
	api, err := semantic.NewAPIEmbedder(semantic.APIEmbedderConfig{
		BaseURL: "http://127.0.0.1:1", Dimension: 4, // closed loopback port: instant connection-refused
	})
	if err != nil {
		t.Fatalf("NewAPIEmbedder: %v", err)
	}

	cases := []struct {
		name string
		emb  semantic.Embedder
		want string
	}{
		{"local", local, "LocalEmbedder"},
		{"daemon", daemon, "DaemonEmbedder"},
		{"api", api, "APIEmbedder"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend, roleAware, realQuality := classifyEmbedder(context.Background(), tc.emb)
			if !strings.Contains(backend, tc.want) {
				t.Errorf("backend = %q, want it to mention %s", backend, tc.want)
			}
			if !realQuality {
				t.Errorf("%s must report realQuality=true", tc.want)
			}
			if roleAware {
				t.Errorf("%s with an unreachable backend must not report roleAware=true", tc.want)
			}
		})
	}
}

// --- buildStore ----------------------------------------------------------

func TestBuildStore_Success(t *testing.T) {
	ctx := context.Background()
	emb := semantic.NewHashEmbedder(8)
	docs := []doc{{ID: "d1", Text: "hello world"}, {ID: "d2", Text: "goodbye world"}}

	store, err := buildStore(ctx, emb, emb.Dimension(), docs)
	if err != nil {
		t.Fatalf("buildStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	sc, _ := semantic.NewScope(evalTenant, evalUser)
	vecs, _ := emb.Embed(ctx, []string{"hello world"})
	hits, err := store.Search(ctx, sc, vecs[0], 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(hits))
	}
}

func TestBuildStore_EmbedPassageError(t *testing.T) {
	wantErr := errors.New("embed boom")
	emb := &fakeEmbedder{dim: 8, embed: func(context.Context, []string) ([][]float32, error) { return nil, wantErr }}

	_, err := buildStore(context.Background(), emb, 8, []doc{{ID: "d1", Text: "x"}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("buildStore error = %v, want wrapping %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "embed passages") {
		t.Errorf("error = %q, want it to mention embed passages", err)
	}
}

func TestBuildStore_VectorCountMismatch(t *testing.T) {
	emb := &fakeEmbedder{dim: 8, embed: func(_ context.Context, texts []string) ([][]float32, error) {
		// Return one fewer vector than requested.
		out := make([][]float32, len(texts)-1)
		for i := range out {
			out[i] = make([]float32, 8)
		}
		return out, nil
	}}

	_, err := buildStore(context.Background(), emb, 8, []doc{{ID: "d1", Text: "x"}, {ID: "d2", Text: "y"}})
	if err == nil || !strings.Contains(err.Error(), "vectors for") {
		t.Fatalf("buildStore error = %v, want a vector-count mismatch error", err)
	}
}

func TestBuildStore_UpsertDimensionMismatch(t *testing.T) {
	// The embedder always returns 4-wide vectors, but the store is built for
	// dimension 99 — Upsert must reject with ErrDimensionMismatch, and
	// buildStore must wrap it as an "upsert corpus" error.
	emb := &fakeEmbedder{dim: 99, embed: constVector(4, 1)}

	_, err := buildStore(context.Background(), emb, 99, []doc{{ID: "d1", Text: "x"}})
	if err == nil || !strings.Contains(err.Error(), "upsert corpus") {
		t.Fatalf("buildStore error = %v, want an upsert corpus error", err)
	}
	if !errors.Is(err, semantic.ErrDimensionMismatch) {
		t.Errorf("error = %v, want it to wrap ErrDimensionMismatch", err)
	}
}

// --- runHarness / run ----------------------------------------------------

func TestRunHarness_SuccessJudgeSkipped(t *testing.T) {
	var buf bytes.Buffer
	outPath := filepath.Join(t.TempDir(), "results.txt")
	emb := semantic.NewHashEmbedder(8)
	jc := eval.JudgeClient{Provider: "stub", Real: false}

	err := runHarness(context.Background(), &buf, miniGoldPath, outPath, 5, emb, jc)
	if err != nil {
		t.Fatalf("runHarness: %v", err)
	}

	rendered := buf.String()
	for _, want := range []string{"mini-test-gold", "en_monolingual", "es_monolingual", "MACRO-AVG", "HARNESS VALIDATION", "SKIPPED"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("report missing %q", want)
		}
	}

	written, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read written report: %v", err)
	}
	if string(written) != rendered {
		t.Error("the file written to outPath must match what was printed to w")
	}
}

func TestRunHarness_SuccessJudgeRan(t *testing.T) {
	var buf bytes.Buffer
	outPath := filepath.Join(t.TempDir(), "results.txt")
	emb := semantic.NewHashEmbedder(8)
	jc := eval.JudgeClient{Client: llm.NewStubClient(), Provider: "stub-test", Real: true}

	err := runHarness(context.Background(), &buf, miniGoldPath, outPath, 5, emb, jc)
	if err != nil {
		t.Fatalf("runHarness: %v", err)
	}
	if !strings.Contains(buf.String(), "retrieved_context_relevance") {
		t.Error("expected the ran judge section in the report")
	}
}

func TestRunHarness_LoadGoldSetError(t *testing.T) {
	var buf bytes.Buffer
	err := runHarness(context.Background(), &buf, "testdata/does-not-exist.json", filepath.Join(t.TempDir(), "out.txt"), 5,
		semantic.NewHashEmbedder(8), eval.JudgeClient{})
	if err == nil {
		t.Fatal("expected an error for a missing gold set file")
	}
}

func TestRunHarness_BuildStoreError(t *testing.T) {
	var buf bytes.Buffer
	wantErr := errors.New("embed boom")
	emb := &fakeEmbedder{dim: 8, embed: func(context.Context, []string) ([][]float32, error) { return nil, wantErr }}

	err := runHarness(context.Background(), &buf, miniGoldPath, filepath.Join(t.TempDir(), "out.txt"), 5, emb, eval.JudgeClient{})
	if err == nil || !strings.Contains(err.Error(), "build") {
		t.Fatalf("runHarness error = %v, want a build-store error", err)
	}
}

func TestRunHarness_EvalRetrievalError(t *testing.T) {
	// A gold set whose single query text is the embedder's failure sentinel:
	// buildStore's batched corpus embed (len(texts)>1 or a different text)
	// succeeds, but EvalRetrieval's single-query embed for this case fails.
	body := `{
		"name": "retrieval-fail-fixture",
		"corpora": {"en": [{"id": "en-0", "text": "alpha"}, {"id": "en-1", "text": "beta"}]},
		"slices": [{"name": "en_monolingual", "query_lang": "en", "corpus_lang": "en",
			"cases": [{"name": "c", "query": "fail-query", "relevant_ids": ["en-0"]}]}]
	}`
	path := writeTempJSON(t, body)

	emb := &fakeEmbedder{dim: 8, embed: failOnText(8, "fail-query", errors.New("query embed boom"))}
	var buf bytes.Buffer
	err := runHarness(context.Background(), &buf, path, filepath.Join(t.TempDir(), "out.txt"), 5, emb, eval.JudgeClient{})
	if err == nil || !strings.Contains(err.Error(), "slice") {
		t.Fatalf("runHarness error = %v, want a slice-scoped retrieval error", err)
	}
}

func TestRunHarness_WriteReportError(t *testing.T) {
	var buf bytes.Buffer
	badOutPath := filepath.Join(t.TempDir(), "no-such-subdir", "results.txt")
	err := runHarness(context.Background(), &buf, miniGoldPath, badOutPath, 5, semantic.NewHashEmbedder(8), eval.JudgeClient{})
	if err == nil || !strings.Contains(err.Error(), "write report") {
		t.Fatalf("runHarness error = %v, want a write-report error", err)
	}
}

func TestRun_WrapsEnvDefaults(t *testing.T) {
	for _, k := range []string{"OPENROUTER_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY",
		"AISOTROPY_EMBED_BIN", "AISOTROPY_EMBED_URL", "AISOTROPY_JUDGE_MODEL"} {
		t.Setenv(k, "")
	}

	var buf bytes.Buffer
	outPath := filepath.Join(t.TempDir(), "results.txt")
	if err := run(context.Background(), &buf, miniGoldPath, outPath, 5); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(buf.String(), "mini-test-gold") {
		t.Error("expected the rendered report to reach the writer")
	}
}
