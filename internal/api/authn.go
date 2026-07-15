package api

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Luum-Home/aisotropy/internal/auth"
	"github.com/Luum-Home/aisotropy/internal/llm"
)

// authn.go wires the authenticated-identity seam (internal/auth) into the Huma
// request pipeline as ONE API middleware, registered via humaAPI.UseMiddleware
// before the first route (see aisotropy/app.go). It is the single fail-closed
// gate in front of every /v1 route: no chat/knowledge handler runs until a
// caller's credential has been verified AND proven to agree with the URL path
// identity.
//
// Why the bind is MUST-AGREE (verified principal == path identity): it keeps
// every downstream NewScope site unchanged. Those sites read in.TenantID/
// in.UserID from the path; the bind proves those equal the verified principal,
// so the path identity the handlers already trust is now a verified fact, not a
// bare assertion. A mismatch is 403; a missing/invalid credential is 401.
//
// The health/liveness/costs/openapi/docs routes are registered directly on chi
// (or via the Huma adapter's Handle, for openapi/docs), OUTSIDE the operation
// middleware chain UseMiddleware feeds, so they stay open — this gate covers
// only the /v1 operations registered through huma.Register.

// principalCtxKey carries the verified auth.Principal (including its verified
// scopes) through context.Context, alongside tenantCtxKey/userCtxKey.
type principalCtxKey struct{}

// humaAuthRequest adapts a huma.Context to the auth.Request the Authenticator
// inspects, so internal/auth never depends on huma. tenant_id/user_id are the
// operation's path params; Header/TLS read the underlying request.
type humaAuthRequest struct{ ctx huma.Context }

func (r humaAuthRequest) PathTenant() string        { return r.ctx.Param("tenant_id") }
func (r humaAuthRequest) PathUser() string          { return r.ctx.Param("user_id") }
func (r humaAuthRequest) Header(name string) string { return r.ctx.Header(name) }
func (r humaAuthRequest) TLS() *tls.ConnectionState { return r.ctx.TLS() }

// BindAuthMiddleware returns the Huma middleware that authenticates each
// request with authn, enforces the must-agree bind, and injects the verified
// identity into ctx before the handler runs. It fails closed: on any rejection
// it writes the huma error (401/403) and does NOT call next, so no handler
// runs. api is the Huma API the error is written through (content-negotiated).
func BindAuthMiddleware(api huma.API, authn auth.Authenticator) func(huma.Context, func(huma.Context)) {
	return func(hctx huma.Context, next func(huma.Context)) {
		req := humaAuthRequest{ctx: hctx}

		principal, err := authn.Authenticate(hctx.Context(), req)
		if err != nil {
			writeAuthError(api, hctx, err)
			return
		}

		// Must-agree bind: the verified principal MUST equal the URL path
		// identity. This is what lets the downstream NewScope sites keep
		// trusting the path params — they are now proven equal to a verified
		// principal. A mismatch is an authorization failure (403).
		if principal.TenantID != req.PathTenant() || principal.UserID != req.PathUser() {
			_ = huma.WriteErr(api, hctx, http.StatusForbidden,
				"authenticated principal does not match the requested tenant/user")
			return
		}

		next(huma.WithContext(hctx, withVerifiedPrincipal(hctx.Context(), principal)))
	}
}

// DenyAllMiddleware returns the default-deny Huma middleware installed when no
// authenticator is configured (Option C default): it rejects every /v1 request
// with 401 and never calls next. The body message points the operator at the
// two explicit choices (configure an authenticator, or opt into NoAuth).
func DenyAllMiddleware(api huma.API) func(huma.Context, func(huma.Context)) {
	return func(hctx huma.Context, _ func(huma.Context)) {
		_ = huma.WriteErr(api, hctx, http.StatusUnauthorized,
			"authentication not configured: pass aisotropy.WithAuthenticator (use aisotropy.NoAuth() to explicitly disable)")
	}
}

// withVerifiedPrincipal injects the authenticated, bind-verified principal into
// ctx, reusing the SAME tenant/user carriers chatIdentity/withIdentity set (so
// handlers see identity already in ctx) plus internal/llm's tenant/user carriers
// (so OTel attribution works), and adds a principal carrier exposing the
// verified scopes. Session is intentionally not set here: it arrives in the
// body and is parsed per-route by chatIdentity, which re-derives the same
// tenant/user — now proven equal to this principal.
func withVerifiedPrincipal(ctx context.Context, p auth.Principal) context.Context {
	ctx = context.WithValue(ctx, tenantCtxKey{}, p.TenantID)
	ctx = context.WithValue(ctx, userCtxKey{}, p.UserID)
	ctx = llm.WithTenant(ctx, p.TenantID)
	ctx = llm.WithUser(ctx, p.UserID)
	ctx = context.WithValue(ctx, principalCtxKey{}, p)
	return ctx
}

// writeAuthError maps an Authenticator error to the fail-closed HTTP status:
// ErrForbidden -> 403, everything else -> 401.
func writeAuthError(api huma.API, hctx huma.Context, err error) {
	if errors.Is(err, auth.ErrForbidden) {
		_ = huma.WriteErr(api, hctx, http.StatusForbidden, "forbidden")
		return
	}
	_ = huma.WriteErr(api, hctx, http.StatusUnauthorized, "unauthenticated")
}

// PrincipalFromContext returns the verified auth.Principal the auth middleware
// injected, or (zero, false) if absent (e.g. a route reached without the
// middleware, as in unit tests that call Register directly). It exposes the
// verified scopes that effectiveScopes uses to gate tools.
func PrincipalFromContext(ctx context.Context) (auth.Principal, bool) {
	p, ok := ctx.Value(principalCtxKey{}).(auth.Principal)
	return p, ok
}

// effectiveScopes resolves the AUTHORITATIVE scopes that gate tool access for a
// request. When a real authenticator ran and vouched for the principal's scopes
// (ScopesVerified), those verified scopes are the authority — a client that
// smuggles extra scopes into the request body CANNOT escalate, because the body
// scopes are ignored entirely. Otherwise (NoAuth, or no auth middleware at all,
// as in unit tests that call Register directly) the guarantee is not active and
// the caller-asserted body scopes are honored as the fallback.
//
// This is the single place the client-asserted-vs-verified decision lives, so
// every tool-gating site (chat, chat/stream, chat/agent) resolves scopes
// identically and no handler can accidentally read in.Body.Scopes directly.
func effectiveScopes(ctx context.Context, bodyScopes []string) []string {
	if p, ok := PrincipalFromContext(ctx); ok && p.ScopesVerified {
		return p.Scopes
	}
	return bodyScopes
}
