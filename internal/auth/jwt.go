package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// jwtMode selects the signing family the verifier expects. It is fixed by the
// construction option (not the token) and drives both the header "alg" pin and
// the signature-verification path — the core of the algorithm-confusion defense.
type jwtMode int

const (
	// modeHS256 (the zero value / default) verifies a symmetric HMAC-SHA256
	// signature against a shared secret.
	modeHS256 jwtMode = iota
	// modeRS256 verifies an asymmetric RSA (PKCS#1 v1.5, SHA-256) signature
	// against either a static public key (rsaKey) or a JWKS-sourced key (jwks).
	modeRS256
)

// expectedAlg returns the single JOSE "alg" this verifier accepts, derived from
// the configured mode — never from the token. A token whose header alg differs
// is rejected before any signature check.
func (m jwtMode) expectedAlg() string {
	if m == modeRS256 {
		return "RS256"
	}
	return "HS256"
}

// jwtBearer verifies an "Authorization: Bearer <jwt>" credential using ONLY the
// Go standard library (crypto/hmac, crypto/rsa, crypto/sha256, crypto/x509,
// encoding/base64, encoding/json, math/big, net/http). It deliberately adds NO
// module dependency (no golang-jwt, no external JWK library), so the build stays
// offline-green and the stack's 100% MIT/Apache dependency policy is untouched.
//
// It supports two signing families, selected by CONFIGURATION at construction —
// never by the token:
//   - HS256 (symmetric): NewJWTBearer(secret) — the default. Verified with an
//     HMAC-SHA256 over header.claims.
//   - RS256 (asymmetric): WithRSAPublicKey(pub) for a static key, or
//     WithJWKS(url) to fetch/rotate keys from an external IdP. Verified with
//     crypto/rsa.VerifyPKCS1v15 over a SHA-256 digest of header.claims.
//
// THE ALGORITHM-CONFUSION DEFENSE (mode field): the configured mode fixes the
// single expected header "alg" (HS256 for symmetric, RS256 for asymmetric).
// Authenticate rejects any token whose header alg does not match the configured
// mode BEFORE it verifies a signature, and it dispatches verification purely on
// the mode — the token header can never steer the verifier onto the HMAC path
// while an RSA public key is configured (the classic RS256->HS256 confusion
// where an attacker HMAC-signs with the public key as the secret). The
// verification algorithm is NEVER derived from the token header.
//
// Claim mapping: tenant_id|tid -> TenantID, user_id|uid|sub -> UserID,
// scope|scp|scopes -> Scopes (a space-delimited string or a string array).
type jwtBearer struct {
	// mode fixes the single expected header alg and the signature-verification
	// path. It is set by which construction option was used (default HS256), and
	// the token header can never change it — this is the alg-confusion defense.
	mode jwtMode
	// secret is the HS256 signing secret (used only in modeHS256).
	secret []byte
	// rsaKey is the static RS256 public key (used only in modeRS256 static-key).
	rsaKey *rsa.PublicKey
	// jwks is the RS256 JWKS key source (used only in modeRS256 JWKS mode). When
	// set, the verifier resolves the token's "kid" against a TTL-cached key set
	// fetched from an external IdP. See jwt_rs256.go.
	jwks *jwksSource

	now    func() time.Time
	leeway time.Duration

	// audience, when non-empty, requires the token's "aud" claim (string or
	// string-array per RFC 7519) to contain it. Empty = not checked (back-compat).
	audience string
	// issuer, when non-empty, requires the token's "iss" claim to equal it.
	// Empty = not checked (back-compat).
	issuer string
	// maxTTL, when > 0, caps the token lifetime: (exp - iat), or (exp - now) if
	// iat is absent. Guards against absurdly long-lived tokens. 0 = not checked.
	maxTTL time.Duration
	// secretTooShort is set at construction when the HMAC secret is shorter than
	// MinHS256SecretBytes. Because NewJWTBearer returns only an Authenticator (no
	// error), a weak secret cannot be surfaced at the constructor; instead the
	// verifier fails closed — every Authenticate returns ErrUnauthenticated. The
	// boot path should call ValidateHS256Secret to fail fast before this point.
	secretTooShort bool
}

// MinHS256SecretBytes is the minimum acceptable length of an HS256 signing
// secret. HMAC-SHA256 keys shorter than the 256-bit (32-byte) output offer no
// additional security and invite brute-force/dictionary attacks, so this
// verifier refuses them (see ValidateHS256Secret and the fail-closed behavior
// of a verifier built from a short secret).
const MinHS256SecretBytes = 32

// ValidateHS256Secret reports whether secret is strong enough to sign/verify
// HS256 tokens: it returns a clear, non-nil error for a secret shorter than
// MinHS256SecretBytes (32 bytes), and nil otherwise. The boot path
// (aisotropy/authenv.go) calls it to refuse to start on a weak secret; the
// error is NOT wrapped in ErrUnauthenticated because it is a server-side
// configuration fault, not a request-authentication outcome.
func ValidateHS256Secret(secret []byte) error {
	if len(secret) < MinHS256SecretBytes {
		return fmt.Errorf("auth: HS256 secret is %d bytes; a minimum of %d bytes is required", len(secret), MinHS256SecretBytes)
	}
	return nil
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

// WithAudience requires the token's "aud" claim to contain aud (RFC 7519: aud
// is a string or an array of strings). When unset (default), aud is not checked
// so existing tokens keep verifying.
func WithAudience(aud string) JWTOption {
	return func(j *jwtBearer) { j.audience = aud }
}

// WithIssuer requires the token's "iss" claim to equal iss. When unset
// (default), iss is not checked so existing tokens keep verifying.
func WithIssuer(iss string) JWTOption {
	return func(j *jwtBearer) { j.issuer = iss }
}

// WithMaxTTL caps the accepted token lifetime to d: a token whose (exp - iat)
// exceeds d is rejected (or (exp - now) when iat is absent). It guards against
// mistakenly minted, effectively immortal tokens. When unset (default, 0), the
// lifetime is not bounded beyond the required exp check.
func WithMaxTTL(d time.Duration) JWTOption {
	return func(j *jwtBearer) { j.maxTTL = d }
}

// NewJWTBearer builds an HS256 bearer-token Authenticator verifying against
// hmacSecret. Missing/malformed/expired/bad-signature tokens, or tokens lacking
// a tenant/user claim, are rejected with ErrUnauthenticated. A secret shorter
// than MinHS256SecretBytes (32 bytes) yields a verifier that fails closed —
// every Authenticate returns ErrUnauthenticated — so a weak key can never
// authenticate a request; callers should call ValidateHS256Secret to fail fast
// at boot instead of relying on this last-line defense.
func NewJWTBearer(hmacSecret []byte, opts ...JWTOption) Authenticator {
	j := &jwtBearer{secret: hmacSecret, now: time.Now}
	for _, o := range opts {
		o(j)
	}
	// The 32-byte minimum applies to the HMAC secret only. In RS256 mode the
	// secret is unused, so a nil/empty secret there is not a weakness.
	if j.mode == modeHS256 && ValidateHS256Secret(hmacSecret) != nil {
		j.secretTooShort = true
	}
	return j
}

// verifySignature checks the token signature using the path fixed by the
// configured mode — HMAC-SHA256 for HS256, RSA PKCS#1 v1.5 for RS256. It is
// dispatched purely on j.mode (config), never on the token header, which is
// what makes the algorithm-confusion attack impossible. signingInput is the
// "header.claims" string the signature covers. Every failure returns
// ErrUnauthenticated (fail closed).
func (j *jwtBearer) verifySignature(ctx context.Context, header jwtHeader, signingInput string, sig []byte) error {
	if j.mode == modeRS256 {
		return j.verifyRS256(ctx, header, signingInput, sig)
	}
	mac := hmac.New(sha256.New, j.secret)
	_, _ = mac.Write([]byte(signingInput))
	if !hmac.Equal(mac.Sum(nil), sig) {
		return fmt.Errorf("%w: JWT signature verification failed", ErrUnauthenticated)
	}
	return nil
}

// jwtHeader is the minimal JOSE header this verifier inspects. Kid is the key
// identifier used only in RS256 JWKS mode to select the matching public key;
// it is NEVER used to choose the verification algorithm (see jwtMode).
type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

// Authenticate parses and verifies the bearer token, returning the mapped
// Principal. Every failure path returns ErrUnauthenticated (fail closed): the
// caller cannot distinguish "no token" from "bad token", which is the intended
// posture for an auth boundary.
func (j *jwtBearer) Authenticate(ctx context.Context, req Request) (Principal, error) {
	// Fail closed on a weak signing secret: a verifier built from a secret
	// shorter than MinHS256SecretBytes rejects every request rather than
	// authenticating anyone against a brute-forceable key.
	if j.secretTooShort {
		return Principal{}, fmt.Errorf("%w: HS256 signing secret is below the minimum length", ErrUnauthenticated)
	}

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
	// Algorithm-confusion defense: the CONFIGURED mode decides the expected alg,
	// never the token. Reject a mismatched header alg BEFORE verifying anything,
	// so an RS256-configured verifier can never be tricked onto the HMAC path by
	// an attacker-supplied "alg":"HS256" header (and vice versa).
	if !strings.EqualFold(header.Alg, j.mode.expectedAlg()) {
		return Principal{}, fmt.Errorf("%w: unexpected JWT alg %q (this verifier expects %s)", ErrUnauthenticated, header.Alg, j.mode.expectedAlg())
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Principal{}, fmt.Errorf("%w: JWT signature is not valid base64url: %v", ErrUnauthenticated, err)
	}
	if err := j.verifySignature(ctx, header, parts[0]+"."+parts[1], sig); err != nil {
		return Principal{}, err
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

	// Not-before (nbf): if present, the token is not yet valid when the current
	// time — extended by the clock-skew leeway — is still before nbf. This
	// mirrors the exp+leeway tolerance direction: now.Add(leeway).Before(nbf)
	// is equivalent to now.Before(nbf.Sub(leeway)).
	if nbf, ok := numericClaim(claims, "nbf"); ok {
		if j.now().Add(j.leeway).Before(nbf) {
			return Principal{}, fmt.Errorf("%w: JWT is not yet valid (nbf)", ErrUnauthenticated)
		}
	}

	// Issued-at (iat) sanity: reject a token stamped in the far future (beyond
	// leeway), which signals a forged or clock-broken issuer.
	if iat, ok := numericClaim(claims, "iat"); ok {
		if iat.After(j.now().Add(j.leeway)) {
			return Principal{}, fmt.Errorf("%w: JWT iat is in the future", ErrUnauthenticated)
		}
	}

	// Maximum lifetime: when configured, bound (exp - iat), or (exp - now) when
	// iat is absent, so an absurdly long-lived token is rejected.
	if j.maxTTL > 0 {
		if iat, ok := numericClaim(claims, "iat"); ok {
			if exp.Sub(iat) > j.maxTTL {
				return Principal{}, fmt.Errorf("%w: JWT lifetime exceeds the maximum", ErrUnauthenticated)
			}
		} else if exp.Sub(j.now()) > j.maxTTL {
			return Principal{}, fmt.Errorf("%w: JWT lifetime exceeds the maximum", ErrUnauthenticated)
		}
	}

	// Issuer (iss): when configured, the token issuer must match exactly.
	if j.issuer != "" {
		if firstStringClaim(claims, "iss") != j.issuer {
			return Principal{}, fmt.Errorf("%w: JWT issuer is not accepted", ErrUnauthenticated)
		}
	}

	// Audience (aud): when configured, the token's aud (string or string-array)
	// must contain the expected audience.
	if j.audience != "" {
		if !audienceContains(claims, j.audience) {
			return Principal{}, fmt.Errorf("%w: JWT audience is not accepted", ErrUnauthenticated)
		}
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
	return numericClaim(claims, "exp")
}

// numericClaim reads a NumericDate claim (seconds since the Unix epoch, per RFC
// 7519) — exp, nbf, iat. Returns ok=false when the claim is absent or not a
// JSON number.
func numericClaim(claims map[string]json.RawMessage, key string) (time.Time, bool) {
	raw, ok := claims[key]
	if !ok {
		return time.Time{}, false
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err != nil {
		return time.Time{}, false
	}
	return time.Unix(int64(n), 0), true
}

// audienceContains reports whether the "aud" claim contains want. Per RFC 7519,
// aud is either a single string or an array of strings; both encodings are
// accepted. A missing or malformed aud claim returns false (fail closed).
func audienceContains(claims map[string]json.RawMessage, want string) bool {
	raw, ok := claims["aud"]
	if !ok {
		return false
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		for _, a := range arr {
			if a == want {
				return true
			}
		}
		return false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s == want
	}
	return false
}
