package api

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"github.com/Luum-Home/aisotropy/internal/auth"
)

// inspectingAuth reads the request through the auth.Request seam — Header (the
// Authorization header) and TLS — and echoes a principal derived from them, so
// driving a real request through the middleware exercises humaAuthRequest.Header
// and humaAuthRequest.TLS (the huma.Context-backed adapter methods).
type inspectingAuth struct {
	sawHeader string
	sawTLS    bool
}

func (a *inspectingAuth) Authenticate(_ context.Context, r auth.Request) (auth.Principal, error) {
	a.sawHeader = r.Header("Authorization")
	a.sawTLS = r.TLS() != nil
	// Bind to the path identity so the must-agree check passes.
	return auth.Principal{TenantID: r.PathTenant(), UserID: r.PathUser()}, nil
}

// TestHumaAuthRequest_HeaderAndTLS drives a request through BindAuthMiddleware
// with an authenticator that reads Header + TLS off the adapted huma.Context,
// covering humaAuthRequest.Header and humaAuthRequest.TLS (which read the
// underlying request). The request carries an Authorization header and no TLS
// (plaintext httptest), so the authenticator observes the header value and a nil
// TLS state.
func TestHumaAuthRequest_HeaderAndTLS(t *testing.T) {
	authn := &inspectingAuth{}

	router := chi.NewRouter()
	humaAPI := humachi.New(router, huma.DefaultConfig("authn-req-test", "0.1.0"))
	humaAPI.UseMiddleware(BindAuthMiddleware(humaAPI, authn))
	huma.Register(humaAPI, huma.Operation{
		OperationID: "auth-echo",
		Method:      http.MethodGet,
		Path:        "/v1/tenant/{tenant_id}/user/{user_id}/echo",
	}, func(context.Context, *authEchoInput) (*authEchoOutput, error) {
		return &authEchoOutput{}, nil
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/tenant/acme/user/alice/echo", nil)
	req.Header.Set("Authorization", "Bearer inspect-me")
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if authn.sawHeader != "Bearer inspect-me" {
		t.Errorf("authenticator saw Authorization header %q, want \"Bearer inspect-me\" (Header adapter must read the request header)", authn.sawHeader)
	}
	if authn.sawTLS {
		t.Error("plaintext httptest request should present a nil TLS state")
	}
}

// TestHumaAuthRequest_TLSPresent confirms the TLS adapter surfaces a non-nil
// connection state when the request arrived over TLS.
func TestHumaAuthRequest_TLSPresent(t *testing.T) {
	authn := &inspectingAuth{}

	router := chi.NewRouter()
	humaAPI := humachi.New(router, huma.DefaultConfig("authn-tls-test", "0.1.0"))
	humaAPI.UseMiddleware(BindAuthMiddleware(humaAPI, authn))
	huma.Register(humaAPI, huma.Operation{
		OperationID: "auth-echo",
		Method:      http.MethodGet,
		Path:        "/v1/tenant/{tenant_id}/user/{user_id}/echo",
	}, func(context.Context, *authEchoInput) (*authEchoOutput, error) {
		return &authEchoOutput{}, nil
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/tenant/acme/user/alice/echo", nil)
	req.TLS = &tls.ConnectionState{} // simulate a TLS-terminated request
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !authn.sawTLS {
		t.Error("TLS adapter returned nil for a request with a non-nil r.TLS")
	}
}
