package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
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
	"testing"
	"time"
)

// This file closes specific statement-coverage gaps left by the existing
// behavioral test files (jwt_test.go, jwt_rs256_test.go), targeting exact
// branches identified via `go tool cover -func`. It reuses the shared test
// fixtures (testSecret, makeHS256, makeRS256, bearer, fixedClock, rsaKeys,
// mutableJWKS, fakeClock, jwksBodyKeys, validRSClaims) already defined in
// those files — same package, same test binary.

// rawHS256Token builds an HS256 JWT from RAW (already-encoded, possibly
// invalid) header/claims segments, computing a real HMAC signature over the
// exact wire string. This lets a test make the SIGNATURE verify while one of
// the segments is deliberately malformed (invalid base64 or invalid JSON) —
// something makeHS256 (which always encodes valid JSON) cannot produce.
func rawHS256Token(secret []byte, headerSeg, claimsSeg string) string {
	signingInput := headerSeg + "." + claimsSeg
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// --- jwt.go: Authenticate malformed-segment branches ------------------------

func TestJWTBearer_MalformedHeaderBase64(t *testing.T) {
	authn := NewJWTBearer(testSecret)
	claimsB64 := base64.RawURLEncoding.EncodeToString([]byte(`{"tenant_id":"acme","user_id":"alice","exp":9999999999}`))
	tok := rawHS256Token(testSecret, "not-valid-base64!!!", claimsB64)
	if _, err := authn.Authenticate(context.Background(), bearer(tok)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("malformed header base64 error = %v, want ErrUnauthenticated", err)
	}
}

func TestJWTBearer_HeaderValidBase64InvalidJSON(t *testing.T) {
	authn := NewJWTBearer(testSecret)
	headerB64 := base64.RawURLEncoding.EncodeToString([]byte("not a json object"))
	claimsB64 := base64.RawURLEncoding.EncodeToString([]byte(`{"tenant_id":"acme","user_id":"alice","exp":9999999999}`))
	tok := rawHS256Token(testSecret, headerB64, claimsB64)
	if _, err := authn.Authenticate(context.Background(), bearer(tok)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("header valid-base64/invalid-JSON error = %v, want ErrUnauthenticated", err)
	}
}

func TestJWTBearer_InvalidBase64Signature(t *testing.T) {
	authn := NewJWTBearer(testSecret)
	headerB64 := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claimsB64 := base64.RawURLEncoding.EncodeToString([]byte(`{"tenant_id":"acme","user_id":"alice","exp":9999999999}`))
	tok := headerB64 + "." + claimsB64 + "." + "not-valid-base64!!!"
	if _, err := authn.Authenticate(context.Background(), bearer(tok)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("malformed signature base64 error = %v, want ErrUnauthenticated", err)
	}
}

// TestJWTBearer_ClaimsInvalidBase64ButValidSignature covers the claims-base64
// decode error branch specifically — the signature must verify successfully
// (proving the failure is claims decoding, not signature mismatch) while the
// claims segment itself is not valid base64url.
func TestJWTBearer_ClaimsInvalidBase64ButValidSignature(t *testing.T) {
	authn := NewJWTBearer(testSecret)
	headerB64 := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	tok := rawHS256Token(testSecret, headerB64, "not-valid-base64!!!")
	if _, err := authn.Authenticate(context.Background(), bearer(tok)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("malformed claims base64 (valid signature) error = %v, want ErrUnauthenticated", err)
	}
}

func TestJWTBearer_ClaimsValidBase64InvalidJSON(t *testing.T) {
	authn := NewJWTBearer(testSecret)
	headerB64 := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claimsB64 := base64.RawURLEncoding.EncodeToString([]byte("not a json object"))
	tok := rawHS256Token(testSecret, headerB64, claimsB64)
	if _, err := authn.Authenticate(context.Background(), bearer(tok)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("claims valid-base64/invalid-JSON error = %v, want ErrUnauthenticated", err)
	}
}

// TestJWTBearer_NumericClaimNonNumericTreatedAsAbsent covers numericClaim's
// json.Unmarshal error branch: a non-numeric "iat" must be treated as ABSENT
// (ok=false), not rejected outright — the iat sanity check is simply skipped.
func TestJWTBearer_NumericClaimNonNumericTreatedAsAbsent(t *testing.T) {
	authn := NewJWTBearer(testSecret)
	tok := makeHS256(t, testSecret, map[string]any{
		"tenant_id": "acme", "user_id": "alice",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": "not-a-number",
	})
	if _, err := authn.Authenticate(context.Background(), bearer(tok)); err != nil {
		t.Fatalf("non-numeric iat should be treated as absent, not rejected: %v", err)
	}
}

// TestJWTBearer_AudienceNonStringNonArrayRejected covers audienceContains'
// final fail-closed `return false` — an "aud" claim that is neither a string
// nor a string array (a bare JSON number here) must be rejected when an
// audience is configured.
func TestJWTBearer_AudienceNonStringNonArrayRejected(t *testing.T) {
	now := time.Now()
	tok := makeHS256(t, testSecret, map[string]any{
		"tenant_id": "acme", "user_id": "alice",
		"exp": now.Add(time.Hour).Unix(),
		"aud": 12345,
	})
	authn := NewJWTBearer(testSecret, fixedClock(now), WithAudience("aisotropy"))
	if _, err := authn.Authenticate(context.Background(), bearer(tok)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("numeric aud claim should be rejected (neither string nor array), got %v", err)
	}
}

// --- jwt_rs256.go: verifyRS256 with no key source configured ----------------

// TestJWTBearer_RS256_NoKeySourceConfigured covers verifyRS256's `if pub ==
// nil` branch: a verifier constructed in RS256 mode with neither a static key
// nor a JWKS source must fail closed. This bypasses the public WithRSAPublicKey/
// WithJWKS constructors (both always set a key source) via a direct struct
// literal — legal white-box construction within the same package.
func TestJWTBearer_RS256_NoKeySourceConfigured(t *testing.T) {
	keyA, _ := rsaKeys(t)
	j := &jwtBearer{mode: modeRS256, now: time.Now}
	tok := makeRS256(t, keyA, "", validRSClaims())
	if _, err := j.Authenticate(context.Background(), bearer(tok)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("RS256 verifier with no key source configured error = %v, want ErrUnauthenticated", err)
	}
}

// --- jwt_rs256.go: WithJWKSHTTPClient ---------------------------------------

func TestWithJWKSHTTPClient(t *testing.T) {
	custom := &http.Client{Timeout: 42 * time.Second}
	authn := NewJWTBearer(nil, WithJWKS("http://jwks.invalid/keys", WithJWKSHTTPClient(custom)))
	j, ok := authn.(*jwtBearer)
	if !ok || j.jwks == nil {
		t.Fatalf("expected a configured JWKS source, got %T", authn)
	}
	if j.jwks.httpClient != custom {
		t.Errorf("WithJWKSHTTPClient did not override the JWKS source's HTTP client")
	}

	// A nil client must be ignored (the default client is kept).
	authn2 := NewJWTBearer(nil, WithJWKS("http://jwks.invalid/keys", WithJWKSHTTPClient(nil)))
	j2 := authn2.(*jwtBearer)
	if j2.jwks.httpClient == nil {
		t.Errorf("WithJWKSHTTPClient(nil) should leave the default client in place, got nil")
	}
}

// --- jwt_rs256.go: keyForKid refetch-on-unknown-kid failure -----------------

// TestJWTBearer_JWKS_RefetchOnUnknownKidFails covers the branch where the
// cache is already populated (bootstrap succeeded), an unknown kid triggers a
// throttle-bounded refetch, and THAT refetch itself fails — distinct from
// TestJWTBearer_JWKS_FetchFailureFailsClosed, which fails on the bootstrap
// fetch before any cache exists.
func TestJWTBearer_JWKS_RefetchOnUnknownKidFails(t *testing.T) {
	keyA, keyB := rsaKeys(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}

	endpoint := &mutableJWKS{body: jwksBodyKeys(t, map[string]*rsa.PublicKey{"key-A": &keyA.PublicKey})}
	var failAfter bool
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if failAfter {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		endpoint.serveHTTP(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	const minRefetch = time.Minute
	authn := NewJWTBearer(nil, WithJWKS(srv.URL,
		WithJWKSClock(clk.now),
		WithJWKSCacheTTL(time.Hour),
		WithJWKSMinRefetchInterval(minRefetch),
	))

	// Bootstrap: populate the cache with key-A.
	tokA := makeRS256(t, keyA, "key-A", validRSClaims())
	if _, err := authn.Authenticate(context.Background(), bearer(tokA)); err != nil {
		t.Fatalf("bootstrap key-A token rejected: %v", err)
	}

	// The endpoint now fails every request; an unknown-kid token after the
	// throttle window elapses triggers a refetch that itself errors.
	failAfter = true
	clk.advance(minRefetch)
	tokB := makeRS256(t, keyB, "key-B", validRSClaims())
	if _, err := authn.Authenticate(context.Background(), bearer(tokB)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("unknown-kid refetch failure error = %v, want ErrUnauthenticated", err)
	}
}

// --- jwt_rs256.go: fetch() transport-level branches -------------------------

// TestJWKSFetchNilContext covers fetch's http.NewRequestWithContext error
// branch: passing a nil ctx all the way down yields "nil Context".
func TestJWKSFetchNilContext(t *testing.T) {
	keyA, _ := rsaKeys(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(nil)
	}))
	defer srv.Close()

	authn := NewJWTBearer(nil, WithJWKS(srv.URL))
	tok := makeRS256(t, keyA, "key-1", validRSClaims())
	//nolint:staticcheck // deliberately passing nil to reach http.NewRequestWithContext's nil-ctx error path
	if _, err := authn.Authenticate(nil, bearer(tok)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("nil-context JWKS fetch error = %v, want ErrUnauthenticated", err)
	}
}

// TestJWKSFetchConnectionRefused covers fetch's httpClient.Do error branch: a
// closed server yields a transport-level connection error.
func TestJWKSFetchConnectionRefused(t *testing.T) {
	keyA, _ := rsaKeys(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(nil)
	}))
	url := srv.URL
	srv.Close() // now nothing listens on this port

	authn := NewJWTBearer(nil, WithJWKS(url))
	tok := makeRS256(t, keyA, "key-1", validRSClaims())
	if _, err := authn.Authenticate(context.Background(), bearer(tok)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("connection-refused JWKS fetch error = %v, want ErrUnauthenticated", err)
	}
}

// errBodyReadCloser is an io.ReadCloser whose Read always errors, used to
// drive fetch's io.ReadAll error branch via a custom RoundTripper.
type errBodyReadCloser struct{}

func (errBodyReadCloser) Read([]byte) (int, error) { return 0, errors.New("injected body read error") }
func (errBodyReadCloser) Close() error             { return nil }

type errBodyRoundTripper struct{}

func (errBodyRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       errBodyReadCloser{},
		Header:     make(http.Header),
	}, nil
}

// TestJWKSFetchBodyReadError covers fetch's io.ReadAll error branch by
// injecting a RoundTripper whose response body always fails to read.
func TestJWKSFetchBodyReadError(t *testing.T) {
	keyA, _ := rsaKeys(t)
	client := &http.Client{Transport: errBodyRoundTripper{}}
	authn := NewJWTBearer(nil, WithJWKS("http://jwks.invalid/keys", WithJWKSHTTPClient(client)))
	tok := makeRS256(t, keyA, "key-1", validRSClaims())
	if _, err := authn.Authenticate(context.Background(), bearer(tok)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("body-read-error JWKS fetch error = %v, want ErrUnauthenticated", err)
	}
}

// --- jwt_rs256.go: parseJWKS filtering + error branches ---------------------

func TestParseJWKSInvalidJSON(t *testing.T) {
	if _, err := parseJWKS([]byte("not json at all")); err == nil {
		t.Error("expected an error for invalid JWKS JSON")
	}
}

// TestParseJWKSFiltersAndSkipsUnusableKeys covers every skip branch in
// parseJWKS (non-RSA kty, encryption-scoped use, non-RS256 alg, an
// unparseable key) in one pass, and confirms exactly the one valid key
// survives.
func TestParseJWKSFiltersAndSkipsUnusableKeys(t *testing.T) {
	keyA, _ := rsaKeys(t)
	nStr := base64.RawURLEncoding.EncodeToString(keyA.PublicKey.N.Bytes())
	eStr := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(keyA.PublicKey.E)).Bytes())

	set := jwkSet{Keys: []jwk{
		{Kty: "EC", Kid: "ec-key", N: nStr, E: eStr},                              // skip: not RSA
		{Kty: "RSA", Kid: "enc-key", Use: "enc", N: nStr, E: eStr},                // skip: encryption use
		{Kty: "RSA", Kid: "es256-key", Alg: "ES256", N: nStr, E: eStr},            // skip: wrong alg
		{Kty: "RSA", Kid: "bad-key", N: "not-valid-base64!!!", E: eStr},           // skip: unparseable
		{Kty: "RSA", Kid: "good-key", Use: "sig", Alg: "RS256", N: nStr, E: eStr}, // kept
	}}
	body, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("marshal jwkSet: %v", err)
	}
	keys, err := parseJWKS(body)
	if err != nil {
		t.Fatalf("parseJWKS: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected exactly 1 usable key, got %d: %v", len(keys), keys)
	}
	if _, ok := keys["good-key"]; !ok {
		t.Errorf("expected the good-key entry to survive filtering, got %v", keys)
	}
}

func TestParseJWKSAllUnusableErrors(t *testing.T) {
	set := jwkSet{Keys: []jwk{{Kty: "EC", Kid: "x", N: "AQAB", E: "AQAB"}}}
	body, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("marshal jwkSet: %v", err)
	}
	if _, err := parseJWKS(body); err == nil {
		t.Error("expected an error when the JWKS has no usable RSA keys")
	}
}

// --- jwt_rs256.go: jwkToRSAPublicKey error branches -------------------------

func TestJwkToRSAPublicKeyErrors(t *testing.T) {
	validN := base64.RawURLEncoding.EncodeToString([]byte{1, 2, 3})
	validE := base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}) // 65537

	cases := []struct {
		name string
		k    jwk
	}{
		{"invalid n base64", jwk{N: "not-valid-base64!!!", E: validE}},
		{"invalid e base64", jwk{N: validN, E: "not-valid-base64!!!"}},
		{"empty n", jwk{N: "", E: validE}},
		{"empty e", jwk{N: validN, E: ""}},
		{"exponent out of range", jwk{N: validN, E: base64.RawURLEncoding.EncodeToString(big.NewInt(1 << 40).Bytes())}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := jwkToRSAPublicKey(tc.k); err == nil {
				t.Errorf("jwkToRSAPublicKey(%+v) expected an error, got nil", tc.k)
			}
		})
	}
}

// --- jwt_rs256.go: ParseRSAPublicKeyPEM error branches -----------------------

func TestParseRSAPublicKeyPEMErrors(t *testing.T) {
	// Invalid PKIX DER inside a "PUBLIC KEY" block.
	badPKIX := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("not valid DER bytes")})
	if _, err := ParseRSAPublicKeyPEM(badPKIX); err == nil {
		t.Error("expected an error for a PUBLIC KEY block with invalid PKIX DER")
	}

	// Valid PKIX DER, but the key is NOT RSA (ECDSA).
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate EC key: %v", err)
	}
	ecDER, err := x509.MarshalPKIXPublicKey(&ecKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal EC public key: %v", err)
	}
	ecPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: ecDER})
	if _, err := ParseRSAPublicKeyPEM(ecPEM); err == nil {
		t.Error("expected an error for a non-RSA PKIX public key")
	}

	// Invalid PKCS#1 DER inside an "RSA PUBLIC KEY" block.
	badPKCS1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: []byte("not valid DER bytes")})
	if _, err := ParseRSAPublicKeyPEM(badPKCS1); err == nil {
		t.Error("expected an error for an RSA PUBLIC KEY block with invalid PKCS#1 DER")
	}

	// Unsupported PEM block type.
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("whatever")})
	if _, err := ParseRSAPublicKeyPEM(cert); err == nil {
		t.Error("expected an error for an unsupported PEM block type")
	}
}
