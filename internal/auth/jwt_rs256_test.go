package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Shared RSA test keys, generated once (2048-bit keygen is the slow part of
// these tests). keyA is the "trusted" signer; keyB is an unrelated key used for
// the wrong-key case.
var (
	rsaKeyOnce sync.Once
	rsaKeyA    *rsa.PrivateKey
	rsaKeyB    *rsa.PrivateKey
)

func rsaKeys(t *testing.T) (a, b *rsa.PrivateKey) {
	t.Helper()
	rsaKeyOnce.Do(func() {
		var err error
		if rsaKeyA, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
		if rsaKeyB, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
	})
	return rsaKeyA, rsaKeyB
}

// makeRS256 mints an RS256 JWT signed with key, optionally carrying a kid header
// (omitted when kid == ""). It mirrors the wire format the verifier consumes,
// built with the standard library only.
func makeRS256(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal jwt segment: %v", err)
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

// jwksBody builds a JWKS document exposing pub under kid (stdlib only), matching
// the shape parseJWKS expects.
func jwksBody(t *testing.T, kid string, pub *rsa.PublicKey) []byte {
	t.Helper()
	set := map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA",
			"kid": kid,
			"use": "sig",
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}},
	}
	b, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	return b
}

func validRSClaims() map[string]any {
	return map[string]any{
		"tenant_id": "acme",
		"user_id":   "alice",
		"scope":     "read:financial-data",
		"exp":       time.Now().Add(time.Hour).Unix(),
	}
}

func TestJWTBearer_RS256_Valid(t *testing.T) {
	keyA, _ := rsaKeys(t)
	authn := NewJWTBearer(nil, WithRSAPublicKey(&keyA.PublicKey))
	tok := makeRS256(t, keyA, "", validRSClaims())

	p, err := authn.Authenticate(context.Background(), bearer(tok))
	if err != nil {
		t.Fatalf("valid RS256 token rejected: %v", err)
	}
	if p.TenantID != "acme" || p.UserID != "alice" {
		t.Fatalf("principal = %+v, want tenant=acme user=alice", p)
	}
	if !p.ScopesVerified {
		t.Error("RS256 verified claims must set ScopesVerified=true")
	}
	if len(p.Scopes) != 1 || p.Scopes[0] != "read:financial-data" {
		t.Errorf("scopes = %v, want [read:financial-data]", p.Scopes)
	}
}

func TestJWTBearer_RS256_Expired(t *testing.T) {
	keyA, _ := rsaKeys(t)
	authn := NewJWTBearer(nil, WithRSAPublicKey(&keyA.PublicKey))
	claims := validRSClaims()
	claims["exp"] = time.Now().Add(-time.Minute).Unix()
	tok := makeRS256(t, keyA, "", claims)

	_, err := authn.Authenticate(context.Background(), bearer(tok))
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired RS256 token error = %v, want ErrUnauthenticated", err)
	}
}

func TestJWTBearer_RS256_WrongKey(t *testing.T) {
	keyA, keyB := rsaKeys(t)
	// Verifier trusts keyA, but the token is signed by keyB.
	authn := NewJWTBearer(nil, WithRSAPublicKey(&keyA.PublicKey))
	tok := makeRS256(t, keyB, "", validRSClaims())

	_, err := authn.Authenticate(context.Background(), bearer(tok))
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("wrong-key RS256 token error = %v, want ErrUnauthenticated", err)
	}
}

func TestJWTBearer_RS256_Tampered(t *testing.T) {
	keyA, _ := rsaKeys(t)
	authn := NewJWTBearer(nil, WithRSAPublicKey(&keyA.PublicKey))
	tok := makeRS256(t, keyA, "", validRSClaims())

	// Re-sign header, then swap in a DIFFERENT claims segment so the signature
	// no longer matches the payload it covers.
	forged := makeRS256(t, keyA, "", map[string]any{
		"tenant_id": "acme", "user_id": "alice", "exp": time.Now().Add(time.Hour).Unix(),
	})
	// Splice: header+sig from tok, claims from forged -> signature mismatch.
	partsTok := splitJWT(t, tok)
	partsForged := splitJWT(t, forged)
	spliced := partsTok[0] + "." + partsForged[1] + "." + partsTok[2]

	_, err := authn.Authenticate(context.Background(), bearer(spliced))
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("tampered RS256 token error = %v, want ErrUnauthenticated", err)
	}
}

func splitJWT(t *testing.T, tok string) [3]string {
	t.Helper()
	var out [3]string
	i := 0
	start := 0
	for pos := 0; pos < len(tok); pos++ {
		if tok[pos] == '.' {
			if i >= 2 {
				t.Fatalf("token has more than 3 segments")
			}
			out[i] = tok[start:pos]
			i++
			start = pos + 1
		}
	}
	out[2] = tok[start:]
	return out
}

// TestJWTBearer_AlgConfusion_HS256TokenToRS256Verifier is THE core defense: an
// RS256-configured verifier must REJECT an HS256-alg token even when the
// attacker HMAC-signs it using the RSA public key bytes as the secret (the
// classic RS256->HS256 confusion). The config, not the token, decides the alg.
func TestJWTBearer_AlgConfusion_HS256TokenToRS256Verifier(t *testing.T) {
	keyA, _ := rsaKeys(t)
	authn := NewJWTBearer(nil, WithRSAPublicKey(&keyA.PublicKey))

	// The attacker uses the marshalled public key as the HMAC secret — a token
	// that WOULD verify if the server naively HMAC'd with its own public key.
	pubDER, err := x509.MarshalPKIXPublicKey(&keyA.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	forged := makeHS256(t, pubDER, validRSClaims())

	_, err = authn.Authenticate(context.Background(), bearer(forged))
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("alg-confusion (HS256 token to RS256 verifier) not rejected: err = %v, want ErrUnauthenticated", err)
	}
}

// TestJWTBearer_AlgConfusion_RS256TokenToHS256Verifier is the mirror: an
// HS256-configured verifier must reject an RS256-alg token at the alg pin,
// before any signature work.
func TestJWTBearer_AlgConfusion_RS256TokenToHS256Verifier(t *testing.T) {
	keyA, _ := rsaKeys(t)
	authn := NewJWTBearer(testSecret) // HS256 mode
	tok := makeRS256(t, keyA, "", validRSClaims())

	_, err := authn.Authenticate(context.Background(), bearer(tok))
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("alg-confusion (RS256 token to HS256 verifier) not rejected: err = %v, want ErrUnauthenticated", err)
	}
}

func TestJWTBearer_JWKS_KidMatchHit(t *testing.T) {
	keyA, _ := rsaKeys(t)
	const kid = "key-1"
	body := jwksBody(t, kid, &keyA.PublicKey)

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	authn := NewJWTBearer(nil, WithJWKS(srv.URL))
	tok := makeRS256(t, keyA, kid, validRSClaims())

	p, err := authn.Authenticate(context.Background(), bearer(tok))
	if err != nil {
		t.Fatalf("JWKS kid-match token rejected: %v", err)
	}
	if p.TenantID != "acme" || p.UserID != "alice" || !p.ScopesVerified {
		t.Fatalf("principal = %+v, want verified acme/alice", p)
	}

	// A second call within the TTL must be served from cache (no extra fetch).
	if _, err := authn.Authenticate(context.Background(), bearer(tok)); err != nil {
		t.Fatalf("second JWKS auth rejected: %v", err)
	}
	if hits != 1 {
		t.Errorf("JWKS fetched %d times, want 1 (TTL cache should serve the repeat)", hits)
	}
}

func TestJWTBearer_JWKS_UnknownKidMiss(t *testing.T) {
	keyA, _ := rsaKeys(t)
	// Server publishes only "key-1"; the token presents "key-2".
	body := jwksBody(t, "key-1", &keyA.PublicKey)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	authn := NewJWTBearer(nil, WithJWKS(srv.URL))
	tok := makeRS256(t, keyA, "key-2", validRSClaims())

	_, err := authn.Authenticate(context.Background(), bearer(tok))
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("unknown-kid token error = %v, want ErrUnauthenticated", err)
	}
}

func TestJWTBearer_JWKS_FetchFailureFailsClosed(t *testing.T) {
	keyA, _ := rsaKeys(t)
	// Server always fails: the verifier must reject, never fall open.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	authn := NewJWTBearer(nil, WithJWKS(srv.URL))
	tok := makeRS256(t, keyA, "key-1", validRSClaims())

	_, err := authn.Authenticate(context.Background(), bearer(tok))
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("JWKS fetch failure error = %v, want ErrUnauthenticated (fail closed)", err)
	}
}

func TestParseRSAPublicKeyPEM(t *testing.T) {
	keyA, _ := rsaKeys(t)

	// PKIX ("PUBLIC KEY") round-trip.
	pkixDER, err := x509.MarshalPKIXPublicKey(&keyA.PublicKey)
	if err != nil {
		t.Fatalf("marshal PKIX: %v", err)
	}
	pkixPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pkixDER})
	if pub, err := ParseRSAPublicKeyPEM(pkixPEM); err != nil {
		t.Fatalf("ParseRSAPublicKeyPEM(PKIX) error: %v", err)
	} else if pub.N.Cmp(keyA.PublicKey.N) != 0 || pub.E != keyA.PublicKey.E {
		t.Error("PKIX round-trip did not recover the same key")
	}

	// PKCS#1 ("RSA PUBLIC KEY") round-trip.
	pkcs1PEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&keyA.PublicKey)})
	if pub, err := ParseRSAPublicKeyPEM(pkcs1PEM); err != nil {
		t.Fatalf("ParseRSAPublicKeyPEM(PKCS1) error: %v", err)
	} else if pub.N.Cmp(keyA.PublicKey.N) != 0 || pub.E != keyA.PublicKey.E {
		t.Error("PKCS1 round-trip did not recover the same key")
	}

	// Garbage input is rejected with a clear error.
	if _, err := ParseRSAPublicKeyPEM([]byte("not a pem")); err == nil {
		t.Error("ParseRSAPublicKeyPEM(garbage) = nil error, want non-nil")
	}
}
