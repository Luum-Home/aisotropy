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
	"crypto/rand"
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

// TestPathParamTrustUnauthenticatedRequestIsAcceptedAuthnRegressionAnchor
// documents CURRENT behavior and is the explicit regression anchor for the
// future Authenticator.
//
// TODAY: tenant_id/user_id are TRUSTED, validated Huma path params (regex
// ^[a-zA-Z0-9_-]{1,64}$, internal/api/chat.go). There is NO authentication
// wired — aisotropy/app.go installs only chi middleware.RequestID + Recoverer
// (see app.go's router.Use calls). So ANY caller may address ANY tenant/user in
// the path with no credential and be served (200), as chat.go's package doc
// admits ("the path param here is the walking-skeleton stand-in" for a JWT auth
// middleware).
//
// This test asserts that "authless request is accepted" as the CURRENT truth.
// When the Authenticator lands, this assertion MUST flip: an unauthenticated
// request should then be REJECTED (401/403), and this test's failure is the
// intended signal that the security posture changed. Do NOT delete it — invert
// its expectation and rename the "IsAccepted" clause.
func TestPathParamTrustUnauthenticatedRequestIsAcceptedAuthnRegressionAnchor(t *testing.T) {
	base := startIsolationApp(t)

	// No Authorization header, arbitrary tenant/user picked out of thin air.
	status, reply := postChat(t, base, "any-unowned-tenant", "any-unowned-user", "s1", "hello with no credentials")

	if status != http.StatusOK {
		t.Fatalf("AUTHN POSTURE CHANGED: an unauthenticated request to an arbitrary tenant/user returned %d, not 200. "+
			"If an Authenticator was intentionally wired, invert this anchor to assert rejection (401/403) and rename it. Reply: %q", status, reply)
	}
	t.Logf("current behavior (no authn wired): unauthenticated request to arbitrary tenant/user accepted with 200; reply=%q", reply)

	// Also confirm the ONLY gate in front of the handler today is the identity
	// charset regex (422), not authorization: a path param violating the regex
	// is rejected pre-handler, proving the 200 above is not an auth check but
	// mere input validation.
	// "tenant.bad" is URL-safe (dots are legal path chars) but violates the
	// ^[a-zA-Z0-9_-]{1,64}$ identity regex, so Huma rejects it pre-handler.
	badStatus, _ := postChat(t, base, "tenant.bad", "userA", "s1", "hi")
	if badStatus == http.StatusOK {
		t.Errorf("expected the identity charset regex to reject an invalid tenant_id pre-handler, but got 200")
	}
}
