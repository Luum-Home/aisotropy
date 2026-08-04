package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"github.com/Luum-Home/aisotropy/internal/guardrails"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/responsecache"
)

// countingClient is an llm.Client that returns a fixed canned reply and counts
// how many times StreamChat is invoked, so a test can prove a response-cache HIT
// did NOT call the model again. It also records the last prompt it received so
// the redaction test can assert the prompt reaching the model was scrubbed. Safe
// for concurrent use.
type countingClient struct {
	reply string

	mu         sync.Mutex
	calls      int
	lastPrompt string
	lastSystem string
}

func (c *countingClient) Name() string { return "counting" }

func (c *countingClient) StreamChat(_ context.Context, system, prompt string) (<-chan llm.Token, error) {
	c.mu.Lock()
	c.calls++
	c.lastPrompt = prompt
	c.lastSystem = system
	c.mu.Unlock()

	out := make(chan llm.Token, 2)
	out <- llm.Token{Text: c.reply}
	out <- llm.Token{Done: true, Usage: &llm.Usage{InputTokens: 1, OutputTokens: 1}}
	close(out)
	return out, nil
}

func (c *countingClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *countingClient) prompt() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastPrompt
}

// newExactOnlyCache builds an exact-only ResponseCache (no embedder needed), so
// /ask tests exercise the cache HIT/MISS + isolation behavior without depending
// on a semantic embedder.
func newExactOnlyCache() *responsecache.ResponseCache {
	return responsecache.New(responsecache.NewExactCache(nil, 0, false), nil)
}

// newAskRouter builds a router with only the /ask endpoint registered. semStore
// and embedder are nil (no RAG needed for the cache-behavior tests); persona nil
// selects the default persona.
func newAskRouter(t *testing.T, client llm.Client, cache *responsecache.ResponseCache, guard *Guard) http.Handler {
	t.Helper()
	router := chi.NewRouter()
	humaAPI := humachi.New(router, huma.DefaultConfig("Aisotropy-test", "0.1.0"))
	RegisterAsk(humaAPI, client, cache, nil, nil, nil, nil, nil, guard)
	return router
}

// askResult is the decoded AskOutput body.
type askResult struct {
	Reply    string `json:"reply"`
	TenantID string `json:"tenant_id"`
	Cached   bool   `json:"cached"`
	Stub     bool   `json:"stub"`
}

// postAsk drives POST /v1/tenant/{tenant}/user/{user}/ask and returns the HTTP
// status and the decoded body.
func postAsk(t *testing.T, handler http.Handler, tenant, user, prompt string) (int, askResult) {
	t.Helper()
	body, err := json.Marshal(AskRequest{Prompt: prompt, ProductID: "ask-test"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	path := "/v1/tenant/" + tenant + "/user/" + user + "/ask"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	var out askResult
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal response (%s): %v", rec.Body.String(), err)
		}
	}
	return rec.Code, out
}

// TestAsk_SecondIdenticalRequestServedFromCacheNoSecondLLMCall is the headline
// acceptance criterion: a second identical /ask within the SAME (tenant,user)
// scope is served from the response cache WITHOUT a second LLM call. The counting
// client proves the model was invoked exactly once across both requests.
func TestAsk_SecondIdenticalRequestServedFromCacheNoSecondLLMCall(t *testing.T) {
	client := &countingClient{reply: "cached answer"}
	handler := newAskRouter(t, client, newExactOnlyCache(), nil)

	const prompt = "what is aisotropy"

	// First request: MISS -> the model is called once, the answer is returned
	// fresh (cached=false).
	st, out := postAsk(t, handler, "acme", "alice", prompt)
	if st != http.StatusOK {
		t.Fatalf("first request: status = %d", st)
	}
	if out.Cached {
		t.Errorf("first request should NOT be served from cache (cached=true)")
	}
	if out.Reply != "cached answer" {
		t.Errorf("first request reply = %q, want %q", out.Reply, "cached answer")
	}
	if client.callCount() != 1 {
		t.Fatalf("after first request, LLM calls = %d, want 1", client.callCount())
	}

	// Second identical request in the SAME scope: HIT -> NO new LLM call.
	st, out = postAsk(t, handler, "acme", "alice", prompt)
	if st != http.StatusOK {
		t.Fatalf("second request: status = %d", st)
	}
	if !out.Cached {
		t.Errorf("second identical request should be served from cache (cached=false)")
	}
	if out.Reply != "cached answer" {
		t.Errorf("second request reply = %q, want %q", out.Reply, "cached answer")
	}
	if client.callCount() != 1 {
		t.Fatalf("a cache HIT must NOT call the LLM again: calls = %d, want 1", client.callCount())
	}
}

// TestAsk_CrossScopeIdenticalPromptIsCacheMiss proves the response cache's
// (tenant,user) isolation on the request path: an IDENTICAL prompt under a
// different tenant — or a different user within the same tenant — is a cache
// MISS and recomputes, and the original scope's entry is never disturbed. This
// reuses the internal/responsecache isolation guarantee (scope baked into the
// key) as the request-path acceptance.
func TestAsk_CrossScopeIdenticalPromptIsCacheMiss(t *testing.T) {
	client := &countingClient{reply: "answer"}
	handler := newAskRouter(t, client, newExactOnlyCache(), nil)

	const prompt = "identical question across tenants"

	// tenantA/userA: MISS -> 1 LLM call, cached for that scope.
	if st, out := postAsk(t, handler, "tenantA", "userA", prompt); st != http.StatusOK || out.Cached {
		t.Fatalf("seed tenantA/userA: status=%d cached=%v (want 200, cached=false)", st, out.Cached)
	}
	if client.callCount() != 1 {
		t.Fatalf("after seeding tenantA/userA, LLM calls = %d, want 1", client.callCount())
	}

	// tenantB/userB, IDENTICAL prompt: cross-tenant MISS -> must recompute.
	if _, out := postAsk(t, handler, "tenantB", "userB", prompt); out.Cached {
		t.Errorf("CROSS-TENANT CACHE HIT: tenantB/userB must NOT be served tenantA/userA's cached answer")
	}
	if client.callCount() != 2 {
		t.Fatalf("a different tenant must recompute: LLM calls = %d, want 2", client.callCount())
	}

	// tenantA/userB (same tenant, different user), IDENTICAL prompt: also a MISS,
	// because the cache scope is (tenant,user), not tenant alone.
	if _, out := postAsk(t, handler, "tenantA", "userB", prompt); out.Cached {
		t.Errorf("CROSS-USER CACHE HIT: tenantA/userB must NOT be served tenantA/userA's cached answer")
	}
	if client.callCount() != 3 {
		t.Fatalf("a different user must recompute: LLM calls = %d, want 3", client.callCount())
	}

	// Original scope still hits — its entry survived the other scopes' activity.
	if _, out := postAsk(t, handler, "tenantA", "userA", prompt); !out.Cached {
		t.Errorf("tenantA/userA's own cached answer should still HIT after other scopes recomputed")
	}
	if client.callCount() != 3 {
		t.Fatalf("original scope re-read must be a cache HIT (no new call): LLM calls = %d, want 3", client.callCount())
	}
}

// TestAsk_InputRedactionApplies proves the input guardrail runs on /ask exactly
// as it does on /chat: a secret in the prompt is redacted BEFORE it reaches the
// model (and, by extension, before it becomes the cache key). The counting client
// records the prompt it received; the raw email must be gone and its placeholder
// present.
func TestAsk_InputRedactionApplies(t *testing.T) {
	client := &countingClient{reply: "ok"}
	guard := &Guard{G: guardrails.Default()}
	handler := newAskRouter(t, client, newExactOnlyCache(), guard)

	const secretEmail = "alice.secret@example.com"
	prompt := "please email a summary to " + secretEmail

	if st, _ := postAsk(t, handler, "acme", "alice", prompt); st != http.StatusOK {
		t.Fatalf("status = %d", st)
	}

	got := client.prompt()
	if strings.Contains(got, secretEmail) {
		t.Errorf("input guardrail did NOT redact: the raw email %q reached the model in prompt %q", secretEmail, got)
	}
	if !strings.Contains(got, "[REDACTED_EMAIL]") {
		t.Errorf("expected the redaction placeholder [REDACTED_EMAIL] in the forwarded prompt, got %q", got)
	}
}
