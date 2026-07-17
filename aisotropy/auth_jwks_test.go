package aisotropy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestNewJWTAuthenticator_JWKSWithCacheTTL exercises the public JWKS builder
// path including WithJWTJWKSCacheTTL: an RS256 token is verified against a JWKS
// endpoint whose key set is served with an explicit cache TTL. Proving a
// kid-matched token verifies confirms the WithJWTJWKS + WithJWTJWKSCacheTTL
// re-exports thread through to a working RS256-pinned verifier (nil HMAC secret,
// since JWKS supplies the key).
func TestNewJWTAuthenticator_JWKSWithCacheTTL(t *testing.T) {
	key := envRSAPrivateKey(t)
	const kid = "jwks-ttl-key"
	set := map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
	}}}
	body, _ := json.Marshal(set)

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	authr := NewJWTAuthenticator(nil, WithJWTJWKS(srv.URL, WithJWTJWKSCacheTTL(time.Hour)))

	tok := mintRS256(t, key, kid, rsClaims())
	if _, err := authr.Authenticate(context.Background(), fakeAuthRequest{authorization: "Bearer " + tok}); err != nil {
		t.Fatalf("valid JWKS token rejected: %v", err)
	}

	// A second verification within the TTL is served from cache: the JWKS
	// endpoint is not re-fetched, proving the cache TTL option took effect.
	firstHits := hits
	if _, err := authr.Authenticate(context.Background(), fakeAuthRequest{authorization: "Bearer " + tok}); err != nil {
		t.Fatalf("second JWKS verification rejected: %v", err)
	}
	if hits != firstHits {
		t.Errorf("JWKS re-fetched within cache TTL (hits %d -> %d); WithJWTJWKSCacheTTL should have served from cache", firstHits, hits)
	}
}
