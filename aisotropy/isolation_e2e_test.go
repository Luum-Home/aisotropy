package aisotropy

// isolation_e2e_test.go is the headline "demonstrable artifact" for the moat:
// a BLACK-BOX test that drives the real public aisotropy.App HTTP surface with
// two tenants and proves tenant A can never read tenant B's data — across
// working memory, facts, and semantic hydration — under both sequential and
// concurrent load.
//
// It reuses the harness pattern from app_test.go (TestRunServesAndShutsDown):
// the App is built with the credential-less stub LLM (no ANTHROPIC_API_KEY
// needed) and default in-process stores, booted on a free loopback port, and
// probed over real net/http.
//
// Why the streaming route is the leak probe: the stub's SSE echo folds the
// hydrated system prompt back into its response verbatim (buildStubWords in
// internal/llm/stub.go emits "system=%q"). The system prompt is exactly where
// working-memory recent messages, facts, and semantic RAG land
// (hydrateSystemPrompt in internal/api/chat_stream.go). So if any of tenant B's
// hydration ever pulled tenant A's content, that content would appear in B's
// streamed body. A marker that is absent from B's *request* but present in B's
// *response* is a proven cross-tenant leak. The negative queries below are
// phrased with the retrieval words but WITHOUT the literal marker, so a passing
// assertion is never vacuous: the paired positive control proves the same
// phrasing DOES surface the marker when the scope legitimately matches.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// fastStub is the credential-less stub LLM with a near-zero inter-token delay,
// so the SSE echo of a (potentially long) hydrated system prompt streams
// quickly enough to keep the concurrency probe snappy. Delay must be > 0 or
// StreamChat floors it back to 15ms.
func fastStub() *llm.StubClient { return &llm.StubClient{Delay: time.Nanosecond} }

// marker returns a distinctive, purely-alphanumeric single token (lowercase so
// it survives FTS5 tokenization as one term, no punctuation so it is never
// JSON-escaped in the SSE body). Two markers never collide, so a marker found
// in the wrong tenant's response is unambiguous proof of a leak.
func marker(t *testing.T, prefix string) string {
	t.Helper()
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return prefix + hex.EncodeToString(b[:])
}

// startIsolationApp boots a fresh App (own in-memory working/facts/semantic
// stores) on a free port with the fast stub and an effectively unlimited rate
// limit — the quota gate is a different constitutional concern, and throttling
// would only mask hydration under the concurrency probe. Returns the base URL;
// registers a t.Cleanup that cancels ctx and waits for a clean Run return.
func startIsolationApp(t *testing.T) string {
	t.Helper()
	addr := freeAddr(t)
	app := New(
		WithAddr(addr),
		WithLLM(fastStub()),
		WithRateLimit(1e9, 1e9),
		// NoAuth: these probes exercise cross-tenant HYDRATION isolation, a
		// concern orthogonal to authentication. Opt out of the default deny-all
		// gate so every /v1 request reaches a handler (the pre-seam behavior),
		// keeping the leak assertions meaningful. The authn posture itself is
		// covered by TestPathParamTrust... below.
		WithAuthenticator(NoAuth()),
	)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	base := "http://" + addr
	waitForServer(t, base+"/healthz")

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("Run returned error on shutdown: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("Run did not return after ctx cancellation")
		}
	})
	return base
}

// chatURL / streamURL build the two per-(tenant,user) chat route paths.
func chatURL(base, tenant, user string) string {
	return fmt.Sprintf("%s/v1/tenant/%s/user/%s/chat", base, tenant, user)
}
func streamURL(base, tenant, user string) string {
	return fmt.Sprintf("%s/v1/tenant/%s/user/%s/chat/stream", base, tenant, user)
}

// postChat drives the non-streaming route (writer path): it validates identity,
// persists the user turn to working memory + the consolidation outbox, and
// returns the structured reply. Used to seed a tenant's memory. Returns the
// HTTP status and the decoded reply text.
func postChat(t *testing.T, base, tenant, user, session, prompt string) (int, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"prompt": prompt, "product_id": "isolation-test", "session_id": session})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, chatURL(base, tenant, user), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST chat: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, string(raw)
	}
	var out struct {
		Reply    string `json:"reply"`
		TenantID string `json:"tenant_id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode chat reply (%s): %v", raw, err)
	}
	return resp.StatusCode, out.Reply
}

// streamChat drives the SSE route (leak probe): it returns the full raw
// response body. Because the stub echoes the hydrated system prompt into the
// stream, the returned body contains every fragment of context the server
// hydrated for this exact (tenant, user, session) — which is precisely what a
// leak would smuggle across. Returns HTTP status and the raw SSE body.
func streamChat(t *testing.T, base, tenant, user, session, prompt string) (int, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"prompt": prompt, "product_id": "isolation-test", "session_id": session})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, streamURL(base, tenant, user), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST chat/stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body) // blocks until the producer closes the stream
	if err != nil {
		t.Fatalf("read SSE body: %v", err)
	}
	return resp.StatusCode, string(raw)
}

// TestIsolationWorkingMemoryNoCrossTenantLeak proves the BadgerDB working-memory
// tier (the live per-session buffer) never crosses tenants — even when a second
// tenant deliberately reuses the FIRST tenant's session_id. Working-memory keys
// are {tenant,user,session}; sharing only the session component must not share
// the buffer.
func TestIsolationWorkingMemoryNoCrossTenantLeak(t *testing.T) {
	base := startIsolationApp(t)
	const session = "shared-session-id"               // deliberately reused across tenants
	const neutralRead = "please give a status update" // carries NO marker
	secretA := marker(t, "wmalpha")

	// Tenant A writes a note carrying its secret into the shared session id.
	if status, _ := postChat(t, base, "tenantA", "userA", session, "Store this note verbatim: "+secretA); status != http.StatusOK {
		t.Fatalf("seed write for tenantA: status %d", status)
	}

	// POSITIVE CONTROL: A reading its own session (neutral prompt, no marker)
	// must surface secretA via working-memory recent-message hydration. This
	// proves the probe is actually exercising hydration — so the negative
	// assertions below are meaningful, not vacuous.
	status, aBody := streamChat(t, base, "tenantA", "userA", session, neutralRead)
	if status != http.StatusOK {
		t.Fatalf("positive-control stream for tenantA: status %d", status)
	}
	if !strings.Contains(aBody, secretA) {
		t.Fatalf("positive control failed: tenantA's own working memory did not hydrate secret %q into its response; the leak probe is not exercising hydration", secretA)
	}

	// NEGATIVE: every other identity reading the SAME session id must NOT see
	// secretA. userB-within-tenantA and userA-within-tenantB catch per-user
	// leaks, not just per-tenant ones.
	for _, id := range []struct{ tenant, user string }{
		{"tenantB", "userB"},
		{"tenantA", "userB"},
		{"tenantB", "userA"},
	} {
		st, body := streamChat(t, base, id.tenant, id.user, session, neutralRead)
		if st != http.StatusOK {
			t.Errorf("stream for %s/%s: status %d", id.tenant, id.user, st)
			continue
		}
		if strings.Contains(body, secretA) {
			t.Errorf("CROSS-TENANT LEAK: %s/%s reading session %q saw tenantA/userA's working-memory secret %q", id.tenant, id.user, session, secretA)
		}
	}
}

// TestIsolationFactsAndSemanticNoCrossTenantLeak proves the facts (SQLite+FTS5)
// and semantic (in-process cosine) tiers never cross tenants. Unlike working
// memory these tiers are scoped per (tenant,user) — NOT per session — and are
// populated asynchronously by the consolidation outbox worker, so the test
// polls until tenant A can retrieve its own consolidated fact before asserting
// no other identity can.
func TestIsolationFactsAndSemanticNoCrossTenantLeak(t *testing.T) {
	base := startIsolationApp(t)
	secretA := marker(t, "factalpha")
	// "Remember that ..." triggers the heuristic fact extractor, so this turn
	// is distilled into a fact whose content carries secretA, then dual-indexed
	// as a KindFact semantic vector (engram.Store). Retrieval words ("alpha
	// private vault code") are shared with the negative query below; the marker
	// itself is not, so the negative query can only surface secretA via a leak.
	writePrompt := "Remember that " + secretA + " is the alpha private vault code"
	// Retrieval phrasing: overlaps the stored fact on real words, contains NO
	// marker. Used for BOTH the positive control and every negative probe.
	const retrieval = "what is the alpha private vault code"

	if status, _ := postChat(t, base, "tenantA", "userA", "seed-session", writePrompt); status != http.StatusOK {
		t.Fatalf("seed write for tenantA: status %d", status)
	}

	// POSITIVE CONTROL: poll until A can retrieve secretA in a FRESH session
	// (so the hit comes from the facts/semantic tiers, NOT the live working-
	// memory buffer). The consolidation worker polls the outbox ~1s; give it a
	// generous window. Success here proves the tiers are populated AND that the
	// negative queries below use phrasing capable of surfacing the marker.
	deadline := time.Now().Add(10 * time.Second)
	var surfaced bool
	for time.Now().Before(deadline) {
		status, body := streamChat(t, base, "tenantA", "userA", "fresh-read-session", retrieval)
		if status == http.StatusOK && strings.Contains(body, secretA) {
			surfaced = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !surfaced {
		t.Fatalf("positive control failed: tenantA never retrieved its own consolidated fact %q via facts/semantic hydration within the deadline; cannot make the isolation assertion meaningful", secretA)
	}

	// NEGATIVE: no other identity may retrieve secretA, in any session, using
	// the same retrieval phrasing. tenantA/userB shares the TENANT with the
	// writer but is a different user — chat turns consolidate as KindFact/
	// KindTurn (never tenant-shared KindDocument), so this must not leak either.
	for _, id := range []struct{ tenant, user string }{
		{"tenantB", "userB"},
		{"tenantA", "userB"},
		{"tenantB", "userA"},
	} {
		st, body := streamChat(t, base, id.tenant, id.user, "probe-session", retrieval)
		if st != http.StatusOK {
			t.Errorf("stream for %s/%s: status %d", id.tenant, id.user, st)
			continue
		}
		if strings.Contains(body, secretA) {
			t.Errorf("CROSS-TENANT LEAK: %s/%s retrieved tenantA/userA's facts/semantic secret %q", id.tenant, id.user, secretA)
		}
	}
}

// TestIsolationConcurrencyNoCrossTenantLeak is the concurrency probe guarding
// the constitutional gate "no code path may hold two tenants' data in the same
// in-memory buffer/goroutine scope". N goroutines hammer both tenants' streaming
// routes simultaneously — sharing one session_id across tenants to maximize the
// chance a mishandled buffer would bleed — and NO response may ever contain the
// other tenant's marker.
func TestIsolationConcurrencyNoCrossTenantLeak(t *testing.T) {
	base := startIsolationApp(t)
	const session = "concurrent-session"
	const neutralRead = "status ping"
	secretA := marker(t, "concalpha")
	secretB := marker(t, "concbravo")

	// Seed each tenant's working memory (same session id, different tenant) so
	// every concurrent read hydrates real, tenant-specific content.
	if st, _ := postChat(t, base, "tenantA", "userA", session, "note: "+secretA); st != http.StatusOK {
		t.Fatalf("seed tenantA: status %d", st)
	}
	if st, _ := postChat(t, base, "tenantB", "userB", session, "note: "+secretB); st != http.StatusOK {
		t.Fatalf("seed tenantB: status %d", st)
	}

	const iterations = 24 // per tenant
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		leaks   []string
		aSawOwn bool // liveness: at least one A read hydrated secretA
		bSawOwn bool
	)
	record := func(msg string) { mu.Lock(); leaks = append(leaks, msg); mu.Unlock() }

	for i := 0; i < iterations; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			st, body := streamChat(t, base, "tenantA", "userA", session, neutralRead)
			if st != http.StatusOK {
				record(fmt.Sprintf("tenantA stream status %d", st))
				return
			}
			if strings.Contains(body, secretB) {
				record(fmt.Sprintf("CROSS-TENANT LEAK under concurrency: tenantA response contained tenantB secret %q", secretB))
			}
			if strings.Contains(body, secretA) {
				mu.Lock()
				aSawOwn = true
				mu.Unlock()
			}
		}()
		go func() {
			defer wg.Done()
			st, body := streamChat(t, base, "tenantB", "userB", session, neutralRead)
			if st != http.StatusOK {
				record(fmt.Sprintf("tenantB stream status %d", st))
				return
			}
			if strings.Contains(body, secretA) {
				record(fmt.Sprintf("CROSS-TENANT LEAK under concurrency: tenantB response contained tenantA secret %q", secretA))
			}
			if strings.Contains(body, secretB) {
				mu.Lock()
				bSawOwn = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	for _, l := range leaks {
		t.Error(l)
	}
	// Liveness: if neither tenant ever hydrated its OWN secret, the probe never
	// actually exercised hydration and the leak assertions would be vacuous.
	if !aSawOwn || !bSawOwn {
		t.Fatalf("concurrency probe did not exercise hydration (aSawOwn=%v bSawOwn=%v); leak assertions would be meaningless", aSawOwn, bSawOwn)
	}
}

// startAuthApp boots a fresh isolation-style App with an explicitly chosen
// authenticator (or nil to leave it UNconfigured, exercising the default
// deny-all gate). Same fast-stub + unlimited-rate harness as startIsolationApp;
// registers the same cleanup.
func startAuthApp(t *testing.T, authn Authenticator) string {
	t.Helper()
	addr := freeAddr(t)
	opts := []Option{WithAddr(addr), WithLLM(fastStub()), WithRateLimit(1e9, 1e9)}
	if authn != nil {
		opts = append(opts, WithAuthenticator(authn))
	}
	app := New(opts...)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	base := "http://" + addr
	waitForServer(t, base+"/healthz")

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("Run returned error on shutdown: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("Run did not return after ctx cancellation")
		}
	})
	return base
}

// postChatBearer drives the non-streaming chat route with an optional
// "Authorization: Bearer <bearer>" header (omitted when bearer is ""). Returns
// the HTTP status. Used by the authn anchor to probe the gate directly.
func postChatBearer(t *testing.T, base, tenant, user, bearer, prompt string) int {
	t.Helper()
	body, err := json.Marshal(map[string]any{"prompt": prompt, "product_id": "authn-test", "session_id": "s1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, chatURL(base, tenant, user), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST chat: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// makeHS256 builds a minimal HS256 JWT (header.payload.signature, base64url, no
// padding) so the anchor can mint valid/expired tokens without any external JWT
// dependency — mirroring the stdlib-only verifier in internal/auth.
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

// TestPathParamTrustUnauthenticatedRequestIsRejectedByDefaultAuthnRegressionAnchor
// is the INVERTED anchor (formerly ...IsAccepted...): with the authenticated-
// identity seam landed, path tenant/user are no longer trusted with no
// credential. It pins three postures:
//
//  1. NO authenticator configured -> default-deny: every /v1 request is 401.
//  2. NoAuth() explicitly configured -> the pre-seam behavior: an authless
//     request to an arbitrary tenant/user is accepted (200).
//  3. A JWT authenticator: a valid token binds must-agree (right tenant -> 200,
//     wrong tenant -> 403); an expired or bad-signature token -> 401.
//
// If a future change weakens any of these, this anchor fails — that failure is
// the intended signal that the security posture changed.
func TestPathParamTrustUnauthenticatedRequestIsRejectedByDefaultAuthnRegressionAnchor(t *testing.T) {
	// (1) Default-deny: no authenticator wired.
	denyBase := startAuthApp(t, nil)
	if status := postChatBearer(t, denyBase, "any-unowned-tenant", "any-unowned-user", "", "hello with no credentials"); status != http.StatusUnauthorized {
		t.Fatalf("AUTHN POSTURE REGRESSED: an unauthenticated request under the default (no authenticator) returned %d, want 401 (deny-all). "+
			"The runtime must default-deny /v1 when no WithAuthenticator is configured.", status)
	}

	// (2) NoAuth() opt-out: the pre-seam behavior is now an explicit choice.
	noAuthBase := startAuthApp(t, NoAuth())
	if status := postChatBearer(t, noAuthBase, "any-unowned-tenant", "any-unowned-user", "", "hello with no credentials"); status != http.StatusOK {
		t.Fatalf("NoAuth() should accept an authless request to an arbitrary tenant/user (pre-seam behavior), got %d, want 200", status)
	}
	// Under NoAuth the ONLY pre-handler gate is still the identity charset regex
	// (422/4xx), proving the 200 above is authorization opt-out, not a missing
	// input check. "tenant.bad" is URL-safe but violates ^[a-zA-Z0-9_-]{1,64}$.
	if status := postChatBearer(t, noAuthBase, "tenant.bad", "userA", "", "hi"); status == http.StatusOK {
		t.Errorf("expected the identity charset regex to reject an invalid tenant_id pre-handler, but got 200")
	}

	// (3) JWT authenticator: must-agree bind + credential verification.
	// The secret must be at least auth.MinHS256SecretBytes (32) long; a weaker
	// secret makes the verifier fail closed (rejects every request).
	secret := []byte("anchor-hs256-secret-at-least-32b!")
	jwtBase := startAuthApp(t, NewJWTAuthenticator(secret))
	future := time.Now().Add(time.Hour).Unix()
	past := time.Now().Add(-time.Hour).Unix()

	// Valid token for tenantA on tenantA's path -> 200 (bind agrees).
	validA := makeHS256(t, secret, map[string]any{"tenant_id": "tenantA", "user_id": "userA", "exp": future})
	if status := postChatBearer(t, jwtBase, "tenantA", "userA", validA, "hi"); status != http.StatusOK {
		t.Errorf("valid JWT for tenantA on tenantA's path: got %d, want 200", status)
	}
	// Valid token for tenantA on tenantB's path -> 403 (must-agree rejects).
	if status := postChatBearer(t, jwtBase, "tenantB", "userA", validA, "hi"); status != http.StatusForbidden {
		t.Errorf("valid JWT for tenantA on tenantB's path: got %d, want 403 (must-agree bind)", status)
	}
	// Expired token -> 401.
	expiredA := makeHS256(t, secret, map[string]any{"tenant_id": "tenantA", "user_id": "userA", "exp": past})
	if status := postChatBearer(t, jwtBase, "tenantA", "userA", expiredA, "hi"); status != http.StatusUnauthorized {
		t.Errorf("expired JWT: got %d, want 401", status)
	}
	// Bad-signature token (minted with the wrong secret) -> 401.
	badSig := makeHS256(t, []byte("wrong-secret"), map[string]any{"tenant_id": "tenantA", "user_id": "userA", "exp": future})
	if status := postChatBearer(t, jwtBase, "tenantA", "userA", badSig, "hi"); status != http.StatusUnauthorized {
		t.Errorf("bad-signature JWT: got %d, want 401", status)
	}
	// No credential at all against the JWT gate -> 401.
	if status := postChatBearer(t, jwtBase, "tenantA", "userA", "", "hi"); status != http.StatusUnauthorized {
		t.Errorf("missing credential against JWT gate: got %d, want 401", status)
	}
}
