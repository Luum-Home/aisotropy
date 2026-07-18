package eval

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// This file closes remaining statement-coverage gaps in the eval package's
// error-handling and edge-case branches not reached by the existing
// dataset_test.go / judge_test.go / guide_test.go / metrics_test.go /
// provider_test.go / retrieval_test.go suites.

// --- dataset.go: decode-error and empty-dataset branches -------------------

func TestLoadEvalCasesDecodeError(t *testing.T) {
	_, err := LoadEvalCases(strings.NewReader("{not valid json"))
	if err == nil || !strings.Contains(err.Error(), "decode generation dataset") {
		t.Fatalf("LoadEvalCases(malformed) = %v, want a decode error", err)
	}
}

func TestLoadEvalCasesEmpty(t *testing.T) {
	_, err := LoadEvalCases(strings.NewReader(`{"name":"empty","cases":[]}`))
	if err == nil || !strings.Contains(err.Error(), "has no cases") {
		t.Fatalf("LoadEvalCases(empty) = %v, want a no-cases error", err)
	}
}

func TestLoadRetrievalCasesDecodeError(t *testing.T) {
	_, err := LoadRetrievalCases(strings.NewReader("{not valid json"))
	if err == nil || !strings.Contains(err.Error(), "decode retrieval dataset") {
		t.Fatalf("LoadRetrievalCases(malformed) = %v, want a decode error", err)
	}
}

func TestLoadRetrievalCasesEmpty(t *testing.T) {
	_, err := LoadRetrievalCases(strings.NewReader(`{"name":"empty","cases":[]}`))
	if err == nil || !strings.Contains(err.Error(), "has no cases") {
		t.Fatalf("LoadRetrievalCases(empty) = %v, want a no-cases error", err)
	}
}

// --- guide.go: Rubric.render empty branch and RunSuite ctx cancellation ----

func TestRubricRenderEmpty(t *testing.T) {
	if got := (Rubric{}).render(); got != "" {
		t.Fatalf("empty Rubric.render() = %q, want \"\"", got)
	}
}

func TestRunSuiteCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	judge := NewJSONValidityJudge()
	cases := []EvalCase{{Name: "c1", Sample: EvalSample{Response: "{}"}}}
	_, err := RunSuite(ctx, cases, []Judge{judge}, 0.5)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunSuite(cancelled ctx) = %v, want context.Canceled", err)
	}
}

// --- judge.go: retry default, Score error, validate, userPrompt, CodeJudge -

// TestNewLLMJudgeDefaultsRetries covers the maxRetries<=0 fallback: a caller
// that passes 0 (or a negative value) gets defaultJudgeRetries, not a
// zero-retry judge that can never reflect.
func TestNewLLMJudgeDefaultsRetries(t *testing.T) {
	judge, err := NewLLMJudge(llm.NewStubClient(), Relevance, RelevanceRubric, 0)
	if err != nil {
		t.Fatalf("NewLLMJudge: %v", err)
	}
	if judge.maxRetries != defaultJudgeRetries {
		t.Fatalf("maxRetries = %d, want default %d", judge.maxRetries, defaultJudgeRetries)
	}
}

// failingClient implements llm.Client and fails every StreamChat call, so
// LLMJudge.Score's reflection-failure branch is reachable without any real
// provider credential.
type failingClient struct{}

func (failingClient) StreamChat(context.Context, string, string) (<-chan llm.Token, error) {
	return nil, errors.New("llm: simulated unreachable provider")
}
func (failingClient) Name() string { return "failing-client" }

// TestLLMJudgeScoreOperationalFailure covers Score's error-wrap branch: an
// unreachable model must surface as an error, never a silent zero score.
func TestLLMJudgeScoreOperationalFailure(t *testing.T) {
	judge, err := NewLLMJudge(failingClient{}, Relevance, RelevanceRubric, 1)
	if err != nil {
		t.Fatalf("NewLLMJudge: %v", err)
	}
	_, err = judge.Score(context.Background(), EvalSample{Query: "q", Response: "r"})
	if err == nil || !strings.Contains(err.Error(), "reflection failed") {
		t.Fatalf("Score with an unreachable client = %v, want a wrapped reflection error", err)
	}
}

// TestLLMJudgeValidateOutOfScale covers validate's out-of-range feedback
// branch directly: a verdict outside the criterion's Scale must produce
// non-empty correction feedback (and no Go error — a bad number is a retry
// request, not a fatal).
func TestLLMJudgeValidateOutOfScale(t *testing.T) {
	judge, err := NewLLMJudge(llm.NewStubClient(), Relevance, RelevanceRubric, 1)
	if err != nil {
		t.Fatalf("NewLLMJudge: %v", err)
	}
	feedback, verr := judge.validate(context.Background(), verdict{Value: 5, Reasoning: "too high"})
	if verr != nil {
		t.Fatalf("validate out-of-scale = err %v, want nil error", verr)
	}
	if feedback == "" {
		t.Fatal("validate(out-of-scale) returned empty feedback, want a correction message")
	}
	if !strings.Contains(feedback, "must be within the scale") {
		t.Fatalf("feedback = %q, want it to mention the scale", feedback)
	}

	// In-scale value: no feedback.
	feedback2, verr2 := judge.validate(context.Background(), verdict{Value: 0.5})
	if verr2 != nil || feedback2 != "" {
		t.Fatalf("validate in-scale = (%q,%v), want (\"\", nil)", feedback2, verr2)
	}
}

// TestLLMJudgeUserPromptWithReference covers userPrompt's Reference-included
// branch: when a gold/ideal answer is present it must appear in the rendered
// prompt so a correctness judge can compare against it.
func TestLLMJudgeUserPromptWithReference(t *testing.T) {
	judge, err := NewLLMJudge(llm.NewStubClient(), Relevance, RelevanceRubric, 1)
	if err != nil {
		t.Fatalf("NewLLMJudge: %v", err)
	}
	prompt := judge.userPrompt(EvalSample{
		Query:     "What is the capital of France?",
		Reference: "Paris is the capital of France.",
		Response:  "The capital is Paris.",
	})
	if !strings.Contains(prompt, "Reference (ideal) answer:") || !strings.Contains(prompt, "Paris is the capital of France.") {
		t.Fatalf("userPrompt with a Reference = %q, want it to include the reference block", prompt)
	}
}

// TestCodeJudgeCriterion covers CodeJudge.Criterion(), never exercised by the
// existing Score-focused tests.
func TestCodeJudgeCriterion(t *testing.T) {
	judge := NewJSONValidityJudge()
	if got := judge.Criterion().Name; got != "json_validity" {
		t.Fatalf("CodeJudge.Criterion().Name = %q, want json_validity", got)
	}
}

// --- metrics.go: dedupTopK k<=0 and NDCGAtK's ideal>k clamp ----------------

func TestDedupTopKNonPositiveK(t *testing.T) {
	if got := dedupTopK([]string{"a", "b"}, 0); got != nil {
		t.Fatalf("dedupTopK(_, 0) = %v, want nil", got)
	}
	if got := dedupTopK([]string{"a", "b"}, -1); got != nil {
		t.Fatalf("dedupTopK(_, -1) = %v, want nil", got)
	}
}

// TestNDCGAtKIdealClampedToK covers the "ideal > k" clamp: when there are
// more relevant ids than the cutoff k, the ideal DCG must be computed over
// only the top k ranks, not every relevant id.
func TestNDCGAtKIdealClampedToK(t *testing.T) {
	retrieved := []string{"r1", "r2"}
	relevant := []string{"r1", "r2", "r3", "r4"} // 4 relevant, k=2 < 4
	got := NDCGAtK(retrieved, relevant, 2)
	// Both retrieved ids are relevant and in ideal order, so nDCG@2 = 1.0
	// regardless of the 2 additional relevant ids the cutoff never sees.
	if !almostEqual(got, 1.0) {
		t.Fatalf("NDCGAtK with ideal clamped to k = %v, want 1.0", got)
	}
}

// --- provider.go: judgeClientWithModel branches ----------------------------

// TestJudgeClientWithModelUnknownProvider covers the terminal "return nil"
// fallback: a provider name outside the three known registry providers falls
// through the switch with no case matched.
func TestJudgeClientWithModelUnknownProvider(t *testing.T) {
	if c := judgeClientWithModel("some-unknown-provider", "model-x"); c != nil {
		t.Fatalf("judgeClientWithModel(unknown) = %v, want nil", c)
	}
}

// TestJudgeClientWithModelMissingKey covers the false side of each provider's
// "if key set" guard: a known provider with no credential in the environment
// also falls through to nil.
func TestJudgeClientWithModelMissingKey(t *testing.T) {
	clearProviderEnv(t)
	for _, p := range []string{providerOpenRouter, providerOpenAI, providerAnthropic} {
		if c := judgeClientWithModel(p, "model-x"); c != nil {
			t.Fatalf("judgeClientWithModel(%q) with no key = %v, want nil", p, c)
		}
	}
}

// TestJudgeClientWithModelOpenRouterAndOpenAI covers the true side of the
// openrouter and openai branches (the anthropic branch is already exercised
// by provider_test.go's ModelOverride test).
func TestJudgeClientWithModelOpenRouterAndOpenAI(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")
	if c := judgeClientWithModel(providerOpenRouter, "model-or"); c == nil {
		t.Fatal("judgeClientWithModel(openrouter) with a key = nil, want a client")
	} else if !strings.Contains(c.Name(), "model-or") {
		t.Fatalf("openrouter client Name = %q, want it to carry the model id", c.Name())
	}

	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-oa-test")
	if c := judgeClientWithModel(providerOpenAI, "model-oa"); c == nil {
		t.Fatal("judgeClientWithModel(openai) with a key = nil, want a client")
	} else if !strings.Contains(c.Name(), "model-oa") {
		t.Fatalf("openai client Name = %q, want it to carry the model id", c.Name())
	}
}

// --- retrieval.go: ctx cancellation, embed error, vector-count mismatch, ---
// --- store search error -----------------------------------------------------

// erroringEmbedder implements semantic.Embedder and fails every Embed call.
type erroringEmbedder struct{ dim int }

func (e erroringEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("semantic: simulated embedder failure")
}
func (e erroringEmbedder) Dimension() int { return e.dim }

// wrongCountEmbedder returns a mismatched number of vectors relative to the
// requested texts, exercising EvalRetrieval's own defensive count check
// (rather than any behavior of the real embedders, which always return one
// vector per text).
type wrongCountEmbedder struct{ dim int }

func (e wrongCountEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return [][]float32{}, nil // zero vectors for a 1-text request
}
func (e wrongCountEmbedder) Dimension() int { return e.dim }

// erroringStore implements semantic.Store and fails every Search call.
type erroringStore struct{ dim int }

func (s erroringStore) Upsert(context.Context, semantic.Scope, []semantic.Point) error { return nil }
func (s erroringStore) Search(context.Context, semantic.Scope, []float32, int, ...semantic.Kind) ([]semantic.ScoredPoint, error) {
	return nil, errors.New("semantic: simulated store failure")
}
func (s erroringStore) Delete(context.Context, semantic.Scope, []string) error { return nil }
func (s erroringStore) DeleteBySource(context.Context, semantic.Scope, semantic.Kind, string) (int, error) {
	return 0, nil
}
func (s erroringStore) PruneTurns(context.Context, semantic.Scope, time.Duration, int) (int, error) {
	return 0, nil
}
func (s erroringStore) Close() error { return nil }

func TestEvalRetrievalCancelledContext(t *testing.T) {
	sc, err := semantic.NewScope("acme", "alice")
	if err != nil {
		t.Fatal(err)
	}
	emb := semantic.NewHashEmbedder(0)
	store := semantic.NewInMemoryStore(emb.Dimension())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []RetrievalCase{{Name: "c1", Query: "q"}}
	_, err = EvalRetrieval(ctx, store, sc, emb, cases, 3)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("EvalRetrieval(cancelled ctx) = %v, want context.Canceled", err)
	}
}

func TestEvalRetrievalEmbedError(t *testing.T) {
	sc, err := semantic.NewScope("acme", "alice")
	if err != nil {
		t.Fatal(err)
	}
	store := semantic.NewInMemoryStore(4)
	cases := []RetrievalCase{{Name: "c1", Query: "q"}}
	_, err = EvalRetrieval(context.Background(), store, sc, erroringEmbedder{dim: 4}, cases, 3)
	if err == nil || !strings.Contains(err.Error(), "embed query for case") {
		t.Fatalf("EvalRetrieval with a failing embedder = %v, want a wrapped embed error", err)
	}
}

func TestEvalRetrievalVectorCountMismatch(t *testing.T) {
	sc, err := semantic.NewScope("acme", "alice")
	if err != nil {
		t.Fatal(err)
	}
	store := semantic.NewInMemoryStore(4)
	cases := []RetrievalCase{{Name: "c1", Query: "q"}}
	_, err = EvalRetrieval(context.Background(), store, sc, wrongCountEmbedder{dim: 4}, cases, 3)
	if err == nil || !strings.Contains(err.Error(), "embedder returned") {
		t.Fatalf("EvalRetrieval with a mismatched embedder = %v, want a vector-count error", err)
	}
}

func TestEvalRetrievalSearchError(t *testing.T) {
	sc, err := semantic.NewScope("acme", "alice")
	if err != nil {
		t.Fatal(err)
	}
	emb := semantic.NewHashEmbedder(0)
	cases := []RetrievalCase{{Name: "c1", Query: "q"}}
	_, err = EvalRetrieval(context.Background(), erroringStore{dim: emb.Dimension()}, sc, emb, cases, 3)
	if err == nil || !strings.Contains(err.Error(), "search for case") {
		t.Fatalf("EvalRetrieval with a failing store = %v, want a wrapped search error", err)
	}
}

// --- documented uncoverable branches ---------------------------------------
//
// metrics.go NDCGAtK's "idcg == 0" branch is unreachable through the public
// API: it is only reached after the function already returns early for
// len(relevant)==0 and k<=0, and with both invariants holding, ideal =
// min(len(rel), k) is always >= 1, so the idcg accumulation loop always runs
// at least once and idcg is always > 0 (1/log2(2) = 1 at minimum). No input
// can drive idcg to exactly 0 once those two guards have passed.
//
// provider.go JudgeClientFromEnv's "override requested but judgeClientWithModel
// returned nil" fallback (documented in its own comment as "should not happen
// given real==true") is not reachable through the public function: the
// provider name and the credential judgeClientWithModel checks both come from
// the SAME llm.RegistryFromEnv() resolution, so whenever real==true the exact
// key judgeClientWithModel needs is, by construction, already set.
