package auth

import (
	"context"
	"crypto/rsa"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The question these tests answer: when the IdP stops answering, for how long do
// its old keys keep verifying tokens?
//
// The former answer was "forever". Past the TTL the refresh error was discarded
// whenever a cached key set existed, so a key the IdP had rotated out — possibly
// because it was compromised — stayed valid for as long as the endpoint stayed
// broken. Nothing was logged, so an IdP unreachable for hours looked identical to
// a healthy one.
//
// Failing closed the instant a refresh fails is the opposite error: one blip and
// every authenticated request dies. So the answer is a BOUND, and these tests pin
// both of its edges — inside the window availability wins, past it revocation
// does. An implementation that only ever served stale keys would pass the first
// test; one that only ever failed closed would pass the second. Neither passes
// both.
//
// Found by scripts/robustness-audit.sh (BLOCKER JWKS_STALE_CACHE).

const (
	staleTestTTL      = 5 * time.Minute
	staleTestMaxStale = 30 * time.Minute
	staleTestRefetch  = time.Minute
)

// newStaleWindowFixture returns an authenticator over a controllable JWKS
// endpoint with a bootstrapped cache, plus a token signed by the cached key.
func newStaleWindowFixture(t *testing.T) (Authenticator, *mutableJWKS, *fakeClock, string) {
	t.Helper()
	keyA, _ := rsaKeys(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}

	endpoint := &mutableJWKS{body: jwksBodyKeys(t, map[string]*rsa.PublicKey{"key-A": &keyA.PublicKey})}
	srv := httptest.NewServer(http.HandlerFunc(endpoint.serveHTTP))
	t.Cleanup(srv.Close)

	authn := NewJWTBearer(nil, WithJWKS(srv.URL,
		WithJWKSClock(clk.now),
		WithJWKSCacheTTL(staleTestTTL),
		WithJWKSMaxStaleness(staleTestMaxStale),
		WithJWKSMinRefetchInterval(staleTestRefetch),
	))

	tok := makeRS256(t, keyA, "key-A", validRSClaims())
	if _, err := authn.Authenticate(context.Background(), bearer(tok)); err != nil {
		t.Fatalf("bootstrap verify: %v", err)
	}
	return authn, endpoint, clk, tok
}

// TestJWKS_FailedRefreshInsideTheGraceWindowKeepsServing is the availability
// edge: a brief IdP outage must not take authentication down with it.
func TestJWKS_FailedRefreshInsideTheGraceWindowKeepsServing(t *testing.T) {
	authn, endpoint, clk, tok := newStaleWindowFixture(t)

	endpoint.setFailing(true)
	// Past the TTL, so a refresh is attempted and fails, but well inside
	// ttl+maxStale.
	clk.advance(staleTestTTL + time.Minute)

	if _, err := authn.Authenticate(context.Background(), bearer(tok)); err != nil {
		t.Fatalf("a token must still verify while the cache is inside its grace window; got %v", err)
	}
}

// TestJWKS_FailedRefreshPastTheGraceWindowRejects is the revocation edge, and the
// blocker itself: the old behaviour served this token indefinitely.
func TestJWKS_FailedRefreshPastTheGraceWindowRejects(t *testing.T) {
	authn, endpoint, clk, tok := newStaleWindowFixture(t)

	endpoint.setFailing(true)
	clk.advance(staleTestTTL + staleTestMaxStale + time.Second)

	_, err := authn.Authenticate(context.Background(), bearer(tok))
	if err == nil {
		t.Fatal("a key set that could not be refreshed for longer than the grace window must stop verifying tokens")
	}
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("error = %v, want ErrUnauthenticated", err)
	}
}

// TestJWKS_RecoversOnTheNextRequestAfterTheEndpointReturns guards the other half
// of failing closed: rejecting is only acceptable if recovery is immediate.
// Dropping the cache (rather than merely rejecting) is what makes the next
// lookup refetch instead of waiting out the failure throttle.
func TestJWKS_RecoversOnTheNextRequestAfterTheEndpointReturns(t *testing.T) {
	authn, endpoint, clk, tok := newStaleWindowFixture(t)

	endpoint.setFailing(true)
	clk.advance(staleTestTTL + staleTestMaxStale + time.Second)
	if _, err := authn.Authenticate(context.Background(), bearer(tok)); err == nil {
		t.Fatal("expected the stale cache to be rejected first")
	}

	// The IdP comes back. No clock advance: recovery must not wait for the
	// throttle window that the preceding failure opened.
	endpoint.setFailing(false)
	if _, err := authn.Authenticate(context.Background(), bearer(tok)); err != nil {
		t.Fatalf("the very next request after the endpoint recovered must verify; got %v", err)
	}
}

// TestJWKS_FailingRefreshIsThrottled: while stale-but-serving, a failing endpoint
// must not be re-hit on every single request. Before the fix, fetchedAt never
// advanced on failure, so every authenticated request re-attempted the fetch —
// the outage generated one upstream call per request, at exactly the moment the
// IdP could least afford it.
func TestJWKS_FailingRefreshIsThrottled(t *testing.T) {
	authn, endpoint, clk, tok := newStaleWindowFixture(t)
	bootstrap := endpoint.count()

	endpoint.setFailing(true)
	clk.advance(staleTestTTL + time.Second) // past TTL, inside the grace window

	for i := 0; i < 20; i++ {
		if _, err := authn.Authenticate(context.Background(), bearer(tok)); err != nil {
			t.Fatalf("request %d inside the grace window must still verify: %v", i, err)
		}
	}

	if got := endpoint.count() - bootstrap; got != 1 {
		t.Errorf("20 requests against a failing endpoint produced %d fetches, want 1 "+
			"(the first attempt; the rest throttled by minRefetch)", got)
	}
}

// TestJWKS_ThrottleDoesNotDelayARotationFromAHealthyEndpoint pins the regression
// the first version of this fix introduced: gating the TTL refresh on the last
// ATTEMPT (rather than the last FAILURE) silently redefines the TTL, because a
// healthy endpoint past its TTL then waits out minRefetch too. The existing
// TestJWTBearer_JWKS_TTLExpiryRefetch caught it; this states the property
// directly so the next person to touch the throttle sees why the field is
// lastFailure.
func TestJWKS_ThrottleDoesNotDelayARotationFromAHealthyEndpoint(t *testing.T) {
	keyA, keyB := rsaKeys(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}

	endpoint := &mutableJWKS{body: jwksBodyKeys(t, map[string]*rsa.PublicKey{"key-A": &keyA.PublicKey})}
	srv := httptest.NewServer(http.HandlerFunc(endpoint.serveHTTP))
	t.Cleanup(srv.Close)

	authn := NewJWTBearer(nil, WithJWKS(srv.URL,
		WithJWKSClock(clk.now),
		WithJWKSCacheTTL(staleTestTTL),
		WithJWKSMaxStaleness(staleTestMaxStale),
		// Far longer than the TTL: if the TTL path were throttled by attempts,
		// the rotated key would not be picked up for an hour.
		WithJWKSMinRefetchInterval(time.Hour),
	))

	tokA := makeRS256(t, keyA, "key-A", validRSClaims())
	if _, err := authn.Authenticate(context.Background(), bearer(tokA)); err != nil {
		t.Fatalf("bootstrap verify: %v", err)
	}

	endpoint.set(jwksBodyKeys(t, map[string]*rsa.PublicKey{"key-B": &keyB.PublicKey}))
	tokB := makeRS256(t, keyB, "key-B", validRSClaims())

	clk.advance(staleTestTTL + time.Second)
	if _, err := authn.Authenticate(context.Background(), bearer(tokB)); err != nil {
		t.Fatalf("a healthy endpoint past its TTL must refresh regardless of minRefetch; got %v", err)
	}
}
