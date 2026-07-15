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
	secret := []byte("s3cr3t")
	authn := NewJWTBearer(secret)
	tok := makeHS256(t, secret, map[string]any{
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
}

func TestJWTBearer_AlternateClaimKeys(t *testing.T) {
	secret := []byte("s3cr3t")
	authn := NewJWTBearer(secret)
	// tid/sub aliases + scopes as a JSON array.
	tok := makeHS256(t, secret, map[string]any{
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
	secret := []byte("s3cr3t")
	authn := NewJWTBearer(secret)
	tok := makeHS256(t, secret, map[string]any{
		"tenant_id": "acme", "user_id": "alice",
		"exp": time.Now().Add(-time.Minute).Unix(),
	})
	_, err := authn.Authenticate(context.Background(), bearer(tok))
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired token error = %v, want ErrUnauthenticated", err)
	}
}

func TestJWTBearer_BadSignature(t *testing.T) {
	authn := NewJWTBearer([]byte("right-secret"))
	tok := makeHS256(t, []byte("wrong-secret"), map[string]any{
		"tenant_id": "acme", "user_id": "alice",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	_, err := authn.Authenticate(context.Background(), bearer(tok))
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("bad-signature error = %v, want ErrUnauthenticated", err)
	}
}

func TestJWTBearer_MissingTenantOrUserClaim(t *testing.T) {
	secret := []byte("s3cr3t")
	authn := NewJWTBearer(secret)
	// Has user but no tenant claim.
	tok := makeHS256(t, secret, map[string]any{
		"user_id": "alice", "exp": time.Now().Add(time.Hour).Unix(),
	})
	_, err := authn.Authenticate(context.Background(), bearer(tok))
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("missing-tenant error = %v, want ErrUnauthenticated", err)
	}
}

func TestJWTBearer_MissingExpIsRejected(t *testing.T) {
	secret := []byte("s3cr3t")
	authn := NewJWTBearer(secret)
	tok := makeHS256(t, secret, map[string]any{"tenant_id": "acme", "user_id": "alice"})
	_, err := authn.Authenticate(context.Background(), bearer(tok))
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("missing-exp error = %v, want ErrUnauthenticated (fail closed)", err)
	}
}

func TestJWTBearer_UnsupportedAlg(t *testing.T) {
	secret := []byte("s3cr3t")
	authn := NewJWTBearer(secret)
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
	authn := NewJWTBearer([]byte("s3cr3t"))
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
	secret := []byte("s3cr3t")
	// Token expired 30s ago; a 1m leeway keeps it valid under a fixed clock.
	fixedNow := time.Now()
	authn := NewJWTBearer(secret, WithClock(func() time.Time { return fixedNow }), WithLeeway(time.Minute))
	tok := makeHS256(t, secret, map[string]any{
		"tenant_id": "acme", "user_id": "alice",
		"exp": fixedNow.Add(-30 * time.Second).Unix(),
	})
	if _, err := authn.Authenticate(context.Background(), bearer(tok)); err != nil {
		t.Fatalf("token within leeway rejected: %v", err)
	}
}
