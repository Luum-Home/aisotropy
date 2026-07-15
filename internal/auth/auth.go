// Package auth defines the authenticated-identity seam for the Aisotropy
// runtime: the contract a caller's credential is verified against before any
// chat/knowledge handler runs.
//
// Motivation (the primary isolation hole this closes): historically tenant_id
// and user_id were TRUSTED, regex-validated path params — any caller could
// address any tenant/user with no credential and be served (see the package
// doc of internal/api/chat.go and the regression anchor in
// aisotropy/isolation_e2e_test.go). This package introduces an Authenticator
// invoked by ONE Huma API middleware (internal/api/authn.go) that verifies the
// caller and proves the verified principal MUST AGREE with the URL path
// identity, so the ~8 NewScope sites downstream keep reading in.TenantID/
// in.UserID unchanged — now proven equal to a verified principal.
//
// Design: the interface + value types live here (importable by the runtime and
// re-exported from package aisotropy); the middleware that binds them to Huma
// lives in internal/api (it reuses that package's ctx carriers). Reference
// implementations: NoAuth (today's behavior, named + opt-in) and JWTBearer
// (stdlib-only HS256, no external dependency, so the build stays offline-green
// and dependency-clean — see jwt.go).
package auth

import (
	"context"
	"crypto/tls"
	"errors"
)

// ErrUnauthenticated signals a missing, malformed, expired, or otherwise
// unverifiable credential. The middleware maps it to HTTP 401. It is the
// fail-closed default: any authenticator error that is not explicitly
// ErrForbidden is treated as unauthenticated.
var ErrUnauthenticated = errors.New("auth: unauthenticated")

// ErrForbidden signals a credential that verified but is not permitted to act
// as the requested identity (the must-agree bind rejects it). The middleware
// maps it to HTTP 403.
var ErrForbidden = errors.New("auth: forbidden")

// Principal is the verified identity an Authenticator returns. TenantID/UserID
// are the authenticated identity the middleware binds (must-agree) against the
// URL path; Scopes are the verified granted permissions, exposed via ctx for
// later tool-gating (the switch of tool-gating from client-asserted to verified
// scopes is a documented follow-on — see internal/api/authn.go).
type Principal struct {
	TenantID string
	UserID   string
	Scopes   []string
}

// Request exposes the request attributes an Authenticator inspects. It is a
// narrow adapter over the underlying HTTP/Huma request so an Authenticator
// never depends on huma or net/http directly, and so it can be faked in tests.
type Request interface {
	// PathTenant returns the tenant_id path parameter (the requested tenant).
	PathTenant() string
	// PathUser returns the user_id path parameter (the requested user).
	PathUser() string
	// Header returns the named request header value, or "" if absent.
	Header(name string) string
	// TLS returns the connection's TLS state, or nil for a plaintext request
	// (available for authenticators that verify client certificates).
	TLS() *tls.ConnectionState
}

// Authenticator verifies a request and returns the verified Principal. A
// non-nil error rejects the request: ErrForbidden maps to 403, anything else
// (including ErrUnauthenticated) maps to 401 (fail closed).
type Authenticator interface {
	Authenticate(ctx context.Context, req Request) (Principal, error)
}

// noAuth is the reference "no verification" Authenticator: it trusts the path
// identity verbatim, reproducing the pre-seam behavior — but NAMED and opt-in,
// so running without authentication is now an explicit deployment choice, never
// the silent default (the default is deny-all; see package aisotropy's Run).
type noAuth struct{}

// NoAuth returns an Authenticator that performs NO verification: the returned
// Principal is exactly the URL path tenant/user, so the must-agree bind is a
// tautology that never rejects. This is the explicit opt-out from
// authentication (aisotropy.NoAuth()); use it only for local/dev or when a
// separate gateway already authenticates upstream.
func NoAuth() Authenticator { return noAuth{} }

// Authenticate returns the path identity unverified.
func (noAuth) Authenticate(_ context.Context, req Request) (Principal, error) {
	return Principal{TenantID: req.PathTenant(), UserID: req.PathUser()}, nil
}
