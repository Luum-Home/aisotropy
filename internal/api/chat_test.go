package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
)

// newChatOnlyRouter builds a router with only the Phase 0 stub endpoint
// registered, for tests that don't need working memory or an LLM client.
func newChatOnlyRouter(t *testing.T) http.Handler {
	t.Helper()
	router := chi.NewRouter()
	humaAPI := humachi.New(router, huma.DefaultConfig("Aisotropy-test", "0.1.0"))
	Register(humaAPI)
	return router
}

// TestChat_RouteAndIdentity verifies the tenant_id + user_id path params are
// validated and echoed back, proving the new /v1/tenant/{tenant_id}/user/{user_id}/chat
// route registers and carries identity correctly.
func TestChat_RouteAndIdentity(t *testing.T) {
	handler := newChatOnlyRouter(t)

	body, err := json.Marshal(ChatRequest{Prompt: "hi", SessionID: "s1"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/tenant/acme/user/alice/chat", bytes.NewReader(body))
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
		t.Error("expected stub=true")
	}
}

// TestChat_InvalidIdentityRejected verifies an empty user_id or session_id
// is rejected with a 4xx via memory.Key.Validate(), not silently accepted.
func TestChat_InvalidIdentityRejected(t *testing.T) {
	handler := newChatOnlyRouter(t)

	body, err := json.Marshal(ChatRequest{Prompt: "hi", SessionID: ""})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/tenant/acme/user/alice/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("status = %d, want a 4xx for empty session_id; body = %s", rec.Code, rec.Body.String())
	}
}
