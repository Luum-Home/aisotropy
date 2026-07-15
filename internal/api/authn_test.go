package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"github.com/Luum-Home/aisotropy/internal/auth"
)

// authEchoInput mirrors the chat routes' path-param shape so the middleware
// sees the same tenant_id/user_id params it does in production.
type authEchoInput struct {
	TenantID string `path:"tenant_id"`
	UserID   string `path:"user_id"`
}

type authEchoOutput struct {
	Body struct {
		Tenant string   `json:"tenant"`
		User   string   `json:"user"`
		Scopes []string `json:"scopes"`
	}
}

// fakeAuth is a canned Authenticator returning a fixed principal/error.
type fakeAuth struct {
	principal auth.Principal
	err       error
}

func (f fakeAuth) Authenticate(context.Context, auth.Request) (auth.Principal, error) {
	return f.principal, f.err
}

func doEcho(t *testing.T, h http.Handler, tenant, user string) (int, authEchoOutput) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/tenant/"+tenant+"/user/"+user+"/echo", nil)
	h.ServeHTTP(rec, req)
	var out authEchoOutput
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out.Body); err != nil {
			t.Fatalf("decode echo body %q: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, out
}

func newDenyRouter(t *testing.T) http.Handler {
	t.Helper()
	router := chi.NewRouter()
	api := humachi.New(router, huma.DefaultConfig("authn-test", "0.1.0"))
	api.UseMiddleware(DenyAllMiddleware(api))
	huma.Register(api, huma.Operation{
		OperationID: "auth-echo",
		Method:      http.MethodGet,
		Path:        "/v1/tenant/{tenant_id}/user/{user_id}/echo",
	}, func(context.Context, *authEchoInput) (*authEchoOutput, error) {
		return &authEchoOutput{}, nil
	})
	return router
}

func newBindRouter(t *testing.T, authn auth.Authenticator) http.Handler {
	t.Helper()
	router := chi.NewRouter()
	api := humachi.New(router, huma.DefaultConfig("authn-test", "0.1.0"))
	api.UseMiddleware(BindAuthMiddleware(api, authn))
	huma.Register(api, huma.Operation{
		OperationID: "auth-echo",
		Method:      http.MethodGet,
		Path:        "/v1/tenant/{tenant_id}/user/{user_id}/echo",
	}, func(ctx context.Context, _ *authEchoInput) (*authEchoOutput, error) {
		out := &authEchoOutput{}
		out.Body.Tenant, _ = TenantFromContext(ctx)
		out.Body.User, _ = UserFromContext(ctx)
		if p, ok := PrincipalFromContext(ctx); ok {
			out.Body.Scopes = p.Scopes
		}
		return out, nil
	})
	return router
}

func TestDenyAllMiddleware_Rejects401(t *testing.T) {
	h := newDenyRouter(t)
	if code, _ := doEcho(t, h, "acme", "alice"); code != http.StatusUnauthorized {
		t.Fatalf("deny-all echo status = %d, want 401", code)
	}
}

func TestBindAuthMiddleware_NoAuthReachesHandlerWithInjectedIdentity(t *testing.T) {
	h := newBindRouter(t, auth.NoAuth())
	code, out := doEcho(t, h, "acme", "alice")
	if code != http.StatusOK {
		t.Fatalf("NoAuth echo status = %d, want 200", code)
	}
	if out.Body.Tenant != "acme" || out.Body.User != "alice" {
		t.Fatalf("injected identity = %s/%s, want acme/alice", out.Body.Tenant, out.Body.User)
	}
}

func TestBindAuthMiddleware_MatchingPrincipalInjectsScopes(t *testing.T) {
	authn := fakeAuth{principal: auth.Principal{TenantID: "acme", UserID: "alice", Scopes: []string{"write:tasks"}}}
	h := newBindRouter(t, authn)
	code, out := doEcho(t, h, "acme", "alice")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(out.Body.Scopes) != 1 || out.Body.Scopes[0] != "write:tasks" {
		t.Fatalf("verified scopes = %v, want [write:tasks]", out.Body.Scopes)
	}
}

func TestBindAuthMiddleware_MustAgreeBindMismatchIs403(t *testing.T) {
	// Principal authenticates as tenantA but the path asks for tenantB.
	authn := fakeAuth{principal: auth.Principal{TenantID: "tenantA", UserID: "alice"}}
	h := newBindRouter(t, authn)
	if code, _ := doEcho(t, h, "tenantB", "alice"); code != http.StatusForbidden {
		t.Fatalf("mismatched-tenant status = %d, want 403", code)
	}
	// User mismatch is equally rejected.
	authn2 := fakeAuth{principal: auth.Principal{TenantID: "acme", UserID: "alice"}}
	h2 := newBindRouter(t, authn2)
	if code, _ := doEcho(t, h2, "acme", "bob"); code != http.StatusForbidden {
		t.Fatalf("mismatched-user status = %d, want 403", code)
	}
}

func TestBindAuthMiddleware_ErrorMapping(t *testing.T) {
	unauth := newBindRouter(t, fakeAuth{err: auth.ErrUnauthenticated})
	if code, _ := doEcho(t, unauth, "acme", "alice"); code != http.StatusUnauthorized {
		t.Fatalf("ErrUnauthenticated -> status %d, want 401", code)
	}
	forbidden := newBindRouter(t, fakeAuth{err: auth.ErrForbidden})
	if code, _ := doEcho(t, forbidden, "acme", "alice"); code != http.StatusForbidden {
		t.Fatalf("ErrForbidden -> status %d, want 403", code)
	}
}
