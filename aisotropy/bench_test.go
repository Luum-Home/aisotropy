package aisotropy

// bench_test.go carries the two honest benchmark classes for the public runtime,
// both with the MODEL HELD CONSTANT as the keyless stub so that measured time is
// framework overhead, never model performance (which is not a framework property):
//
//   CLASS 1 — framework/runtime OVERHEAD (BenchmarkTurn): one App.Turn end to end
//   — identity→ctx, pre-spend quota gate, guardrail, gateway selection, memory
//   hydration, transactional-outbox persist, fail-closed reflection loop, output
//   guardrail, cost record — with a zero-latency stub so ns/op is the pipeline's
//   own cost. This is the in-process (no-HTTP) hot path a library consumer pays.
//
//   CLASS 2b — cost-governance EFFECTIVENESS with NO real model, proven by a
//   TOKEN-COUNTING stub (countingClient) that tallies model invocations and the
//   provider-reported Usage on every call it forwards:
//     * TestQuotaGatePreSpendReducesTokenSpend — the constitutional pre-spend
//       gate: a rate-limited tenant is rejected BEFORE StreamChat is ever called,
//       so the counter proves zero tokens were spent on the rejected turns.
//     * TestResponseCacheReducesTokenSpend — the real /ask route's response cache:
//       an identical repeated question is served from cache with NO second model
//       call, so the counter proves the cached turn spent zero tokens.
//   Neither needs a provider: the counter delta IS the proof (mirroring the
//   call-counter method used across arenas/COST-OBS.md).
//
// Class 2a (true bits/token memory-lift SCORING) needs a real provider + eval and
// lives in internal/bitrate behind the `manual` build tag — see that package's
// harness_test.go for the model-free plumbing proof.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// countingClient is a token-counting LLM stub: it wraps an inner Client (the
// keyless stub) and, for every StreamChat it forwards, counts the call and
// accumulates the provider-reported Usage from the terminal token. A model call
// that never happens (because a gate rejected the turn upstream, or a cache
// served it) is never counted — which is exactly what makes it a proof of
// pre-spend governance: the counter can only advance when tokens are actually
// spent.
type countingClient struct {
	inner     llm.Client
	calls     atomic.Int64
	inTokens  atomic.Int64
	outTokens atomic.Int64
}

func (c *countingClient) Name() string { return c.inner.Name() }

func (c *countingClient) StreamChat(ctx context.Context, system, prompt string) (<-chan llm.Token, error) {
	c.calls.Add(1)
	up, err := c.inner.StreamChat(ctx, system, prompt)
	if err != nil {
		return nil, err
	}
	out := make(chan llm.Token)
	go func() {
		defer close(out)
		for tok := range up {
			if tok.Done && tok.Usage != nil {
				c.inTokens.Add(int64(tok.Usage.InputTokens))
				c.outTokens.Add(int64(tok.Usage.OutputTokens))
			}
			select {
			case <-ctx.Done():
				return
			case out <- tok:
			}
		}
	}()
	return out, nil
}

// totalTokens is the sum this client has seen actually spent (input + output).
func (c *countingClient) totalTokens() int64 { return c.inTokens.Load() + c.outTokens.Load() }

// BenchmarkTurn measures CLASS 1: the full in-process turn pipeline with the
// model held constant as a zero-latency stub, so ns/op and allocs/op are the
// framework's own overhead per turn (no HTTP hop, no model latency). It reuses
// one warmed runtime and one session, the realistic steady state a long-lived
// consumer runs (working memory bounded by its window; async condensation does
// not block Turn).
func BenchmarkTurn(b *testing.B) {
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))
	defer func() { _ = app.Close() }()

	ctx := context.Background()
	// Warm the lazily-built runtime once so the first measured iteration is not
	// paying the one-time boot cost.
	if _, err := app.Turn(ctx, ChatInput{TenantID: "bench", UserID: "u", SessionID: "s", Prompt: "warmup"}); err != nil {
		b.Fatalf("warmup Turn: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := app.Turn(ctx, ChatInput{TenantID: "bench", UserID: "u", SessionID: "s", Prompt: "hello world, run one full turn"}); err != nil {
			b.Fatalf("Turn: %v", err)
		}
	}
}

// runTurnsCounting fires `attempts` in-process turns for one tenant against an
// App wired with a token-counting stub and the given token-bucket (rate, burst),
// returning the counting client plus how many turns were rejected with a 429
// (quota) versus failing for some other reason. It is the shared driver behind
// the quota-gate proof and its uncapped control.
//
// Each turn uses a DISTINCT session so its two-message buffer (user + persisted
// reply) never overflows the working-memory window. That matters for a clean
// count: the default summarizer is memory.LLMSummarizer, so an overflowing
// session would spawn a BACKGROUND condensation LLM call and inflate the model
// counter with work that is not part of the request's own spend. One turn per
// session keeps every counted call attributable to a served turn.
func runTurnsCounting(t *testing.T, rate, burst float64, attempts int) (cc *countingClient, rejected int) {
	t.Helper()
	cc = &countingClient{inner: fastStub()}
	app := New(WithLLM(cc), WithRateLimit(rate, burst))
	t.Cleanup(func() { _ = app.Close() })

	ctx := context.Background()
	for i := range attempts {
		_, err := app.Turn(ctx, ChatInput{TenantID: "acme", UserID: "alice", SessionID: fmt.Sprintf("s%d", i), Prompt: fmt.Sprintf("question %d", i)})
		if err == nil {
			continue
		}
		var se huma.StatusError
		if errors.As(err, &se) && se.GetStatus() == http.StatusTooManyRequests {
			rejected++
			continue
		}
		t.Fatalf("turn %d failed with a non-quota error: %v", i, err)
	}
	return cc, rejected
}

// TestQuotaGatePreSpendReducesTokenSpend proves the constitutional pre-spend
// gate: with a token bucket of burst=3 and zero refill, only 3 of 10 turns may
// reach the model; the other 7 are rejected with a 429 BEFORE StreamChat is
// called, so the token-counting stub records exactly 3 model calls and the
// tokens spent are strictly fewer than the uncapped control that lets all 10
// through. The counter delta is the proof — no provider involved.
func TestQuotaGatePreSpendReducesTokenSpend(t *testing.T) {
	const (
		burst    = 3
		attempts = 10
	)

	// Gated: rate 0 (no refill), burst 3 → at most 3 turns can ever spend.
	gated, rejected := runTurnsCounting(t, 0, burst, attempts)
	// Uncapped control: effectively unlimited bucket → every turn spends.
	control, controlRejected := runTurnsCounting(t, 1e9, 1e9, attempts)

	gatedCalls := gated.calls.Load()
	controlCalls := control.calls.Load()

	if gatedCalls != burst {
		t.Errorf("gated model calls = %d, want exactly %d (burst): the gate must stop spend after the bucket empties", gatedCalls, burst)
	}
	if rejected != attempts-burst {
		t.Errorf("gated rejections = %d, want %d (all post-burst turns rejected pre-spend)", rejected, attempts-burst)
	}
	if controlRejected != 0 {
		t.Errorf("control rejections = %d, want 0 (the uncapped control must never reject)", controlRejected)
	}
	// The uncapped control must let EVERY turn reach the model (>= attempts; a
	// legitimate reflection retry could push it slightly higher, which is not a
	// gating failure). The load-bearing contrast is gated==burst vs. control
	// reaching every turn.
	if controlCalls < attempts {
		t.Errorf("control model calls = %d, want >= %d (every turn must reach the model without a gate)", controlCalls, attempts)
	}
	if gatedCalls >= controlCalls {
		t.Errorf("gated calls (%d) must be < control calls (%d): the gate must cut model invocations", gatedCalls, controlCalls)
	}
	if gated.totalTokens() >= control.totalTokens() {
		t.Errorf("gated tokens (%d) must be < control tokens (%d): the pre-spend gate must reduce spend", gated.totalTokens(), control.totalTokens())
	}

	saved := control.totalTokens() - gated.totalTokens()
	t.Logf("QUOTA GATE (no model): %d/%d turns rejected pre-spend; model calls gated=%d vs control=%d; tokens gated=%d vs control=%d (saved %d, %.0f%%)",
		rejected, attempts, gatedCalls, controlCalls, gated.totalTokens(), control.totalTokens(), saved,
		100*float64(saved)/float64(control.totalTokens()))
}

// askResponse is the /ask route's strict output body (see internal/api/ask.go).
type askResponse struct {
	Reply    string `json:"reply"`
	TenantID string `json:"tenant_id"`
	Cached   bool   `json:"cached"`
	Stub     bool   `json:"stub"`
}

// postAsk issues one POST /v1/tenant/{tenant}/user/{user}/ask and decodes the
// strict body. It fails the test on any non-200 or decode error.
func postAsk(t *testing.T, ctx context.Context, base, tenant, user, prompt string) askResponse {
	t.Helper()
	// product_id is a required property of the /ask contract (internal/api's
	// AskRequest; the Go struct IS the OpenAPI schema, ADR-0002). Send it
	// explicitly — an empty value is present-and-valid and resolves to the default
	// persona — so the request passes body validation instead of a 422.
	body, err := json.Marshal(map[string]string{"prompt": prompt, "product_id": ""})
	if err != nil {
		t.Fatalf("marshal ask body: %v", err)
	}
	url := fmt.Sprintf("%s/v1/tenant/%s/user/%s/ask", base, tenant, user)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build ask request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /ask: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /ask = %d, want 200; body: %s", resp.StatusCode, raw)
	}
	// The Huma body is the top-level object here (no envelope) for this route.
	var out askResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode /ask body %q: %v", raw, err)
	}
	return out
}

// TestResponseCacheReducesTokenSpend proves the second cost-governance lever end
// to end over the REAL stateless /ask route: an identical question asked twice
// hits the per-(tenant,user) response cache the second time, so the second turn
// is served WITHOUT any LLM call. The token-counting stub records exactly ONE
// model call across the two requests, and the tokens spent on the cached turn
// are zero. No provider is involved — the counter is the proof.
func TestResponseCacheReducesTokenSpend(t *testing.T) {
	cc := &countingClient{inner: fastStub()}
	addr := freeAddr(t)
	// NoAuth(): the /ask route is a /v1 route behind the default-deny auth gate;
	// this is the explicit local/dev opt-out so the test can exercise it over HTTP
	// without minting JWTs. Auth is orthogonal to the cache behavior under test.
	app := New(WithAddr(addr), WithLLM(cc), WithAuthenticator(NoAuth()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	base := "http://" + addr
	waitForServer(t, base+"/healthz")

	const prompt = "what is the capital of France?"

	// First ask: cache MISS → one model call.
	first := postAsk(t, ctx, base, "acme", "alice", prompt)
	if first.Cached {
		t.Errorf("first ask reported cached=true; the first identical question must be a cache MISS")
	}
	afterFirst := cc.calls.Load()
	if afterFirst != 1 {
		t.Errorf("model calls after first ask = %d, want 1", afterFirst)
	}
	tokensAfterFirst := cc.totalTokens()

	// Second identical ask: cache HIT → NO model call.
	second := postAsk(t, ctx, base, "acme", "alice", prompt)
	if !second.Cached {
		t.Errorf("second identical ask reported cached=false; it must be served from the response cache")
	}
	if got := cc.calls.Load(); got != afterFirst {
		t.Errorf("model calls after second ask = %d, want %d unchanged: the cached turn must not call the model", got, afterFirst)
	}
	if got := cc.totalTokens(); got != tokensAfterFirst {
		t.Errorf("tokens after second ask = %d, want %d unchanged: the cached turn must spend zero tokens", got, tokensAfterFirst)
	}
	if first.Reply != second.Reply {
		t.Errorf("cached reply %q != original reply %q", second.Reply, first.Reply)
	}

	t.Logf("RESPONSE CACHE (no model): 2 identical /ask requests → 1 model call, %d tokens total; the cached turn spent 0 additional tokens",
		cc.totalTokens())

	cancel()
	if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned error on shutdown: %v", err)
	}
}
