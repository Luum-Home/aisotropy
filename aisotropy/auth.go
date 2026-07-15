package aisotropy

import (
	"crypto/rsa"
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

// JWKSOption tunes the JWKS key source built by WithJWTJWKS (cache TTL, refetch
// bound, HTTP client, clock). Alias for the internal type so the public
// WithJWTJWKS signature is self-contained.
type JWKSOption = auth.JWKSOption

// WithJWTRSAPublicKey configures RS256 verification against a STATIC RSA public
// key (for tokens minted by an external IdP). Thin re-export of
// auth.WithRSAPublicKey. Using it pins the verifier to RS256: HS256 tokens are
// rejected outright, which blocks the RS256->HS256 algorithm-confusion attack.
// Parse a PEM key with ParseRSAPublicKeyPEM.
func WithJWTRSAPublicKey(pub *rsa.PublicKey) JWTOption { return auth.WithRSAPublicKey(pub) }

// WithJWTJWKS configures RS256 verification against a JWKS endpoint: the key set
// is fetched (net/http), parsed with the standard library only, cached with a
// TTL, and the token "kid" is matched to a key (bounded refetch on unknown
// kid). Thin re-export of auth.WithJWKS. A fetch failure fails CLOSED (the
// request is rejected). Using it pins the verifier to RS256.
func WithJWTJWKS(jwksURL string, opts ...JWKSOption) JWTOption {
	return auth.WithJWKS(jwksURL, opts...)
}

// WithJWTJWKSCacheTTL sets how long a fetched JWKS key set is served before the
// next lookup refreshes it. Thin re-export of auth.WithJWKSCacheTTL.
func WithJWTJWKSCacheTTL(d time.Duration) JWKSOption { return auth.WithJWKSCacheTTL(d) }

// ParseRSAPublicKeyPEM parses a PEM-encoded RSA public key (PKIX "PUBLIC KEY"
// or PKCS#1 "RSA PUBLIC KEY") into an *rsa.PublicKey for WithJWTRSAPublicKey.
// Thin re-export of auth.ParseRSAPublicKeyPEM; its errors never contain key
// bytes.
func ParseRSAPublicKeyPEM(pemBytes []byte) (*rsa.PublicKey, error) {
	return auth.ParseRSAPublicKeyPEM(pemBytes)
}

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
// "Authorization: Bearer <jwt>" credential using only the Go standard library
// (no external dependency). By default it verifies an HS256 signature against
// hmacSecret; pass WithJWTRSAPublicKey(pub) or WithJWTJWKS(url) to verify RS256
// tokens from an external IdP instead (in which case hmacSecret is unused and
// may be nil). The CONFIGURED option fixes the expected algorithm — a token
// whose header alg does not match is rejected, blocking the RS256->HS256
// confusion attack. Claims map tenant_id|tid -> TenantID, user_id|uid|sub ->
// UserID, scope|scp|scopes -> Scopes. Missing/malformed/expired/bad-signature
// tokens are rejected (401), and a valid token whose tenant/user does not match
// the URL path is rejected by the must-agree bind (403).
func NewJWTAuthenticator(hmacSecret []byte, opts ...auth.JWTOption) Authenticator {
	return auth.NewJWTBearer(hmacSecret, opts...)
}
