package aisotropy

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
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

// envRSAKey is a shared 2048-bit test key (keygen is the slow part).
var (
	envRSAOnce sync.Once
	envRSAKey  *rsa.PrivateKey
)

func envRSAPrivateKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	envRSAOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		envRSAKey = k
	})
	return envRSAKey
}

// mintRS256 signs claims into an RS256 JWT with the stdlib only, so env tests
// can prove the RS256 verifier selected from the environment actually verifies.
func mintRS256(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	header := map[string]any{"alg": "RS256", "typ": "JWT"}
	if kid != "" {
		header["kid"] = kid
	}
	signingInput := enc(header) + "." + enc(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign RS256: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func publicKeyPEM(t *testing.T, pub *rsa.PublicKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal PKIX: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

func rsClaims() map[string]any {
	now := time.Now()
	return map[string]any{
		"tenant_id": "acme",
		"user_id":   "alice",
		"iat":       now.Unix(),
		"exp":       now.Add(30 * time.Minute).Unix(),
	}
}

// TestAuthenticatorFromEnv_RS256StaticKey proves AISOTROPY_JWT_PUBLIC_KEY selects
// the RS256 static-key mode ("jwt-rs256") and the resulting authenticator
// verifies a token signed by the matching private key.
func TestAuthenticatorFromEnv_RS256StaticKey(t *testing.T) {
	key := envRSAPrivateKey(t)
	t.Setenv("AISOTROPY_JWT_PUBLIC_KEY", string(publicKeyPEM(t, &key.PublicKey)))

	authr, mode, err := AuthenticatorFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != "jwt-rs256" {
		t.Fatalf("mode = %q, want jwt-rs256", mode)
	}
	if authr == nil {
		t.Fatal("nil Authenticator with a public key set")
	}

	tok := mintRS256(t, key, "", rsClaims())
	p, aerr := authr.Authenticate(context.Background(), fakeAuthRequest{authorization: "Bearer " + tok})
	if aerr != nil {
		t.Fatalf("valid RS256 token rejected: %v", aerr)
	}
	if p.TenantID != "acme" || p.UserID != "alice" {
		t.Errorf("principal = {%q,%q}, want {acme,alice}", p.TenantID, p.UserID)
	}
}

// TestAuthenticatorFromEnv_RS256StaticKeyFile proves the _FILE variant reads the
// PEM from disk and selects the same mode.
func TestAuthenticatorFromEnv_RS256StaticKeyFile(t *testing.T) {
	key := envRSAPrivateKey(t)
	path := filepath.Join(t.TempDir(), "pub.pem")
	if err := os.WriteFile(path, publicKeyPEM(t, &key.PublicKey), 0o600); err != nil {
		t.Fatalf("write pem: %v", err)
	}
	t.Setenv("AISOTROPY_JWT_PUBLIC_KEY_FILE", path)

	authr, mode, err := AuthenticatorFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != "jwt-rs256" || authr == nil {
		t.Fatalf("mode = %q, authr nil = %v; want jwt-rs256 with a live authenticator", mode, authr == nil)
	}
	tok := mintRS256(t, key, "", rsClaims())
	if _, aerr := authr.Authenticate(context.Background(), fakeAuthRequest{authorization: "Bearer " + tok}); aerr != nil {
		t.Fatalf("valid RS256 token rejected: %v", aerr)
	}
}

// TestAuthenticatorFromEnv_RS256JWKS proves AISOTROPY_JWT_JWKS_URL selects the
// JWKS mode ("jwt-rs256-jwks") and verifies a kid-matched token end to end.
func TestAuthenticatorFromEnv_RS256JWKS(t *testing.T) {
	key := envRSAPrivateKey(t)
	const kid = "env-key"
	set := map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
	}}}
	body, _ := json.Marshal(set)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	t.Setenv("AISOTROPY_JWT_JWKS_URL", srv.URL)

	authr, mode, err := AuthenticatorFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != "jwt-rs256-jwks" || authr == nil {
		t.Fatalf("mode = %q, authr nil = %v; want jwt-rs256-jwks with a live authenticator", mode, authr == nil)
	}
	tok := mintRS256(t, key, kid, rsClaims())
	if _, aerr := authr.Authenticate(context.Background(), fakeAuthRequest{authorization: "Bearer " + tok}); aerr != nil {
		t.Fatalf("valid JWKS token rejected: %v", aerr)
	}
}

// TestAuthenticatorFromEnv_AmbiguousModes proves that configuring more than one
// signing mode is a fatal boot error, never a silent precedence pick.
func TestAuthenticatorFromEnv_AmbiguousModes(t *testing.T) {
	key := envRSAPrivateKey(t)
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"hs256 + rs256 static", map[string]string{
			"AISOTROPY_JWT_SECRET":     validSecret,
			"AISOTROPY_JWT_PUBLIC_KEY": string(publicKeyPEM(t, &key.PublicKey)),
		}},
		{"hs256 + jwks", map[string]string{
			"AISOTROPY_JWT_SECRET":   validSecret,
			"AISOTROPY_JWT_JWKS_URL": "https://issuer.example/jwks",
		}},
		{"rs256 static + jwks", map[string]string{
			"AISOTROPY_JWT_PUBLIC_KEY": string(publicKeyPEM(t, &key.PublicKey)),
			"AISOTROPY_JWT_JWKS_URL":   "https://issuer.example/jwks",
		}},
		{"public key literal + file", map[string]string{
			"AISOTROPY_JWT_PUBLIC_KEY":      string(publicKeyPEM(t, &key.PublicKey)),
			"AISOTROPY_JWT_PUBLIC_KEY_FILE": "/some/path.pem",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			authr, _, err := AuthenticatorFromEnv()
			if err == nil {
				t.Fatal("expected an ambiguous-configuration error; the binary must not boot")
			}
			if authr != nil {
				t.Error("expected a nil Authenticator on ambiguous configuration")
			}
		})
	}
}

// TestAuthenticatorFromEnv_InvalidPublicKey proves a malformed PEM fails fast.
func TestAuthenticatorFromEnv_InvalidPublicKey(t *testing.T) {
	t.Setenv("AISOTROPY_JWT_PUBLIC_KEY", "-----BEGIN PUBLIC KEY-----\nnot-base64\n-----END PUBLIC KEY-----")
	authr, _, err := AuthenticatorFromEnv()
	if err == nil {
		t.Fatal("expected an error for a malformed RSA public key")
	}
	if authr != nil {
		t.Error("expected a nil Authenticator when the public key is rejected")
	}
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
