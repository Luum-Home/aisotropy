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
	return jwksBodyKeys(t, map[string]*rsa.PublicKey{kid: pub})
}

// jwksBodyKeys builds a JWKS document exposing every kid->key pair (stdlib
// only). It is the multi-key form used to model a key set that gains a rotated-in
// key between fetches.
func jwksBodyKeys(t *testing.T, keys map[string]*rsa.PublicKey) []byte {
	t.Helper()
	jwks := make([]map[string]any, 0, len(keys))
	for kid, pub := range keys {
		jwks = append(jwks, map[string]any{
			"kty": "RSA",
			"kid": kid,
			"use": "sig",
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		})
	}
	b, err := json.Marshal(map[string]any{"keys": jwks})
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	return b
}

// mutableJWKS is an httptest handler backing a JWKS endpoint whose served key
// set can be swapped between requests, modelling an IdP that rotates keys. It
// counts requests so tests can assert the refetch throttle bounds network load.
// All state is guarded by mu because the handler runs in the server's goroutine
// while the test mutates/reads from its own.
type mutableJWKS struct {
	mu   sync.Mutex
	body []byte
	hits int
	// failing makes the endpoint answer 503, modelling an IdP that is
	// unreachable rather than one that rotated its keys. Requests are still
	// counted, so a test can assert that a failing endpoint is not hammered.
	failing bool
}

func (m *mutableJWKS) set(body []byte) {
	m.mu.Lock()
	m.body = body
	m.mu.Unlock()
}

func (m *mutableJWKS) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hits
}

func (m *mutableJWKS) setFailing(failing bool) {
	m.mu.Lock()
	m.failing = failing
	m.mu.Unlock()
}

func (m *mutableJWKS) serveHTTP(w http.ResponseWriter, _ *http.Request) {
	m.mu.Lock()
	m.hits++
	body := m.body
	failing := m.failing
	m.mu.Unlock()
	if failing {
		http.Error(w, "jwks unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// fakeClock is a manually-advanced clock injected via WithJWKSClock so cache TTL
// and refetch-throttle windows are exercised without real sleeps. Every
// Authenticate call runs synchronously on the test goroutine, so the clock is
// only ever touched from that single goroutine.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

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

// TestJWTBearer_JWKS_RotationSuccess proves the SUCCESS path of key rotation: a
// key rotated INTO the IdP's JWKS is picked up. The token is signed by keyB
// (kid=key-B). The endpoint initially serves only keyA (kid=key-A), so the first
// verify fails as an unknown kid. The endpoint then also serves keyB; after the
// bounded-refetch window elapses, a subsequent verify of the kid=key-B token
// succeeds — the rotated-in key was fetched and cached.
func TestJWTBearer_JWKS_RotationSuccess(t *testing.T) {
	keyA, keyB := rsaKeys(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}

	// Initially the IdP advertises only key-A.
	endpoint := &mutableJWKS{body: jwksBodyKeys(t, map[string]*rsa.PublicKey{"key-A": &keyA.PublicKey})}
	srv := httptest.NewServer(http.HandlerFunc(endpoint.serveHTTP))
	defer srv.Close()

	const minRefetch = time.Minute
	authn := NewJWTBearer(nil, WithJWKS(srv.URL,
		WithJWKSClock(clk.now),
		WithJWKSCacheTTL(5*time.Minute),
		WithJWKSMinRefetchInterval(minRefetch),
	))
	tok := makeRS256(t, keyB, "key-B", validRSClaims())

	// First verify: kid=key-B is unknown (only key-A is served). The bootstrap
	// fetch happened this same tick, so the unknown-kid refetch is throttled and
	// the request is rejected.
	if _, err := authn.Authenticate(context.Background(), bearer(tok)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("pre-rotation kid=key-B error = %v, want ErrUnauthenticated", err)
	}
	if got := endpoint.count(); got != 1 {
		t.Fatalf("fetches before rotation = %d, want 1 (bootstrap only)", got)
	}

	// The IdP rotates key-B IN (key-A stays valid during the overlap window).
	endpoint.set(jwksBodyKeys(t, map[string]*rsa.PublicKey{
		"key-A": &keyA.PublicKey,
		"key-B": &keyB.PublicKey,
	}))
	// Let the refetch-throttle window elapse (still inside the TTL, so this
	// exercises the unknown-kid refetch, not TTL expiry).
	clk.advance(minRefetch)

	// Second verify: kid=key-B is still unknown in cache, but the throttle window
	// has elapsed, so a refetch runs and now returns key-B. Verify SUCCEEDS.
	p, err := authn.Authenticate(context.Background(), bearer(tok))
	if err != nil {
		t.Fatalf("post-rotation kid=key-B rejected: %v (rotated-in key not picked up)", err)
	}
	if p.TenantID != "acme" || p.UserID != "alice" || !p.ScopesVerified {
		t.Fatalf("principal = %+v, want verified acme/alice", p)
	}
	if got := endpoint.count(); got != 2 {
		t.Fatalf("fetches after rotation = %d, want 2 (bootstrap + one refetch)", got)
	}
}

// TestJWTBearer_JWKS_TTLExpiryRefetch proves a cached key set past its TTL
// triggers a refetch that reflects the rotated set, using the injected clock. A
// key-A token verifies first; the IdP then rotates so that only key-C (keyB
// material) is served; after the TTL elapses a key-C token verifies against the
// refreshed cache.
func TestJWTBearer_JWKS_TTLExpiryRefetch(t *testing.T) {
	keyA, keyB := rsaKeys(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}

	endpoint := &mutableJWKS{body: jwksBodyKeys(t, map[string]*rsa.PublicKey{"key-A": &keyA.PublicKey})}
	srv := httptest.NewServer(http.HandlerFunc(endpoint.serveHTTP))
	defer srv.Close()

	const ttl = 5 * time.Minute
	authn := NewJWTBearer(nil, WithJWKS(srv.URL,
		WithJWKSClock(clk.now),
		WithJWKSCacheTTL(ttl),
		// A large throttle window so the unknown-kid refetch never fires here:
		// this test isolates the TTL-expiry refetch path from the throttle path.
		WithJWKSMinRefetchInterval(time.Hour),
	))

	// First verify against key-A populates the cache.
	tokA := makeRS256(t, keyA, "key-A", validRSClaims())
	if _, err := authn.Authenticate(context.Background(), bearer(tokA)); err != nil {
		t.Fatalf("initial key-A token rejected: %v", err)
	}
	if got := endpoint.count(); got != 1 {
		t.Fatalf("fetches after first verify = %d, want 1", got)
	}

	// The IdP rotates the whole set: key-A is retired, key-C (keyB) is served.
	endpoint.set(jwksBodyKeys(t, map[string]*rsa.PublicKey{"key-C": &keyB.PublicKey}))
	tokC := makeRS256(t, keyB, "key-C", validRSClaims())

	// Still inside the TTL: the cache holds only key-A, so key-C is rejected and
	// (bootstrap fetch was this tick) no refetch is attempted.
	clk.advance(ttl - time.Second)
	if _, err := authn.Authenticate(context.Background(), bearer(tokC)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("within-TTL key-C error = %v, want ErrUnauthenticated", err)
	}

	// Cross the TTL boundary: the next lookup MUST refresh the cache, which now
	// reflects the rotated set, so the key-C token verifies.
	clk.advance(2 * time.Second) // now past fetchedAt + ttl
	if _, err := authn.Authenticate(context.Background(), bearer(tokC)); err != nil {
		t.Fatalf("post-TTL key-C rejected: %v (TTL refetch did not pick up rotated set)", err)
	}
	if got := endpoint.count(); got != 2 {
		t.Fatalf("fetches = %d, want 2 (initial + one TTL refetch); a within-TTL refetch would inflate this", got)
	}
}

// TestJWTBearer_JWKS_RefetchThrottle proves a burst of unknown-kid tokens does
// NOT hammer the IdP: within the refetch window the endpoint is hit only once
// (the bootstrap fetch), no matter how many unknown-kid tokens arrive.
func TestJWTBearer_JWKS_RefetchThrottle(t *testing.T) {
	keyA, keyB := rsaKeys(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}

	// The IdP only ever serves key-A during the burst.
	endpoint := &mutableJWKS{body: jwksBodyKeys(t, map[string]*rsa.PublicKey{"key-A": &keyA.PublicKey})}
	srv := httptest.NewServer(http.HandlerFunc(endpoint.serveHTTP))
	defer srv.Close()

	const minRefetch = time.Minute
	authn := NewJWTBearer(nil, WithJWKS(srv.URL,
		WithJWKSClock(clk.now),
		WithJWKSCacheTTL(time.Hour), // large TTL so only the throttle governs refetches
		WithJWKSMinRefetchInterval(minRefetch),
	))

	// A flood of tokens with an unknown kid (signed by keyB, kid=key-B), all at
	// the same instant on the injected clock.
	tok := makeRS256(t, keyB, "key-B", validRSClaims())
	const burst = 25
	for i := 0; i < burst; i++ {
		if _, err := authn.Authenticate(context.Background(), bearer(tok)); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("burst request %d error = %v, want ErrUnauthenticated", i, err)
		}
	}
	if got := endpoint.count(); got != 1 {
		t.Fatalf("JWKS fetched %d times for a %d-token unknown-kid burst, want 1 (throttle must bound refetches)", got, burst)
	}

	// After the throttle window elapses, exactly one more refetch is permitted.
	clk.advance(minRefetch)
	if _, err := authn.Authenticate(context.Background(), bearer(tok)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("post-window unknown-kid error = %v, want ErrUnauthenticated", err)
	}
	if got := endpoint.count(); got != 2 {
		t.Fatalf("fetches after window elapsed = %d, want 2 (one refetch allowed per interval)", got)
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
