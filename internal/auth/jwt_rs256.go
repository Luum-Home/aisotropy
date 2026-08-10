package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RS256 asymmetric verification, stdlib only. Two sub-modes share modeRS256:
//   - static key: WithRSAPublicKey(pub) verifies against one fixed public key.
//   - JWKS: WithJWKS(url) fetches a key set from an external IdP, caches it with
//     a TTL, and resolves the token "kid" to the matching key (with a bounded
//     refetch on an unknown kid so a rotated-in key is picked up).
//
// The verification algorithm is fixed by modeRS256 and is NEVER derived from the
// token header — the alg pin in jwt.go rejects any non-RS256 token before this
// code runs, which is what blocks the RS256->HS256 confusion attack.

// WithRSAPublicKey configures RS256 verification against a static RSA public
// key. Using it switches the verifier to modeRS256, so it will accept ONLY
// tokens whose header alg is RS256 and reject HS256 tokens outright (no HMAC
// path exists once an RSA key is configured). Parse a PEM-encoded key with
// ParseRSAPublicKeyPEM.
func WithRSAPublicKey(pub *rsa.PublicKey) JWTOption {
	return func(j *jwtBearer) {
		j.mode = modeRS256
		j.rsaKey = pub
	}
}

// WithJWKS configures RS256 verification against a JWKS endpoint (a JSON Web Key
// Set served by an external IdP). Using it switches the verifier to modeRS256.
// The key set is fetched with net/http, parsed with the standard library only
// (RSA jwk "n"/"e" base64url-decoded via math/big — no external JWK library),
// cached for a TTL, and refetched (bounded) when a token presents an unknown
// kid. A fetch failure with no usable cached key fails CLOSED: the request is
// rejected, never allowed through.
func WithJWKS(jwksURL string, opts ...JWKSOption) JWTOption {
	return func(j *jwtBearer) {
		j.mode = modeRS256
		j.jwks = newJWKSSource(jwksURL, opts...)
	}
}

// ParseRSAPublicKeyPEM parses a PEM-encoded RSA public key into an
// *rsa.PublicKey, accepting both PKIX ("PUBLIC KEY") and PKCS#1 ("RSA PUBLIC
// KEY") block types. It returns a clear error (never containing key bytes) when
// the input is not a single RSA public-key PEM block. Use it to build the
// argument to WithRSAPublicKey.
func ParseRSAPublicKeyPEM(pemBytes []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("auth: no PEM block found in RSA public key input")
	}
	switch block.Type {
	case "PUBLIC KEY":
		pub, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("auth: parsing PKIX public key: %w", err)
		}
		rsaPub, ok := pub.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("auth: PEM public key is not RSA (%T)", pub)
		}
		return rsaPub, nil
	case "RSA PUBLIC KEY":
		rsaPub, err := x509.ParsePKCS1PublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("auth: parsing PKCS#1 RSA public key: %w", err)
		}
		return rsaPub, nil
	default:
		return nil, fmt.Errorf("auth: unsupported PEM block type %q (want PUBLIC KEY or RSA PUBLIC KEY)", block.Type)
	}
}

// verifyRS256 resolves the RSA public key (static or JWKS-by-kid) and verifies
// the PKCS#1 v1.5 signature over a SHA-256 digest of signingInput. Every failure
// returns ErrUnauthenticated (fail closed).
func (j *jwtBearer) verifyRS256(ctx context.Context, header jwtHeader, signingInput string, sig []byte) error {
	pub := j.rsaKey
	if j.jwks != nil {
		k, err := j.jwks.keyForKid(ctx, header.Kid)
		if err != nil {
			return err // already wrapped ErrUnauthenticated; fail closed
		}
		pub = k
	}
	if pub == nil {
		return fmt.Errorf("%w: no RSA public key configured for RS256 verification", ErrUnauthenticated)
	}
	digest := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		return fmt.Errorf("%w: JWT signature verification failed", ErrUnauthenticated)
	}
	return nil
}

// Default JWKS cache tuning. maxJWKSResponseBytes bounds the response read so a
// hostile or misconfigured endpoint cannot exhaust memory.
const (
	defaultJWKSCacheTTL         = 5 * time.Minute
	defaultJWKSMinRefetchWindow = 1 * time.Minute
	maxJWKSResponseBytes        = 1 << 20 // 1 MiB
	// defaultJWKSMaxStaleness bounds how far PAST the TTL a cached key set may
	// still be served while refreshes keep failing. It is the answer to a
	// question with two bad extremes: fail closed the instant a refresh fails and
	// one blip at the IdP takes down every authenticated request; serve the
	// cached set with no bound and a key rotated out — possibly BECAUSE it was
	// compromised — stays valid for as long as the endpoint stays broken, which
	// can be forever. 30 minutes rides out a real outage and caps the window in
	// which a withdrawn key still verifies at ttl+maxStale (35 minutes on the
	// defaults). Tune with WithJWKSMaxStaleness.
	defaultJWKSMaxStaleness = 30 * time.Minute
)

// JWKSOption tunes the JWKS key source built by WithJWKS.
type JWKSOption func(*jwksSource)

// WithJWKSCacheTTL sets how long a fetched key set is served before it is
// refreshed on the next lookup. Non-positive values are ignored (the default is
// kept).
func WithJWKSCacheTTL(d time.Duration) JWKSOption {
	return func(s *jwksSource) {
		if d > 0 {
			s.ttl = d
		}
	}
}

// WithJWKSMinRefetchInterval bounds how often an unknown-kid lookup may trigger
// a network refetch, so a flood of unknown-kid tokens cannot hammer the JWKS
// endpoint. Non-positive values are ignored (the default is kept).
func WithJWKSMinRefetchInterval(d time.Duration) JWKSOption {
	return func(s *jwksSource) {
		if d > 0 {
			s.minRefetch = d
		}
	}
}

// WithJWKSMaxStaleness bounds how long past the cache TTL a key set may still be
// served while refreshes fail. Once fetchedAt is older than ttl+maxStaleness the
// cache is dropped and lookups reject, so an IdP that stays unreachable cannot
// keep a rotated-out key verifying indefinitely. Non-positive values are ignored
// (the default is kept); there is deliberately no "unbounded" setting.
func WithJWKSMaxStaleness(d time.Duration) JWKSOption {
	return func(s *jwksSource) {
		if d > 0 {
			s.maxStale = d
		}
	}
}

// WithJWKSHTTPClient overrides the HTTP client used to fetch the key set (e.g.
// to set a custom timeout or transport). A nil client is ignored.
func WithJWKSHTTPClient(c *http.Client) JWKSOption {
	return func(s *jwksSource) {
		if c != nil {
			s.httpClient = c
		}
	}
}

// WithJWKSClock overrides the clock used for cache TTL/refetch bookkeeping. It
// exists for deterministic tests; production uses time.Now.
func WithJWKSClock(now func() time.Time) JWKSOption {
	return func(s *jwksSource) {
		if now != nil {
			s.now = now
		}
	}
}

// jwksSource is a TTL-cached, stdlib-only JWKS key set keyed by kid. All state
// is guarded by mu so concurrent Authenticate calls are safe.
type jwksSource struct {
	url        string
	httpClient *http.Client
	ttl        time.Duration
	minRefetch time.Duration
	maxStale   time.Duration
	now        func() time.Time

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey // kid -> public key; nil until first fetch
	fetchedAt time.Time                 // when keys was last successfully populated
	lastFetch time.Time                 // when a network fetch was last attempted
	// lastFailure is when a fetch last FAILED, zero once one succeeds. It gates
	// the retry throttle on the TTL path, which must not be gated by lastFetch:
	// that would delay picking up a rotated key set from a perfectly healthy
	// endpoint.
	lastFailure time.Time
}

func newJWKSSource(url string, opts ...JWKSOption) *jwksSource {
	s := &jwksSource{
		url:        url,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		ttl:        defaultJWKSCacheTTL,
		minRefetch: defaultJWKSMinRefetchWindow,
		maxStale:   defaultJWKSMaxStaleness,
		now:        time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// keyForKid returns the cached RSA key for kid, refreshing the cache when it is
// empty or past its TTL and refetching once (bounded by minRefetch) on an
// unknown kid. It fails closed: a fetch failure with no usable cache, or a kid
// that no fetched key matches, returns ErrUnauthenticated.
func (s *jwksSource) keyForKid(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if s.keys == nil || now.Sub(s.fetchedAt) >= s.ttl {
		// Do not storm an endpoint that is already failing: when the PREVIOUS
		// attempt failed and a usable cache is still in hand, wait out minRefetch
		// before trying again.
		//
		// The gate is on the last FAILURE, not the last attempt. Throttling every
		// refresh here would silently redefine the TTL: a healthy endpoint past
		// its TTL must refresh on the next lookup, or a rotated key set is not
		// picked up until minRefetch also elapses. That regression is real — it
		// broke TestJWTBearer_JWKS_TTLExpiryRefetch, which configures a long
		// minRefetch precisely to isolate the TTL path from the unknown-kid one.
		throttled := s.keys != nil && !s.lastFailure.IsZero() && now.Sub(s.lastFailure) < s.minRefetch
		if !throttled {
			if err := s.refreshLocked(ctx, now); err != nil {
				if s.keys == nil {
					// No usable cache and the fetch failed: reject (do NOT fall open).
					return nil, err
				}
				// A cache exists. Serving it is the right call for a blip and the
				// wrong one for an outage, which is what the staleness bound below
				// decides. Log either way: this used to be silent, so an IdP that
				// had been unreachable for hours looked exactly like one that was
				// healthy.
				slog.WarnContext(ctx, "auth: JWKS refresh failed; serving the cached key set",
					"err", err,
					"cache_age", now.Sub(s.fetchedAt).String(),
					"max_age", (s.ttl + s.maxStale).String(),
				)
			}
		}
		// Bounded staleness. Past ttl+maxStale the cached set stops being a
		// tolerable substitute for a fresh one: a key the IdP rotated out —
		// possibly BECAUSE it was compromised — would otherwise keep verifying
		// tokens for as long as the endpoint stayed broken. Dropping the cache
		// (rather than only rejecting) also lets the next request retry
		// immediately, so recovery is not delayed by the throttle above.
		if s.keys != nil && now.Sub(s.fetchedAt) >= s.ttl+s.maxStale {
			s.keys = nil
			return nil, fmt.Errorf("%w: JWKS cache is stale beyond the grace window and the key set cannot be refreshed", ErrUnauthenticated)
		}
	}

	if k, ok := s.keys[kid]; ok {
		return k, nil
	}

	// Unknown kid: the signing key may have rotated in. Refetch once, but only
	// if we have not fetched within minRefetch, so unknown-kid tokens cannot
	// storm the endpoint.
	if now.Sub(s.lastFetch) >= s.minRefetch {
		if err := s.refreshLocked(ctx, now); err != nil {
			return nil, err
		}
		if k, ok := s.keys[kid]; ok {
			return k, nil
		}
	}

	return nil, fmt.Errorf("%w: no JWKS key matches the token kid", ErrUnauthenticated)
}

// refreshLocked fetches and replaces the cached key set. The caller must hold
// s.mu. lastFetch is stamped even on failure so the bounded-refetch window also
// throttles repeated failures. On error the previous cache is left intact.
func (s *jwksSource) refreshLocked(ctx context.Context, now time.Time) error {
	s.lastFetch = now
	keys, err := s.fetch(ctx)
	if err != nil {
		s.lastFailure = now
		// The error carries no key material — only the transport/parse cause.
		return fmt.Errorf("%w: JWKS fetch failed: %v", ErrUnauthenticated, err)
	}
	s.keys = keys
	s.fetchedAt = now
	s.lastFailure = time.Time{}
	return nil
}

// fetch retrieves and parses the JWKS document. It bounds the response size and
// requires an HTTP 200.
func (s *jwksSource) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSResponseBytes))
	if err != nil {
		return nil, err
	}
	return parseJWKS(body)
}

// jwkSet / jwk model the RFC 7517 JSON Web Key Set, limited to the fields this
// verifier needs for RSA signature keys.
type jwkSet struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
	Use string `json:"use"`
	Alg string `json:"alg"`
}

// parseJWKS decodes a JWKS document into a kid->key map, keeping only usable RSA
// keys. A key marked for encryption use ("enc") or a non-RS256 alg is skipped.
// An empty or all-unusable set is an error (fail closed).
func parseJWKS(body []byte) (map[string]*rsa.PublicKey, error) {
	var set jwkSet
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("invalid JWKS JSON: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, k := range set.Keys {
		if !strings.EqualFold(k.Kty, "RSA") {
			continue // only RSA signing keys are supported
		}
		if k.Use != "" && !strings.EqualFold(k.Use, "sig") {
			continue // skip keys explicitly scoped to encryption
		}
		if k.Alg != "" && !strings.EqualFold(k.Alg, "RS256") {
			continue // this verifier only checks RS256
		}
		pub, err := jwkToRSAPublicKey(k)
		if err != nil {
			continue // skip an unparseable key rather than reject the whole set
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("JWKS contained no usable RSA keys")
	}
	return keys, nil
}

// jwkToRSAPublicKey reconstructs an *rsa.PublicKey from a JWK's base64url
// modulus ("n") and exponent ("e") using math/big — the stdlib-only path that
// avoids any external JWK dependency.
func jwkToRSAPublicKey(k jwk) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("jwk modulus is not valid base64url: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("jwk exponent is not valid base64url: %w", err)
	}
	if len(nBytes) == 0 || len(eBytes) == 0 {
		return nil, fmt.Errorf("jwk has an empty modulus or exponent")
	}
	e := new(big.Int).SetBytes(eBytes)
	// A sane RSA public exponent fits in an int and is >= 3 (commonly 65537).
	if !e.IsInt64() || e.Int64() < 2 || e.Int64() > (1<<31-1) {
		return nil, fmt.Errorf("jwk exponent is out of the supported range")
	}
	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(nBytes),
		E: int(e.Int64()),
	}, nil
}
