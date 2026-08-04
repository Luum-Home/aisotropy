package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/Luum-Home/aisotropy/internal/auth"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
)

// The constitutional gate (.claude/rules/constitutional-gates.md) requires quota
// to be checked BEFORE any LLM call, on every route that makes one. /chat and
// /chat/stream did; /ask and /chat/agent did not — they were not even wired to a
// limiter, so the gate was structurally unreachable. /chat/agent is the worse of
// the two: its loop can make up to maxSteps billable provider calls per request.

// quotaLimiter returns a limiter whose bucket holds exactly `burst` requests and
// never refills, so the (burst+1)-th call is deterministically over quota.
func quotaLimiter(t *testing.T, burst float64) *memory.Limiter {
	t.Helper()
	return newTestWorkingMemory(t).NewLimiter(0, burst)
}

// newAskRouterWithLimiter mounts /ask behind NoAuth with a quota limiter wired.
func newAskRouterWithLimiter(t *testing.T, client llm.Client, limiter *memory.Limiter) http.Handler {
	t.Helper()
	router := chi.NewRouter()
	api := humachi.New(router, huma.DefaultConfig("ask-quota-test", "0.1.0"))
	api.UseMiddleware(BindAuthMiddleware(api, auth.NoAuth()))
	RegisterAsk(api, client, nil, nil, nil, nil, nil, limiter, nil)
	return router
}

// newAgentRouterWithLimiter mounts /chat/agent behind NoAuth with a quota limiter.
func newAgentRouterWithLimiter(t *testing.T, client llm.Client, limiter *memory.Limiter) http.Handler {
	t.Helper()
	router := chi.NewRouter()
	api := humachi.New(router, huma.DefaultConfig("agent-quota-test", "0.1.0"))
	api.UseMiddleware(BindAuthMiddleware(api, auth.NoAuth()))
	RegisterAgent(api, client, agentScopeRegistry(), DemoExecutor{}, (*TypedToolExecutor)(nil), nil, noop.NewTracerProvider(), 3, limiter, nil)
	return router
}

// postAskPrompt drives /ask and returns the HTTP status.
func postAskPrompt(t *testing.T, h http.Handler, tenant, user, prompt string) int {
	t.Helper()
	body, err := json.Marshal(map[string]any{"prompt": prompt, "product_id": "analytics-v1"})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/v1/tenant/"+tenant+"/user/"+user+"/ask", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	return rec.Code
}

// TestAsk_QuotaExhaustedReturns429 proves /ask now rejects an over-quota tenant
// with a 429 instead of serving an unbounded number of billable calls.
func TestAsk_QuotaExhaustedReturns429(t *testing.T) {
	// A client that fails the test if it is ever reached over quota.
	client := &countingClient{reply: "an answer"}
	h := newAskRouterWithLimiter(t, client, quotaLimiter(t, 1))

	if code := postAskPrompt(t, h, "acme", "alice", "first question"); code != http.StatusOK {
		t.Fatalf("first /ask status = %d, want 200 (the bucket had a token)", code)
	}
	if code := postAskPrompt(t, h, "acme", "alice", "second question"); code != http.StatusTooManyRequests {
		t.Fatalf("second /ask status = %d, want 429: the tenant is over quota", code)
	}
	if calls := client.callCount(); calls != 1 {
		t.Errorf("LLM was called %d times, want 1: the over-quota request must be rejected BEFORE spending tokens", calls)
	}
}

// TestAsk_QuotaIsPerTenant proves the gate scopes by tenant: draining acme's
// bucket must not throttle globex.
func TestAsk_QuotaIsPerTenant(t *testing.T) {
	h := newAskRouterWithLimiter(t, &countingClient{reply: "an answer"}, quotaLimiter(t, 1))

	if code := postAskPrompt(t, h, "acme", "alice", "q"); code != http.StatusOK {
		t.Fatalf("acme first /ask = %d, want 200", code)
	}
	if code := postAskPrompt(t, h, "acme", "alice", "q2"); code != http.StatusTooManyRequests {
		t.Fatalf("acme second /ask = %d, want 429", code)
	}
	if code := postAskPrompt(t, h, "globex", "bob", "q"); code != http.StatusOK {
		t.Fatalf("globex /ask = %d, want 200: acme's exhausted bucket must not throttle another tenant", code)
	}
}

// TestChatAgent_QuotaExhaustedReturns429 proves /chat/agent now rejects an
// over-quota tenant. This route matters most: each ADMITTED request can drive up
// to maxSteps billable provider calls.
//
// The assertion is "not 429" for an admitted request rather than "200": whether
// the stub model drives the tool loop to a clean answer is a different concern,
// and pinning it here would couple this gate's test to the loop's behaviour. What
// matters is that the request reached the loop at all.
func TestChatAgent_QuotaExhaustedReturns429(t *testing.T) {
	h := newAgentRouterWithLimiter(t, llm.NewStubClient(), quotaLimiter(t, 1))

	if code := postAgentPrompt(t, h, "acme", "alice", "what is 2+2"); code == http.StatusTooManyRequests {
		t.Fatal("first /chat/agent was rejected 429: the bucket had a token")
	}
	if code := postAgentPrompt(t, h, "acme", "alice", "what is 3+3"); code != http.StatusTooManyRequests {
		t.Fatalf("second /chat/agent status = %d, want 429: the tenant is over quota", code)
	}
}

// TestChatAgent_QuotaChargesOneTokenPerRequest pins the design decision: the
// bucket's unit is REQUESTS, uniformly across every LLM-serving route — one
// admitted request costs exactly one token, no matter how many loop steps it
// later takes. Charging maxSteps up front would give the same bucket a different
// meaning per route and over-charge every turn that converges in one step.
func TestChatAgent_QuotaChargesOneTokenPerRequest(t *testing.T) {
	h := newAgentRouterWithLimiter(t, llm.NewStubClient(), quotaLimiter(t, 3))

	for i := range 3 {
		if code := postAgentPrompt(t, h, "acme", "alice", "question"); code == http.StatusTooManyRequests {
			t.Fatalf("/chat/agent request %d was rejected 429: a burst of 3 must admit 3 requests", i+1)
		}
	}
	if code := postAgentPrompt(t, h, "acme", "alice", "question"); code != http.StatusTooManyRequests {
		t.Fatalf("4th /chat/agent status = %d, want 429", code)
	}
}

// TestQuotaGate_NilLimiterIsPassThrough keeps the nil-limiter degradation the
// rest of the package documents: a runtime wired without a limiter is unchanged.
func TestQuotaGate_NilLimiterIsPassThrough(t *testing.T) {
	askH := newAskRouterWithLimiter(t, &countingClient{reply: "a"}, nil)
	for i := range 3 {
		if code := postAskPrompt(t, askH, "acme", "alice", "q"); code != http.StatusOK {
			t.Fatalf("/ask %d with a nil limiter = %d, want 200", i+1, code)
		}
	}
	agentH := newAgentRouterWithLimiter(t, llm.NewStubClient(), nil)
	for i := range 3 {
		if code := postAgentPrompt(t, agentH, "acme", "alice", "q"); code == http.StatusTooManyRequests {
			t.Fatalf("/chat/agent %d with a nil limiter was rejected 429: the gate must be a pass-through", i+1)
		}
	}
}
