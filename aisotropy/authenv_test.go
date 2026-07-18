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
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// pathIdentityRequest carries only path tenant/user (no credential) — enough to
// exercise NoAuth, which returns the path identity verbatim.
type pathIdentityRequest struct{ tenant, user string }

func (r pathIdentityRequest) PathTenant() string        { return r.tenant }
func (r pathIdentityRequest) PathUser() string          { return r.user }
func (r pathIdentityRequest) TLS() *tls.ConnectionState { return nil }
func (r pathIdentityRequest) Header(string) string      { return "" }

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

// TestAuthenticatorFromEnv_RS256StaticKeyFile_ReadErrorSurfaces covers the
// AISOTROPY_JWT_PUBLIC_KEY_FILE read-error branch: a path that cannot be read
// (does not exist) surfaces a wrapped error naming the OS cause, never key
// bytes, rather than a panic or a silent fallback.
func TestAuthenticatorFromEnv_RS256StaticKeyFile_ReadErrorSurfaces(t *testing.T) {
	t.Setenv("AISOTROPY_JWT_PUBLIC_KEY_FILE", filepath.Join(t.TempDir(), "does-not-exist.pem"))

	_, mode, err := AuthenticatorFromEnv()
	if err == nil {
		t.Fatal("AuthenticatorFromEnv with an unreadable AISOTROPY_JWT_PUBLIC_KEY_FILE must return an error")
	}
	if mode != "jwt-rs256" {
		t.Errorf("mode = %q, want jwt-rs256", mode)
	}
	if !strings.Contains(err.Error(), "AISOTROPY_JWT_PUBLIC_KEY_FILE") {
		t.Errorf("error = %q, want it to name AISOTROPY_JWT_PUBLIC_KEY_FILE", err.Error())
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

	t.Run("no JWT and no explicit opt-in refuses (ErrNoAuthenticator)", func(t *testing.T) {
		// The zero-config-insecure hole is closed: absent any JWT var AND absent an
		// explicit AISOTROPY_ALLOW_NOAUTH opt-in, the selector returns
		// ErrNoAuthenticator so the binary refuses to boot rather than run NoAuth.
		t.Setenv("AISOTROPY_JWT_SECRET", "")
		t.Setenv("AISOTROPY_ALLOW_NOAUTH", "")

		authr, mode, err := AuthenticatorFromEnv()
		if !errors.Is(err, ErrNoAuthenticator) {
			t.Fatalf("err = %v, want ErrNoAuthenticator", err)
		}
		if authr != nil {
			t.Error("expected a nil Authenticator when no auth is configured and NoAuth is not opted in")
		}
		if mode != "noauth" {
			t.Errorf("mode = %q, want %q (the refused-mode label)", mode, "noauth")
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

// TestAuthenticatorFromEnv_NoAuthExplicitOptIn is acceptance criterion (b): with
// NO JWT configured, an explicit AISOTROPY_ALLOW_NOAUTH opt-in — and only that —
// yields the NoAuth authenticator ("noauth"). It also proves the opt-in is strict:
// a non-boolean value ("yes") does NOT enable NoAuth (it still refuses).
func TestAuthenticatorFromEnv_NoAuthExplicitOptIn(t *testing.T) {
	for _, truthy := range []string{"1", "true", "TRUE", "t"} {
		t.Run("AISOTROPY_ALLOW_NOAUTH="+truthy+" returns noauth", func(t *testing.T) {
			t.Setenv("AISOTROPY_JWT_SECRET", "")
			t.Setenv("AISOTROPY_ALLOW_NOAUTH", truthy)

			authr, mode, err := AuthenticatorFromEnv()
			if err != nil {
				t.Fatalf("unexpected error with explicit NoAuth opt-in: %v", err)
			}
			if mode != "noauth" {
				t.Errorf("mode = %q, want %q", mode, "noauth")
			}
			if authr == nil {
				t.Fatal("expected a non-nil NoAuth Authenticator with the opt-in set")
			}
			// NoAuth trusts the path identity verbatim (must-agree tautology).
			p, aerr := authr.Authenticate(context.Background(), pathIdentityRequest{tenant: "acme", user: "alice"})
			if aerr != nil {
				t.Fatalf("NoAuth rejected a request: %v", aerr)
			}
			if p.TenantID != "acme" || p.UserID != "alice" || p.ScopesVerified {
				t.Errorf("principal = %+v, want {acme, alice, ScopesVerified:false}", p)
			}
		})
	}

	t.Run("non-boolean opt-in does NOT enable NoAuth", func(t *testing.T) {
		t.Setenv("AISOTROPY_JWT_SECRET", "")
		t.Setenv("AISOTROPY_ALLOW_NOAUTH", "yes") // not a Go bool

		_, _, err := AuthenticatorFromEnv()
		if !errors.Is(err, ErrNoAuthenticator) {
			t.Fatalf("err = %v, want ErrNoAuthenticator (a non-boolean value must not opt in)", err)
		}
	})
}

// TestAuthenticatorFromEnv_JWTIgnoresAllowNoAuth is acceptance criterion (c): when a
// JWT secret IS set, the authenticator is the real one and AISOTROPY_ALLOW_NOAUTH is
// ignored — the insecure opt-in cannot downgrade a configured authenticator.
func TestAuthenticatorFromEnv_JWTIgnoresAllowNoAuth(t *testing.T) {
	t.Setenv("AISOTROPY_JWT_SECRET", validSecret)
	t.Setenv("AISOTROPY_ALLOW_NOAUTH", "1")

	authr, mode, err := AuthenticatorFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != "jwt-hs256" {
		t.Errorf("mode = %q, want jwt-hs256 (ALLOW_NOAUTH must be ignored when a JWT is configured)", mode)
	}
	if authr == nil {
		t.Fatal("expected a live HS256 authenticator, not NoAuth")
	}
	// A real authenticator rejects an unauthenticated request (NoAuth would accept it).
	if _, aerr := authr.Authenticate(context.Background(), fakeAuthRequest{authorization: ""}); aerr == nil {
		t.Error("expected the JWT authenticator to reject a request with no bearer token")
	}
}

// TestNoAuthNonLocalGuard proves the second gate: NoAuth is permitted on loopback
// binds, refused on non-loopback binds, and permitted on non-loopback only with the
// explicit AISOTROPY_ALLOW_NOAUTH_NONLOCAL=1 override.
func TestNoAuthNonLocalGuard(t *testing.T) {
	loopback := []string{":0", "127.0.0.1:8080", "localhost:8080", "[::1]:8080", "127.0.0.1"}
	// Note: ":8080"/":0" (empty host, wildcard bind) is NOT loopback — see below.
	for _, addr := range loopback[1:] { // exclude ":0" here; it's a wildcard, tested as non-local
		t.Run("loopback "+addr+" passes", func(t *testing.T) {
			t.Setenv("AISOTROPY_ALLOW_NOAUTH_NONLOCAL", "")
			if err := NoAuthNonLocalGuard(addr); err != nil {
				t.Errorf("NoAuthNonLocalGuard(%q) = %v, want nil (loopback is safe for local dev)", addr, err)
			}
		})
	}

	nonLocal := []string{":8080", "0.0.0.0:8080", "10.0.0.5:8080", "[::]:8080", "example.com:8080"}
	for _, addr := range nonLocal {
		t.Run("non-loopback "+addr+" refuses without override", func(t *testing.T) {
			t.Setenv("AISOTROPY_ALLOW_NOAUTH_NONLOCAL", "")
			if err := NoAuthNonLocalGuard(addr); !errors.Is(err, ErrNoAuthNonLocal) {
				t.Errorf("NoAuthNonLocalGuard(%q) = %v, want ErrNoAuthNonLocal", addr, err)
			}
		})
		t.Run("non-loopback "+addr+" passes with explicit override", func(t *testing.T) {
			t.Setenv("AISOTROPY_ALLOW_NOAUTH_NONLOCAL", "1")
			if err := NoAuthNonLocalGuard(addr); err != nil {
				t.Errorf("NoAuthNonLocalGuard(%q) with override = %v, want nil", addr, err)
			}
		})
	}
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
