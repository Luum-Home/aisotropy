package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// testSecret is a signing secret at least MinHS256SecretBytes (32) long so the
// verifier does not trip its fail-closed short-secret guard. Individual tests
// that exercise the weak-secret path use a deliberately short secret instead.
var testSecret = []byte("test-secret-at-least-32-bytes-long!!")

// makeHS256 mints a minimal HS256 JWT (header.payload.signature, base64url, no
// padding) with the given secret and claims — the same wire format the verifier
// consumes, built with only the standard library.
func makeHS256(t *testing.T, secret []byte, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal jwt segment: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signingInput := enc(map[string]any{"alg": "HS256", "typ": "JWT"}) + "." + enc(claims)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func bearer(token string) fakeRequest {
	return fakeRequest{headers: map[string]string{"Authorization": "Bearer " + token}}
}

func TestJWTBearer_ValidToken_MapsClaims(t *testing.T) {
	authn := NewJWTBearer(testSecret)
	tok := makeHS256(t, testSecret, map[string]any{
		"tenant_id": "acme",
		"user_id":   "alice",
		"scope":     "read:financial-data write:tasks",
		"exp":       time.Now().Add(time.Hour).Unix(),
	})

	p, err := authn.Authenticate(context.Background(), bearer(tok))
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if p.TenantID != "acme" || p.UserID != "alice" {
		t.Fatalf("principal = %+v, want tenant=acme user=alice", p)
	}
	if len(p.Scopes) != 2 || p.Scopes[0] != "read:financial-data" || p.Scopes[1] != "write:tasks" {
		t.Errorf("scopes = %v, want [read:financial-data write:tasks]", p.Scopes)
	}
	if !p.ScopesVerified {
		t.Error("JWTBearer must set ScopesVerified=true: JWT claims are verified authorization")
	}
}

func TestJWTBearer_AlternateClaimKeys(t *testing.T) {
	authn := NewJWTBearer(testSecret)
	// tid/sub aliases + scopes as a JSON array.
	tok := makeHS256(t, testSecret, map[string]any{
		"tid":    "globex",
		"sub":    "bob",
		"scopes": []string{"admin"},
		"exp":    time.Now().Add(time.Hour).Unix(),
	})

	p, err := authn.Authenticate(context.Background(), bearer(tok))
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if p.TenantID != "globex" || p.UserID != "bob" {
		t.Fatalf("principal = %+v, want tenant=globex user=bob", p)
	}
	if len(p.Scopes) != 1 || p.Scopes[0] != "admin" {
		t.Errorf("scopes = %v, want [admin]", p.Scopes)
	}
}

func TestJWTBearer_ExpiredToken(t *testing.T) {
	authn := NewJWTBearer(testSecret)
	tok := makeHS256(t, testSecret, map[string]any{
		"tenant_id": "acme", "user_id": "alice",
		"exp": time.Now().Add(-time.Minute).Unix(),
	})
	_, err := authn.Authenticate(context.Background(), bearer(tok))
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired token error = %v, want ErrUnauthenticated", err)
	}
}

func TestJWTBearer_BadSignature(t *testing.T) {
	authn := NewJWTBearer(testSecret)
	tok := makeHS256(t, []byte("a-different-secret-32-bytes-long!!!!"), map[string]any{
		"tenant_id": "acme", "user_id": "alice",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	_, err := authn.Authenticate(context.Background(), bearer(tok))
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("bad-signature error = %v, want ErrUnauthenticated", err)
	}
}

func TestJWTBearer_MissingTenantOrUserClaim(t *testing.T) {
	authn := NewJWTBearer(testSecret)
	// Has user but no tenant claim.
	tok := makeHS256(t, testSecret, map[string]any{
		"user_id": "alice", "exp": time.Now().Add(time.Hour).Unix(),
	})
	_, err := authn.Authenticate(context.Background(), bearer(tok))
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("missing-tenant error = %v, want ErrUnauthenticated", err)
	}
}

func TestJWTBearer_MissingExpIsRejected(t *testing.T) {
	authn := NewJWTBearer(testSecret)
	tok := makeHS256(t, testSecret, map[string]any{"tenant_id": "acme", "user_id": "alice"})
	_, err := authn.Authenticate(context.Background(), bearer(tok))
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("missing-exp error = %v, want ErrUnauthenticated (fail closed)", err)
	}
}

func TestJWTBearer_UnsupportedAlg(t *testing.T) {
	authn := NewJWTBearer(testSecret)
	// Hand-craft a token with alg=none.
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	tok := enc(map[string]any{"alg": "none", "typ": "JWT"}) + "." +
		enc(map[string]any{"tenant_id": "acme", "user_id": "alice", "exp": time.Now().Add(time.Hour).Unix()}) + "."
	_, err := authn.Authenticate(context.Background(), bearer(tok))
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("alg=none error = %v, want ErrUnauthenticated", err)
	}
}

func TestJWTBearer_MalformedAndMissingHeaders(t *testing.T) {
	authn := NewJWTBearer(testSecret)
	cases := []struct {
		name string
		req  fakeRequest
	}{
		{"no authorization header", fakeRequest{}},
		{"non-bearer scheme", fakeRequest{headers: map[string]string{"Authorization": "Basic abc"}}},
		{"empty bearer", fakeRequest{headers: map[string]string{"Authorization": "Bearer "}}},
		{"two segments", bearer("aaa.bbb")},
		{"garbage token", bearer("not-a-jwt")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := authn.Authenticate(context.Background(), tc.req)
			if !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("error = %v, want ErrUnauthenticated", err)
			}
		})
	}
}

func TestJWTBearer_WithClockLeeway(t *testing.T) {
	// Token expired 30s ago; a 1m leeway keeps it valid under a fixed clock.
	fixedNow := time.Now()
	authn := NewJWTBearer(testSecret, WithClock(func() time.Time { return fixedNow }), WithLeeway(time.Minute))
	tok := makeHS256(t, testSecret, map[string]any{
		"tenant_id": "acme", "user_id": "alice",
		"exp": fixedNow.Add(-30 * time.Second).Unix(),
	})
	if _, err := authn.Authenticate(context.Background(), bearer(tok)); err != nil {
		t.Fatalf("token within leeway rejected: %v", err)
	}
}

// fixedClock returns a WithClock option pinned to now, for deterministic
// time-sensitive assertions across the new hardening tests.
func fixedClock(now time.Time) JWTOption {
	return WithClock(func() time.Time { return now })
}

func TestValidateHS256Secret(t *testing.T) {
	cases := []struct {
		name    string
		secret  []byte
		wantErr bool
	}{
		{"empty", []byte(""), true},
		{"31 bytes (one short)", []byte("0123456789012345678901234567890"), true},
		{"exactly 32 bytes", []byte("01234567890123456789012345678901"), false},
		{"64 bytes", []byte("0123456789012345678901234567890123456789012345678901234567890123"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateHS256Secret(tc.secret)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateHS256Secret(%d bytes) = nil, want error", len(tc.secret))
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateHS256Secret(%d bytes) = %v, want nil", len(tc.secret), err)
			}
			// The error must not be wrapped in ErrUnauthenticated: it is a
			// server config fault, not a request-authentication outcome.
			if err != nil && errors.Is(err, ErrUnauthenticated) {
				t.Errorf("ValidateHS256Secret error must not wrap ErrUnauthenticated: %v", err)
			}
		})
	}
}

func TestJWTBearer_ShortSecretFailsClosed(t *testing.T) {
	// A verifier built from a sub-32-byte secret rejects even a token that is
	// correctly signed with that same weak secret.
	weak := []byte("too-short")
	if err := ValidateHS256Secret(weak); err == nil {
		t.Fatal("precondition: weak secret should fail ValidateHS256Secret")
	}
	authn := NewJWTBearer(weak)
	tok := makeHS256(t, weak, map[string]any{
		"tenant_id": "acme", "user_id": "alice",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	_, err := authn.Authenticate(context.Background(), bearer(tok))
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("short-secret verifier error = %v, want ErrUnauthenticated (fail closed)", err)
	}
}

func TestJWTBearer_Audience(t *testing.T) {
	now := time.Now()
	exp := now.Add(time.Hour).Unix()
	cases := []struct {
		name     string
		audClaim any    // value for "aud" claim; nil omits it
		want     string // configured expected audience; "" = WithAudience not applied
		ok       bool
	}{
		{"match string aud", "aisotropy", "aisotropy", true},
		{"mismatch string aud", "other-api", "aisotropy", false},
		{"missing aud when required", nil, "aisotropy", false},
		{"match within array aud", []string{"x", "aisotropy", "y"}, "aisotropy", true},
		{"mismatch array aud", []string{"x", "y"}, "aisotropy", false},
		{"aud not checked when unconfigured", "whatever", "", true},
		{"no aud claim and unconfigured", nil, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := map[string]any{"tenant_id": "acme", "user_id": "alice", "exp": exp}
			if tc.audClaim != nil {
				claims["aud"] = tc.audClaim
			}
			tok := makeHS256(t, testSecret, claims)
			opts := []JWTOption{fixedClock(now)}
			if tc.want != "" {
				opts = append(opts, WithAudience(tc.want))
			}
			authn := NewJWTBearer(testSecret, opts...)
			_, err := authn.Authenticate(context.Background(), bearer(tok))
			if tc.ok && err != nil {
				t.Fatalf("expected accept, got %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("expected ErrUnauthenticated, got %v", err)
			}
		})
	}
}

func TestJWTBearer_Issuer(t *testing.T) {
	now := time.Now()
	exp := now.Add(time.Hour).Unix()
	cases := []struct {
		name     string
		issClaim any
		want     string // "" = WithIssuer not applied
		ok       bool
	}{
		{"match", "https://issuer.example", "https://issuer.example", true},
		{"mismatch", "https://evil.example", "https://issuer.example", false},
		{"missing iss when required", nil, "https://issuer.example", false},
		{"iss not checked when unconfigured", "anything", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := map[string]any{"tenant_id": "acme", "user_id": "alice", "exp": exp}
			if tc.issClaim != nil {
				claims["iss"] = tc.issClaim
			}
			tok := makeHS256(t, testSecret, claims)
			opts := []JWTOption{fixedClock(now)}
			if tc.want != "" {
				opts = append(opts, WithIssuer(tc.want))
			}
			authn := NewJWTBearer(testSecret, opts...)
			_, err := authn.Authenticate(context.Background(), bearer(tok))
			if tc.ok && err != nil {
				t.Fatalf("expected accept, got %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("expected ErrUnauthenticated, got %v", err)
			}
		})
	}
}

func TestJWTBearer_MaxTTL(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name   string
		claims map[string]any
		maxTTL time.Duration
		ok     bool
	}{
		{
			name:   "within max ttl using iat",
			claims: map[string]any{"iat": now.Unix(), "exp": now.Add(time.Hour).Unix()},
			maxTTL: 2 * time.Hour,
			ok:     true,
		},
		{
			name:   "exceeds max ttl using iat",
			claims: map[string]any{"iat": now.Unix(), "exp": now.Add(3 * time.Hour).Unix()},
			maxTTL: 2 * time.Hour,
			ok:     false,
		},
		{
			name:   "no iat within max ttl using now",
			claims: map[string]any{"exp": now.Add(time.Hour).Unix()},
			maxTTL: 2 * time.Hour,
			ok:     true,
		},
		{
			name:   "no iat exceeds max ttl using now",
			claims: map[string]any{"exp": now.Add(3 * time.Hour).Unix()},
			maxTTL: 2 * time.Hour,
			ok:     false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := map[string]any{"tenant_id": "acme", "user_id": "alice"}
			for k, v := range tc.claims {
				claims[k] = v
			}
			tok := makeHS256(t, testSecret, claims)
			authn := NewJWTBearer(testSecret, fixedClock(now), WithMaxTTL(tc.maxTTL))
			_, err := authn.Authenticate(context.Background(), bearer(tok))
			if tc.ok && err != nil {
				t.Fatalf("expected accept, got %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("expected ErrUnauthenticated, got %v", err)
			}
		})
	}
}

func TestJWTBearer_NotBefore(t *testing.T) {
	now := time.Now()
	exp := now.Add(time.Hour).Unix()
	cases := []struct {
		name   string
		nbf    int64
		leeway time.Duration
		ok     bool
	}{
		{"nbf in the past", now.Add(-time.Minute).Unix(), 0, true},
		{"nbf now", now.Unix(), 0, true},
		{"nbf in the future rejected", now.Add(time.Minute).Unix(), 0, false},
		{"nbf in the future within leeway", now.Add(30 * time.Second).Unix(), time.Minute, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := makeHS256(t, testSecret, map[string]any{
				"tenant_id": "acme", "user_id": "alice", "exp": exp, "nbf": tc.nbf,
			})
			authn := NewJWTBearer(testSecret, fixedClock(now), WithLeeway(tc.leeway))
			_, err := authn.Authenticate(context.Background(), bearer(tok))
			if tc.ok && err != nil {
				t.Fatalf("expected accept, got %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("expected ErrUnauthenticated, got %v", err)
			}
		})
	}
}

func TestJWTBearer_IssuedAtSanity(t *testing.T) {
	now := time.Now()
	exp := now.Add(24 * time.Hour).Unix()
	cases := []struct {
		name   string
		iat    int64
		leeway time.Duration
		ok     bool
	}{
		{"iat in the past", now.Add(-time.Minute).Unix(), 0, true},
		{"iat far in the future rejected", now.Add(time.Hour).Unix(), 0, false},
		{"iat slightly ahead within leeway", now.Add(30 * time.Second).Unix(), time.Minute, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := makeHS256(t, testSecret, map[string]any{
				"tenant_id": "acme", "user_id": "alice", "exp": exp, "iat": tc.iat,
			})
			authn := NewJWTBearer(testSecret, fixedClock(now), WithLeeway(tc.leeway))
			_, err := authn.Authenticate(context.Background(), bearer(tok))
			if tc.ok && err != nil {
				t.Fatalf("expected accept, got %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("expected ErrUnauthenticated, got %v", err)
			}
		})
	}
}
