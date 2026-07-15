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
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// TestEffectiveScopes_VerifiedPrincipalWins: when a real authenticator vouched
// for the principal (ScopesVerified), its scopes are the authority and the
// caller-asserted body scopes are ignored — a client CANNOT escalate by putting
// extra scopes in the body.
func TestEffectiveScopes_VerifiedPrincipalWins(t *testing.T) {
	ctx := withVerifiedPrincipal(context.Background(),
		auth.Principal{TenantID: "acme", UserID: "alice", Scopes: []string{"a", "b"}, ScopesVerified: true})

	got := effectiveScopes(ctx, []string{"a", "b", "admin"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("effectiveScopes = %v, want [a b] (verified principal, no escalation)", got)
	}
}

// TestEffectiveScopes_UnverifiedPrincipalFallsBackToBody: a principal whose
// scopes are NOT verified (NoAuth's posture) does not activate the guarantee —
// the body scopes are honored as the fallback.
func TestEffectiveScopes_UnverifiedPrincipalFallsBackToBody(t *testing.T) {
	ctx := withVerifiedPrincipal(context.Background(),
		auth.Principal{TenantID: "acme", UserID: "alice", Scopes: []string{"only-principal"}, ScopesVerified: false})

	got := effectiveScopes(ctx, []string{"x", "y"})
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Fatalf("effectiveScopes = %v, want [x y] (unverified principal -> body fallback)", got)
	}
}

// TestEffectiveScopes_NoPrincipalFallsBackToBody: with no auth middleware in the
// chain (as in unit tests that call Register directly), the body scopes are the
// fallback — behavior is unchanged from before the verified-scope switch.
func TestEffectiveScopes_NoPrincipalFallsBackToBody(t *testing.T) {
	got := effectiveScopes(context.Background(), []string{"a"})
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("effectiveScopes = %v, want [a] (no principal -> body fallback)", got)
	}
}

// agentScopeRegistry: one always-available tool plus an admin-gated tool, so a
// scope decision is observable in what the agent may execute.
func agentScopeRegistry() *tools.Registry {
	return tools.NewRegistry(
		tools.Tool{Name: "search_docs", Description: "search the kb"},
		tools.Tool{Name: "admin_delete", Description: "danger", RequiredScope: "admin"},
	)
}

// newAgentRouter mounts the chat/agent endpoint behind the given authenticator's
// bind middleware, with a scripted ToolCaller that tries to call admin_delete.
func newAgentRouter(t *testing.T, authn auth.Authenticator, exec *fakeToolCaller, executor mustRunOrNot) http.Handler {
	t.Helper()
	router := chi.NewRouter()
	api := humachi.New(router, huma.DefaultConfig("effective-scopes-test", "0.1.0"))
	api.UseMiddleware(BindAuthMiddleware(api, authn))
	RegisterAgent(api, exec, agentScopeRegistry(), executor, (*TypedToolExecutor)(nil), nil, noop.NewTracerProvider(), 3)
	return router
}

// mustRunOrNot is the executor interface the agent endpoint consumes; the tests
// pass either DemoExecutor (expects to run) or mustNotRunExecutor (expects not).
type mustRunOrNot interface {
	Execute(context.Context, string, map[string]string) (string, error)
}

func postAgent(t *testing.T, h http.Handler, tenant, user string, bodyScopes []string) (int, []AgentStep) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"prompt":     "delete everything",
		"product_id": "analytics-v1",
		"session_id": "s1",
		"scopes":     bodyScopes,
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/tenant/"+tenant+"/user/"+user+"/chat/agent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)

	var out struct {
		TenantID string      `json:"tenant_id"`
		Steps    []AgentStep `json:"steps"`
	}
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode agent body %q: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, out.Steps
}

// TestChatAgent_VerifiedScopesGateTools_NoBodyEscalation: the real-authenticator
// guarantee end-to-end. A verified principal holds only [read] but the request
// body asks for [read, admin]. The admin-gated tool must NOT be offered nor
// executed — the body scope is ignored, so mustNotRunExecutor never fires and
// admin_delete never lands in the steps.
func TestChatAgent_VerifiedScopesGateTools_NoBodyEscalation(t *testing.T) {
	authn := fakeAuth{principal: auth.Principal{TenantID: "acme", UserID: "alice", Scopes: []string{"read"}, ScopesVerified: true}}
	tc := &fakeToolCaller{
		Client: llm.NewStubClient(),
		script: []llm.ToolCallResult{
			{Calls: []llm.ToolCall{{Name: "admin_delete"}}}, // model tries the gated tool anyway
			{Message: "I cannot do that"},                   // terminal answer after the unknown-tool retry
		},
	}
	h := newAgentRouter(t, authn, tc, mustNotRunExecutor{t: t})

	code, steps := postAgent(t, h, "acme", "alice", []string{"read", "admin"})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	for _, s := range steps {
		if s.Tool == "admin_delete" {
			t.Fatalf("body scope escalated a gated tool past verified scopes: %+v", s)
		}
	}
}

// TestChatAgent_NoAuthHonorsBodyScopes: under NoAuth the guarantee is not active,
// so the body scopes are the authority. The same [read, admin] body now DOES
// grant admin_delete: it is offered and executed (DemoExecutor), landing in the
// steps. This proves the fallback path is unchanged from pre-switch behavior.
func TestChatAgent_NoAuthHonorsBodyScopes(t *testing.T) {
	tc := &fakeToolCaller{
		Client: llm.NewStubClient(),
		script: []llm.ToolCallResult{
			{Calls: []llm.ToolCall{{Name: "admin_delete"}}}, // offered under body scopes -> executes
			{Message: "done"}, // terminal answer
		},
	}
	h := newAgentRouter(t, auth.NoAuth(), tc, DemoExecutor{})

	code, steps := postAgent(t, h, "acme", "alice", []string{"read", "admin"})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	var ran bool
	for _, s := range steps {
		if s.Tool == "admin_delete" && s.Result != "" {
			ran = true
		}
	}
	if !ran {
		t.Fatalf("NoAuth should honor body scopes and execute admin_delete, got steps=%+v", steps)
	}
}
