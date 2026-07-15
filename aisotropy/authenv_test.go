package aisotropy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// validSecret is a >= 32-byte HS256 signing secret used across the tests below;
// it never appears in any assertion on the returned mode label.
const validSecret = "this-is-a-32-byte-minimum-secret!" // 33 bytes

// fakeAuthRequest is a minimal auth.Request carrying only an Authorization
// header — enough to exercise the JWT bearer authenticator.
type fakeAuthRequest struct{ authorization string }

func (r fakeAuthRequest) PathTenant() string { return "" }
func (r fakeAuthRequest) PathUser() string   { return "" }
func (r fakeAuthRequest) TLS() *tls.ConnectionState {
	return nil
}
func (r fakeAuthRequest) Header(name string) string {
	if name == "Authorization" {
		return r.authorization
	}
	return ""
}

// mintHS256 signs claims into an HS256 JWT with the stdlib only, mirroring the
// verifier in internal/auth/jwt.go, so tests can prove options are applied.
func mintHS256(t *testing.T, secret []byte, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	header := enc(map[string]string{"alg": "HS256", "typ": "JWT"})
	body := enc(claims)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(header + "." + body))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return header + "." + body + "." + sig
}

// TestAuthenticatorFromEnv is the RESIDUAL #1 acceptance criterion: the shipped
// binary is securable by config. With AISOTROPY_JWT_SECRET set the selector
// returns a real HS256 JWT authenticator ("jwt-hs256"); unset it returns the
// explicit insecure NoAuth opt-out ("noauth"). The secret value is never part
// of the returned mode label.
func TestAuthenticatorFromEnv(t *testing.T) {
	t.Run("valid secret set enables jwt-hs256", func(t *testing.T) {
		t.Setenv("AISOTROPY_JWT_SECRET", validSecret)

		authr, mode, err := AuthenticatorFromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mode != "jwt-hs256" {
			t.Errorf("mode = %q, want %q", mode, "jwt-hs256")
		}
		if authr == nil {
			t.Fatal("AuthenticatorFromEnv() returned a nil Authenticator with a secret set")
		}
		if mode == validSecret {
			t.Error("mode label leaked the secret value")
		}
	})

	t.Run("secret unset defaults to noauth", func(t *testing.T) {
		t.Setenv("AISOTROPY_JWT_SECRET", "")

		authr, mode, err := AuthenticatorFromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mode != "noauth" {
			t.Errorf("mode = %q, want %q", mode, "noauth")
		}
		if authr == nil {
			t.Fatal("AuthenticatorFromEnv() returned a nil Authenticator when unset")
		}
	})

	t.Run("short secret fails fast with error", func(t *testing.T) {
		t.Setenv("AISOTROPY_JWT_SECRET", "too-short") // < 32 bytes

		authr, _, err := AuthenticatorFromEnv()
		if err == nil {
			t.Fatal("expected a non-nil error for a sub-minimum HS256 secret; the binary must not boot")
		}
		if authr != nil {
			t.Error("expected a nil Authenticator when the secret is rejected")
		}
	})

	t.Run("malformed max-ttl fails fast with error", func(t *testing.T) {
		t.Setenv("AISOTROPY_JWT_SECRET", validSecret)
		t.Setenv("AISOTROPY_JWT_MAX_TTL", "not-a-duration")

		authr, _, err := AuthenticatorFromEnv()
		if err == nil {
			t.Fatal("expected a non-nil error for a malformed AISOTROPY_JWT_MAX_TTL")
		}
		if authr != nil {
			t.Error("expected a nil Authenticator when max-ttl is rejected")
		}
	})
}

// TestAuthenticatorFromEnv_OptionsApplied proves the aud/iss/max-ttl env vars
// are wired into the JWT verifier: a token that satisfies all three passes,
// while a token that violates any one is rejected. If the options were not
// applied, the mismatched tokens would incorrectly pass.
func TestAuthenticatorFromEnv_OptionsApplied(t *testing.T) {
	t.Setenv("AISOTROPY_JWT_SECRET", validSecret)
	t.Setenv("AISOTROPY_JWT_AUDIENCE", "aisotropy-api")
	t.Setenv("AISOTROPY_JWT_ISSUER", "trusted-issuer")
	t.Setenv("AISOTROPY_JWT_MAX_TTL", "1h")

	authr, mode, err := AuthenticatorFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != "jwt-hs256" || authr == nil {
		t.Fatalf("mode = %q, authr nil = %v; want jwt-hs256 with a live authenticator", mode, authr == nil)
	}

	secret := []byte(validSecret)
	now := time.Now()
	base := func() map[string]any {
		return map[string]any{
			"tenant_id": "acme",
			"user_id":   "alice",
			"iss":       "trusted-issuer",
			"aud":       "aisotropy-api",
			"iat":       now.Unix(),
			"exp":       now.Add(30 * time.Minute).Unix(),
		}
	}
	authenticate := func(claims map[string]any) error {
		tok := mintHS256(t, secret, claims)
		_, aerr := authr.Authenticate(context.Background(), fakeAuthRequest{authorization: "Bearer " + tok})
		return aerr
	}

	t.Run("all constraints satisfied passes", func(t *testing.T) {
		p, aerr := authr.Authenticate(context.Background(),
			fakeAuthRequest{authorization: "Bearer " + mintHS256(t, secret, base())})
		if aerr != nil {
			t.Fatalf("expected a valid token to pass, got: %v", aerr)
		}
		if p.TenantID != "acme" || p.UserID != "alice" {
			t.Errorf("principal = {%q,%q}, want {acme,alice}", p.TenantID, p.UserID)
		}
	})

	t.Run("wrong audience rejected (WithJWTAudience applied)", func(t *testing.T) {
		c := base()
		c["aud"] = "some-other-api"
		if aerr := authenticate(c); aerr == nil {
			t.Error("expected rejection for a mismatched audience")
		}
	})

	t.Run("wrong issuer rejected (WithJWTIssuer applied)", func(t *testing.T) {
		c := base()
		c["iss"] = "rogue-issuer"
		if aerr := authenticate(c); aerr == nil {
			t.Error("expected rejection for a mismatched issuer")
		}
	})

	t.Run("over-long lifetime rejected (WithJWTMaxTTL applied)", func(t *testing.T) {
		c := base()
		c["exp"] = now.Add(2 * time.Hour).Unix() // exceeds the 1h max-ttl
		if aerr := authenticate(c); aerr == nil {
			t.Error("expected rejection for a token whose lifetime exceeds the max-ttl")
		}
	})
}
