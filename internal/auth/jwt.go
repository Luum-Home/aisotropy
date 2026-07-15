package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// jwtBearer verifies an "Authorization: Bearer <jwt>" credential: an HS256
// signature + exp using ONLY the Go standard library (crypto/hmac,
// crypto/sha256, encoding/base64, encoding/json). It deliberately adds NO
// module dependency (no golang-jwt), so the build stays offline-green and the
// stack's 100% MIT/Apache dependency policy is untouched. RS256/JWKS is a
// documented follow-on (it needs asymmetric-key + key-rotation plumbing this
// minimal verifier does not attempt).
//
// Claim mapping: tenant_id|tid -> TenantID, user_id|uid|sub -> UserID,
// scope|scp|scopes -> Scopes (a space-delimited string or a string array).
type jwtBearer struct {
	secret []byte
	now    func() time.Time
	leeway time.Duration
}

// JWTOption configures a JWTBearer authenticator.
type JWTOption func(*jwtBearer)

// WithClock overrides the clock used to evaluate token expiry. It exists for
// deterministic tests; production uses time.Now.
func WithClock(now func() time.Time) JWTOption {
	return func(j *jwtBearer) { j.now = now }
}

// WithLeeway allows a small clock-skew tolerance when checking exp. Default 0.
func WithLeeway(d time.Duration) JWTOption {
	return func(j *jwtBearer) { j.leeway = d }
}

// NewJWTBearer builds an HS256 bearer-token Authenticator verifying against
// hmacSecret. Missing/malformed/expired/bad-signature tokens, or tokens lacking
// a tenant/user claim, are rejected with ErrUnauthenticated.
func NewJWTBearer(hmacSecret []byte, opts ...JWTOption) Authenticator {
	j := &jwtBearer{secret: hmacSecret, now: time.Now}
	for _, o := range opts {
		o(j)
	}
	return j
}

// jwtHeader is the minimal JOSE header this verifier inspects.
type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

// Authenticate parses and verifies the bearer token, returning the mapped
// Principal. Every failure path returns ErrUnauthenticated (fail closed): the
// caller cannot distinguish "no token" from "bad token", which is the intended
// posture for an auth boundary.
func (j *jwtBearer) Authenticate(_ context.Context, req Request) (Principal, error) {
	scheme, token, found := strings.Cut(strings.TrimSpace(req.Header("Authorization")), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return Principal{}, fmt.Errorf("%w: authorization header is not a bearer token", ErrUnauthenticated)
	}
	token = strings.TrimSpace(token)

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Principal{}, fmt.Errorf("%w: malformed JWT (want 3 dot-separated segments)", ErrUnauthenticated)
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Principal{}, fmt.Errorf("%w: JWT header is not valid base64url: %v", ErrUnauthenticated, err)
	}
	var header jwtHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return Principal{}, fmt.Errorf("%w: JWT header is not valid JSON: %v", ErrUnauthenticated, err)
	}
	if !strings.EqualFold(header.Alg, "HS256") {
		return Principal{}, fmt.Errorf("%w: unsupported JWT alg %q (only HS256)", ErrUnauthenticated, header.Alg)
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Principal{}, fmt.Errorf("%w: JWT signature is not valid base64url: %v", ErrUnauthenticated, err)
	}
	mac := hmac.New(sha256.New, j.secret)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(mac.Sum(nil), sig) {
		return Principal{}, fmt.Errorf("%w: JWT signature verification failed", ErrUnauthenticated)
	}

	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Principal{}, fmt.Errorf("%w: JWT claims are not valid base64url: %v", ErrUnauthenticated, err)
	}
	var claims map[string]json.RawMessage
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return Principal{}, fmt.Errorf("%w: JWT claims are not valid JSON: %v", ErrUnauthenticated, err)
	}

	// Expiry: exp is required (fail closed — a token with no expiry is rejected).
	exp, ok := expClaim(claims)
	if !ok {
		return Principal{}, fmt.Errorf("%w: JWT is missing an exp claim", ErrUnauthenticated)
	}
	if j.now().After(exp.Add(j.leeway)) {
		return Principal{}, fmt.Errorf("%w: JWT is expired", ErrUnauthenticated)
	}

	tenant := firstStringClaim(claims, "tenant_id", "tid")
	user := firstStringClaim(claims, "user_id", "uid", "sub")
	if tenant == "" || user == "" {
		return Principal{}, fmt.Errorf("%w: JWT is missing a tenant/user claim", ErrUnauthenticated)
	}

	return Principal{
		TenantID: tenant,
		UserID:   user,
		// Scopes come from verified JWT claims, so they are an authenticated
		// authorization: a handler may gate tools by them (ScopesVerified=true).
		Scopes:         scopesClaim(claims, "scope", "scp", "scopes"),
		ScopesVerified: true,
	}, nil
}

// firstStringClaim returns the first non-empty string value among keys.
func firstStringClaim(claims map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		raw, ok := claims[k]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil && s != "" {
			return s
		}
	}
	return ""
}

// scopesClaim returns the scopes from the first present key, accepting either a
// JSON array of strings or a single space-delimited string (the standard OAuth
// "scope" encoding). Returns nil when no scope claim is present.
func scopesClaim(claims map[string]json.RawMessage, keys ...string) []string {
	for _, k := range keys {
		raw, ok := claims[k]
		if !ok {
			continue
		}
		var arr []string
		if err := json.Unmarshal(raw, &arr); err == nil {
			return arr
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			if f := strings.Fields(s); len(f) > 0 {
				return f
			}
		}
	}
	return nil
}

// expClaim reads the numeric "exp" claim (seconds since the Unix epoch, per
// RFC 7519 NumericDate). Returns ok=false when absent or non-numeric.
func expClaim(claims map[string]json.RawMessage) (time.Time, bool) {
	raw, ok := claims["exp"]
	if !ok {
		return time.Time{}, false
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err != nil {
		return time.Time{}, false
	}
	return time.Unix(int64(n), 0), true
}
