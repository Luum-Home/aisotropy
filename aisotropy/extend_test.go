package aisotropy

// extend_test.go is the demonstrable artifact for the "bring your own"
// extension surface (extend.go's options + their app.go wiring). Every seam is
// proven INVOKED by the real running pipeline, not merely stored: a spy
// implementation records that the runtime called it. Most seams are exercised
// through the in-process App.Turn -> ChatCore path; the Reranker seam is
// exercised over the real /ask HTTP route, because semantic RAG hydration (the
// stage a Reranker sits in) lives on the streaming/ask paths, not the
// working-memory-centric non-streaming Turn path — so the test drives the seam
// where it actually runs rather than pretending Turn consults it.
//
// The in-process tests are keyless (fastStub, no ANTHROPIC_API_KEY) and defer
// app.Close(). They read invocation counters off spy implementations rather than
// the reply text, for the same reason turn_test.go reads the working-memory store
// directly: the non-streaming reflection pipeline returns a schema-satisfying
// reply that echoes only the turn's prompt, so a seam's effect is asserted where
// the seam actually runs, not off the reply string.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/eval"
	"github.com/Luum-Home/aisotropy/internal/gateway"
	"github.com/Luum-Home/aisotropy/internal/guardrails"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/routing"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// outputGuardRefusalText mirrors internal/api/guard.go's unexported
// outputGuardRefusal constant. It is duplicated here (not imported — the const is
// package-private to internal/api) so the block-output effect assertion is exact;
// if that refusal text ever changes, this test failing is the intended signal to
// update it in lockstep.
const outputGuardRefusalText = "I'm sorry, but I can't help with that request."

// --- spies ------------------------------------------------------------------

// spyGuardrailCheck is a GuardrailCheck that records each invocation and returns
// a caller-fixed verdict, so a test can prove both that the output inspector ran
// it and (with blocking on) that a Suspicious verdict replaces the response.
type spyGuardrailCheck struct {
	name       string
	suspicious bool
	calls      atomic.Int32
}

func (s *spyGuardrailCheck) Name() string { return s.name }

func (s *spyGuardrailCheck) Check(_ context.Context, _ guardrails.OutputCheckInput) guardrails.Verdict {
	s.calls.Add(1)
	if s.suspicious {
		return guardrails.Verdict{Suspicious: true, Signals: []string{"spy:flagged"}, Score: 1.0}
	}
	return guardrails.Verdict{}
}

// spySummarizer is a Summarizer that records each Condense call. It returns a
// trivial deterministic condensation so the working-memory prune completes.
type spySummarizer struct {
	calls atomic.Int32
}

func (s *spySummarizer) Condense(_ context.Context, existing string, _ []memory.Message) (string, error) {
	s.calls.Add(1)
	return existing + " [spy-condensed]", nil
}

// spyModelPolicy is a ModelPolicy that records each Select call and then abstains
// (empty Target), so the built-in chain still decides the target — proving the
// consumer policy is consulted FIRST without altering routing.
type spyModelPolicy struct {
	calls atomic.Int32
}

func (s *spyModelPolicy) Select(_ context.Context, _ gateway.Request) gateway.Decision {
	s.calls.Add(1)
	return gateway.Decision{Reason: "spy: abstain"}
}

// spyTenantPolicy is a TenantRoutingPolicy that records each Tier call and marks
// exactly one tenant Dedicated, so a request for that tenant drives the routing
// store through the Router (which is what consults this policy).
type spyTenantPolicy struct {
	dedicated string
	calls     atomic.Int32
}

func (s *spyTenantPolicy) Tier(tenant string) routing.TenantTier {
	s.calls.Add(1)
	if tenant != "" && tenant == s.dedicated {
		return routing.Dedicated
	}
	return routing.Shared
}

// spyEvalJudge is an EvalJudge bound to the Safety criterion that records each
// Score call and returns a safe (non-flagging) score, so the guardrails output
// check runs it without blocking the turn.
type spyEvalJudge struct {
	calls atomic.Int32
}

func (s *spyEvalJudge) Criterion() eval.Criterion { return eval.Safety }

func (s *spyEvalJudge) Score(_ context.Context, _ eval.EvalSample) (eval.Score, error) {
	s.calls.Add(1)
	return eval.Score{Criterion: eval.Safety.Name, Value: 1.0, Reasoning: "spy: safe"}, nil
}

// spyReranker satisfies Reranker and records each invocation. It is a pure
// pass-through (returns the candidate pool unchanged), so it never alters the
// injected context — the assertion is purely that the wired hydration path called
// it. Recording the query lets the test confirm the real prompt reached the seam.
type spyReranker struct {
	calls     atomic.Int32
	lastQuery atomic.Value // string
}

func (s *spyReranker) Rerank(_ context.Context, query string, candidates []semantic.ScoredPoint, _ int) []semantic.ScoredPoint {
	s.calls.Add(1)
	s.lastQuery.Store(query)
	return candidates
}

// --- helpers ----------------------------------------------------------------

// closeApp defers an App.Close and surfaces its error as a test failure.
func closeApp(t *testing.T, app *App) func() {
	t.Helper()
	return func() {
		if err := app.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}
}

// --- Task A: Tier-1 seam wiring ---------------------------------------------

// TestWithGuardrailCheckInvoked proves a consumer output check is appended to the
// runtime's inspector and runs on every model response (invocation), and that —
// with AISOTROPY_GUARDRAILS_BLOCK_OUTPUT on — a Suspicious verdict from it
// replaces the reply with the safe refusal (effect).
func TestWithGuardrailCheckInvoked(t *testing.T) {
	t.Run("invoked", func(t *testing.T) {
		spy := &spyGuardrailCheck{name: "spy-safe"}
		app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9), WithGuardrailCheck(spy))
		defer closeApp(t, app)()

		if _, err := app.Turn(context.Background(), ChatInput{TenantID: "t", UserID: "u", SessionID: "s", Prompt: "hello"}); err != nil {
			t.Fatalf("turn: %v", err)
		}
		if spy.calls.Load() == 0 {
			t.Fatal("guardrail check was never invoked by the output inspector")
		}
	})

	t.Run("block-output effect", func(t *testing.T) {
		t.Setenv("AISOTROPY_GUARDRAILS_BLOCK_OUTPUT", "true")
		spy := &spyGuardrailCheck{name: "spy-flag", suspicious: true}
		app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9), WithGuardrailCheck(spy))
		defer closeApp(t, app)()

		out, err := app.Turn(context.Background(), ChatInput{TenantID: "t", UserID: "u", SessionID: "s", Prompt: "hello"})
		if err != nil {
			t.Fatalf("turn: %v", err)
		}
		if spy.calls.Load() == 0 {
			t.Fatal("guardrail check was never invoked")
		}
		if out.Reply != outputGuardRefusalText {
			t.Fatalf("reply = %q, want the safe refusal %q (a Suspicious consumer check with BLOCK_OUTPUT on must replace the response)", out.Reply, outputGuardRefusalText)
		}
	})
}

// TestWithSummarizerInvoked proves the consumer Summarizer replaces the built-in
// LLMSummarizer: driving enough turns on one session to overflow the active
// working-memory window (defaultMaxActiveMessages = 12; two messages persist per
// non-streaming turn) triggers background condensation through the spy.
func TestWithSummarizerInvoked(t *testing.T) {
	spy := &spySummarizer{}
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9), WithSummarizer(spy))
	defer closeApp(t, app)()

	ctx := context.Background()
	// 10 turns => ~20 persisted messages, comfortably over the 12-message window.
	for i := 0; i < 10; i++ {
		if _, err := app.Turn(ctx, ChatInput{TenantID: "t", UserID: "u", SessionID: "sess", Prompt: "message"}); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}

	rt, err := app.ensureRuntime()
	if err != nil {
		t.Fatalf("ensureRuntime: %v", err)
	}
	rt.wm.Sync() // drain background condensation so the count is deterministic
	if spy.calls.Load() == 0 {
		t.Fatal("custom summarizer was never invoked despite overflowing the active window")
	}
}

// TestWithEvalJudgeInvoked proves a consumer EvalJudge is used as the guardrails
// output judge — attached regardless of the AISOTROPY_GUARDRAILS_LLM_JUDGE env
// toggle — so the output inspection scores every reply through it.
func TestWithEvalJudgeInvoked(t *testing.T) {
	spy := &spyEvalJudge{}
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9), WithEvalJudge(spy))
	defer closeApp(t, app)()

	if _, err := app.Turn(context.Background(), ChatInput{TenantID: "t", UserID: "u", SessionID: "s", Prompt: "hello"}); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if spy.calls.Load() == 0 {
		t.Fatal("custom eval judge was never invoked by the output inspector")
	}
}

// TestWithModelPolicyInvoked proves a consumer ModelPolicy is chained into the
// model-selection gateway and consulted on every turn (it abstains, so routing is
// unchanged — the point is that Select is called).
func TestWithModelPolicyInvoked(t *testing.T) {
	spy := &spyModelPolicy{}
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9), WithModelPolicy(spy))
	defer closeApp(t, app)()

	if _, err := app.Turn(context.Background(), ChatInput{TenantID: "t", UserID: "u", SessionID: "s", Prompt: "hello"}); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if spy.calls.Load() == 0 {
		t.Fatal("custom model policy was never consulted by the gateway")
	}
}

// TestWithTenantRoutingPolicyInvoked proves a consumer TenantRoutingPolicy
// replaces the static allowlist as the Router's tier policy. The Router is only
// consulted when a routing store exists, which requires a dedicated-tenant
// allowlist; with one configured, a turn for that tenant routes through the
// Router and consults the policy.
func TestWithTenantRoutingPolicyInvoked(t *testing.T) {
	t.Setenv("AISOTROPY_DEDICATED_TENANTS", "acme")
	spy := &spyTenantPolicy{dedicated: "acme"}
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9), WithTenantRoutingPolicy(spy))
	defer closeApp(t, app)()

	if _, err := app.Turn(context.Background(), ChatInput{TenantID: "acme", UserID: "u", SessionID: "s", Prompt: "hello"}); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if spy.calls.Load() == 0 {
		t.Fatal("custom tenant-routing policy was never consulted by the Router")
	}
}

// TestWithRerankerInvoked proves WithReranker is APPLIED: a registered Reranker
// is consulted by the semantic hydration path. The seam lives on the RAG
// hydration stage (streaming /chat/stream and the stateless /ask route), NOT the
// working-memory-centric non-streaming Turn path, so the test drives the seam
// where it runs — one /ask over the real HTTP server — and asserts the wired
// hydration called Rerank with the request's prompt. (A fresh prompt is a cache
// MISS, so the request reaches hydrateSystemPrompt -> searchSemanticContext,
// which hands the isolation-clean candidate pool to the reranker before the model
// call; ADR-0033.) It also asserts the option is stored on the App.
func TestWithRerankerInvoked(t *testing.T) {
	spy := &spyReranker{}
	addr := freeAddr(t)
	// NoAuth(): /ask is a /v1 route behind the default-deny gate; the explicit
	// local/dev opt-out lets the test exercise it over HTTP without minting JWTs.
	app := New(WithAddr(addr), WithLLM(fastStub()), WithReranker(spy), WithAuthenticator(NoAuth()))

	if app.ext.reranker == nil {
		t.Fatal("WithReranker did not store the reranker on the App")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	base := "http://" + addr
	waitForServer(t, base+"/healthz")

	const prompt = "what did we decide about the rollout plan?"
	_ = postAsk(t, ctx, base, "acme", "alice", prompt)

	if got := spy.calls.Load(); got == 0 {
		t.Fatal("reranker Rerank was never invoked by the /ask semantic hydration path; WithReranker must now be APPLIED")
	}
	if q, _ := spy.lastQuery.Load().(string); q != prompt {
		t.Fatalf("reranker received query %q, want the request prompt %q (the wired hydration must pass the real prompt to the seam)", q, prompt)
	}

	cancel()
	if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned error on shutdown: %v", err)
	}
}

// --- Task B: plugin loading -------------------------------------------------

// TestWithPluginDirContributesTool proves the plugin loader is wired into the
// runtime: a go-import manifest discovered in a WithPluginDir directory
// contributes its declared, scope-gated tool into the runtime's tool registry
// (Permitted, the same gate native and MCP tools pass). go-import needs no
// subprocess, so this exercises the merge path without spawning anything.
func TestWithPluginDirContributesTool(t *testing.T) {
	dir := t.TempDir()
	manifest := "" +
		"name: test-plugin\n" +
		"version: 0.0.1\n" +
		"license: MIT\n" +
		"kind: go-import\n" +
		"tools:\n" +
		"  - name: plugin_echo\n" +
		"    description: echo tool contributed by a test plugin\n" +
		"    required_scope: \"\"\n"
	if err := os.WriteFile(filepath.Join(dir, "aisotropy-plugin.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9), WithPluginDir(dir))
	defer closeApp(t, app)()

	rt, err := app.ensureRuntime()
	if err != nil {
		t.Fatalf("ensureRuntime: %v", err)
	}
	found := false
	for _, tool := range rt.toolRegistry.Permitted(nil) { // nil scopes => empty-scope tools
		if tool.Name == "plugin_echo" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("plugin-declared tool plugin_echo was not merged into the runtime tool registry")
	}
}

// TestWithPluginFileContributesTool proves the single-file WithPlugin path
// (as opposed to a directory scan) merges a manifest's tool identically.
func TestWithPluginFileContributesTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "echo-plugin.yaml")
	manifest := "" +
		"name: file-plugin\n" +
		"version: 1.0.0\n" +
		"license: Apache-2.0\n" +
		"kind: go-import\n" +
		"tools:\n" +
		"  - name: file_plugin_tool\n" +
		"    description: tool from a single-file plugin\n"
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9), WithPlugin(path))
	defer closeApp(t, app)()

	rt, err := app.ensureRuntime()
	if err != nil {
		t.Fatalf("ensureRuntime: %v", err)
	}
	found := false
	for _, tool := range rt.toolRegistry.Permitted(nil) {
		if tool.Name == "file_plugin_tool" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("single-file plugin tool file_plugin_tool was not merged into the runtime tool registry")
	}
}

// TestNoOptionsRuntimeUnchanged is the inert-safety anchor: an App built with no
// Wave-2 extension options boots and serves a turn exactly as before (none of the
// new seams alter the keyless/stub baseline). It complements the CLOSED-LOOP
// keyless boot of cmd/aisotropyd by asserting parity at the library surface.
func TestNoOptionsRuntimeUnchanged(t *testing.T) {
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))
	defer closeApp(t, app)()

	out, err := app.Turn(context.Background(), ChatInput{TenantID: "t", UserID: "u", SessionID: "s", Prompt: "hello"})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	if !out.Stub {
		t.Error("expected the keyless stub client")
	}
	if out.TenantID != "t" {
		t.Errorf("tenant echo = %q, want t", out.TenantID)
	}
}
