package aisotropy

import (
	"time"

	"github.com/Luum-Home/aisotropy/internal/auth"
)

// Authenticator verifies a caller's credential before any chat/knowledge
// handler runs and returns the verified Principal. It is the extension point
// that closes the primary isolation hole (path tenant/user were trusted with no
// credential): register one via WithAuthenticator. It is an alias for the real
// internal type, so a consumer's Authenticator is the exact value the runtime's
// auth middleware invokes.
//
// The runtime binds the returned Principal with MUST-AGREE semantics: the
// verified tenant/user MUST equal the URL path tenant/user (mismatch -> 403;
// missing/invalid credential -> 401). If no Authenticator is registered, the
// runtime installs a default-deny gate that rejects every /v1 request with 401
// — running without authentication is an explicit opt-in via NoAuth(), never
// the silent default.
type Authenticator = auth.Authenticator

// Principal is the verified identity an Authenticator returns: the
// authenticated TenantID/UserID the runtime binds against the URL path, plus
// the verified granted Scopes.
type Principal = auth.Principal

// JWTOption configures the JWT bearer Authenticator built by
// NewJWTAuthenticator (e.g. clock-skew leeway). Alias for the internal type so
// the public NewJWTAuthenticator signature is self-contained.
type JWTOption = auth.JWTOption

// WithJWTAudience requires the token's "aud" claim to contain aud (RFC 7519: a
// string or an array of strings). Thin re-export of auth.WithAudience so
// embedders can harden JWT verification via
// NewJWTAuthenticator(secret, WithJWTAudience("...")). Unset (default) = aud is
// not checked, so existing tokens keep verifying.
func WithJWTAudience(aud string) JWTOption { return auth.WithAudience(aud) }

// WithJWTIssuer requires the token's "iss" claim to equal iss. Thin re-export
// of auth.WithIssuer. Unset (default) = iss is not checked.
func WithJWTIssuer(iss string) JWTOption { return auth.WithIssuer(iss) }

// WithJWTMaxTTL caps the accepted token lifetime to d: a token whose
// (exp - iat) — or (exp - now) when iat is absent — exceeds d is rejected. Thin
// re-export of auth.WithMaxTTL that guards against effectively immortal tokens.
// Unset (default, 0) = the lifetime is not bounded beyond the required exp
// check.
func WithJWTMaxTTL(d time.Duration) JWTOption { return auth.WithMaxTTL(d) }

// WithAuthenticator registers the Authenticator the runtime invokes as the
// single fail-closed gate in front of every /v1 route. Register exactly one;
// a later WithAuthenticator overrides an earlier one. Without it (and without
// NoAuth), the runtime defaults to deny-all.
func WithAuthenticator(a Authenticator) Option {
	return func(app *App) { app.authenticator = a }
}

// NoAuth returns an Authenticator that performs NO verification: it trusts the
// URL path tenant/user verbatim (the pre-seam behavior), so the must-agree bind
// never rejects. This is the EXPLICIT opt-out from authentication — use it only
// for local/dev, or when a separate gateway already authenticates upstream.
// Pass it via WithAuthenticator(NoAuth()) to boot without a real credential
// check instead of hitting the default deny-all gate.
func NoAuth() Authenticator { return auth.NoAuth() }

// NewJWTAuthenticator returns an Authenticator that verifies an
// "Authorization: Bearer <jwt>" credential: an HS256 signature + expiry using
// only the Go standard library (no external dependency). Claims map
// tenant_id|tid -> TenantID, user_id|uid|sub -> UserID, scope|scp|scopes ->
// Scopes. Missing/malformed/expired/bad-signature tokens are rejected (401),
// and a valid token whose tenant/user does not match the URL path is rejected
// by the must-agree bind (403). RS256/JWKS is a documented follow-on.
func NewJWTAuthenticator(hmacSecret []byte, opts ...auth.JWTOption) Authenticator {
	return auth.NewJWTBearer(hmacSecret, opts...)
}
