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

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
)

// newChatOnlyRouter builds a router with only the non-streaming chat
// endpoint registered, for tests that don't need the SSE endpoint too.
func newChatOnlyRouter(t *testing.T, client llm.Client, wm *memory.WorkingMemory) http.Handler {
	t.Helper()
	router := chi.NewRouter()
	humaAPI := humachi.New(router, huma.DefaultConfig("Aisotropy-test", "0.1.0"))
	store, err := facts.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("facts.OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("facts store close: %v", err)
		}
	})
	Register(humaAPI, client, wm, store, nil, nil, nil)
	return router
}

// alwaysInvalidClient is an llm.Client that never returns valid JSON, no
// matter how many times it is asked — it exists to prove the reflection loop
// fails closed (an HTTP error, bounded retries) on a persistent schema
// violation, instead of ever falling back to unstructured text (the
// constitutional "Structured-output / reflection loop" gate).
type alwaysInvalidClient struct {
	calls int
}

func (c *alwaysInvalidClient) Name() string { return "always-invalid" }

func (c *alwaysInvalidClient) StreamChat(_ context.Context, _, _ string) (<-chan llm.Token, error) {
	c.calls++
	out := make(chan llm.Token, 2)
	out <- llm.Token{Text: "this is deliberately not JSON, no matter the schema instruction"}
	out <- llm.Token{Done: true}
	close(out)
	return out, nil
}

// TestChat_RouteAndIdentity verifies the tenant_id + user_id path params are
// validated and echoed back, and that a real reply (produced via the
// reflection loop over the keyless stub client) is returned — proving the
// route registers, carries identity correctly, and no longer returns the
// Phase 0 canned stub payload.
func TestChat_RouteAndIdentity(t *testing.T) {
	wm := newTestWorkingMemory(t)
	handler := newChatOnlyRouter(t, llm.NewStubClient(), wm)

	body, err := json.Marshal(ChatRequest{Prompt: "hi", SessionID: "s1"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/tenant/acme/user/alice/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var out struct {
		Reply    string `json:"reply"`
		TenantID string `json:"tenant_id"`
		Stub     bool   `json:"stub"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.TenantID != "acme" {
		t.Errorf("tenant_id = %q, want %q", out.TenantID, "acme")
	}
	if !out.Stub {
		t.Error("expected stub=true (keyless StubClient resolved)")
	}
	if out.Reply == "" {
		t.Error("expected a non-empty reply from the reflection loop")
	}

	// The assistant's structured reply is persisted to working memory,
	// alongside the user's turn — proving this is a real reflection-loop
	// call, not the old Phase 0 stub that never touched working memory.
	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
	got, err := wm.Recent(context.Background(), key, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Recent returned %d messages, want 2 (user + assistant): %+v", len(got), got)
	}
	if got[0].Role != "user" || got[0].Content != "hi" {
		t.Errorf("message 0 = %+v, want user message %q", got[0], "hi")
	}
	if got[1].Role != "assistant" || got[1].Content != out.Reply {
		t.Errorf("message 1 = %+v, want assistant message %q", got[1], out.Reply)
	}
}

// TestChat_InvalidIdentityRejected verifies an empty user_id or session_id
// is rejected with a 4xx via memory.Key.Validate(), not silently accepted —
// and, since identity validation runs before the LLM is ever invoked, this
// must reject deterministically regardless of which Client is wired in.
func TestChat_InvalidIdentityRejected(t *testing.T) {
	wm := newTestWorkingMemory(t)
	handler := newChatOnlyRouter(t, llm.NewStubClient(), wm)

	body, err := json.Marshal(ChatRequest{Prompt: "hi", SessionID: ""})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/tenant/acme/user/alice/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("status = %d, want a 4xx for empty session_id; body = %s", rec.Code, rec.Body.String())
	}
}

// TestChat_FailsClosedOnPersistentSchemaViolation verifies the constitutional
// "Structured-output / reflection loop" gate: when the underlying Client
// persistently returns output that never satisfies chatStructuredReply's
// schema, the endpoint fails closed with an HTTP error after bounded
// retries — it must never fall back to returning the raw unstructured text.
func TestChat_FailsClosedOnPersistentSchemaViolation(t *testing.T) {
	wm := newTestWorkingMemory(t)
	client := &alwaysInvalidClient{}
	handler := newChatOnlyRouter(t, client, wm)

	body, err := json.Marshal(ChatRequest{Prompt: "hi", SessionID: "s1"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/tenant/acme/user/alice/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code < 500 {
		t.Fatalf("status = %d, want a >=500 error for a persistent schema violation (fail closed); body = %s", rec.Code, rec.Body.String())
	}

	var out struct {
		Reply string `json:"reply"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Reply != "" {
		t.Fatalf("expected no reply field populated on a failed-closed response, got %q", out.Reply)
	}

	// Bounded retries: the client must have been called more than once (the
	// reflection loop actually retried) but a finite, small number of times
	// (never unbounded/hanging).
	if client.calls < 2 {
		t.Errorf("client.calls = %d, expected at least 2 (schema-repair retries actually attempted)", client.calls)
	}
	if client.calls > 20 {
		t.Errorf("client.calls = %d, expected a small bounded number of attempts, not effectively unbounded", client.calls)
	}

	// No assistant turn is persisted on a failed-closed request — only the
	// user's turn (appended before the reflection loop runs).
	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
	got, err := wm.Recent(context.Background(), key, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 1 || got[0].Role != "user" {
		t.Fatalf("Recent returned %+v, want exactly 1 user message (no assistant turn persisted on failure)", got)
	}
}
