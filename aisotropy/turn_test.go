package aisotropy

// turn_test.go is the demonstrable artifact for the in-process App.Turn surface:
// the same isolation-by-construction the black-box HTTP probes in
// isolation_e2e_test.go prove over the network, now proven over the LIBRARY path
// — Turn called like a function, no HTTP hop, no auth middleware.
//
// Why the assertions read the working-memory STORE directly rather than the
// reply text: the non-streaming pipeline (ChatCore -> chatReflect) asks the model
// for structured output, so the stub's reply echoes only THIS turn's prompt (the
// "reply" field of a schema-satisfying JSON object), NOT the hydrated system
// prompt. Cross-turn memory and cross-tenant isolation therefore cannot be read
// off the reply string; they are asserted at the tier the pipeline actually wrote
// to, exactly as isolation_e2e_test.go's fact/semantic assertions poll the store.
// Every test is keyless (fastStub, no ANTHROPIC_API_KEY) and defers app.Close().

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/memory"
)

// newTurnApp builds a keyless in-process App with an effectively unlimited rate
// limit — the quota gate is a separate constitutional concern, and throttling
// would only mask the memory/isolation behavior these tests probe. The caller
// defers the returned close.
func newTurnApp(t *testing.T) (*App, func()) {
	t.Helper()
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))
	return app, func() {
		if err := app.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}
}

// recentContents reads a session's working-memory buffer directly off the App's
// shared runtime and joins the message contents, so a test can assert what the
// in-process pipeline actually persisted for a given (tenant, user, session).
// wm.Sync() first drains any background condensation so the read is deterministic.
func recentContents(t *testing.T, app *App, tenant, user, session string) string {
	t.Helper()
	rt, err := app.ensureRuntime()
	if err != nil {
		t.Fatalf("ensureRuntime: %v", err)
	}
	rt.wm.Sync()
	msgs, err := rt.wm.Recent(context.Background(), memory.Key{Tenant: tenant, User: user, Session: session}, 100)
	if err != nil {
		t.Fatalf("Recent(%s/%s/%s): %v", tenant, user, session, err)
	}
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

// TestTurnRunsPipelineInProcess is the smoke anchor: Turn boots the shared
// runtime lazily on first call, runs the same non-streaming pipeline the HTTP
// /chat route runs (ChatCore), and returns the structured reply with the tenant
// isolation echo and the keyless-stub flag. A second turn on a different tenant
// proves the runtime is reused (built once) yet the isolation echo tracks the
// caller, never the first turn.
func TestTurnRunsPipelineInProcess(t *testing.T) {
	app, closeApp := newTurnApp(t)
	defer closeApp()

	ctx := context.Background()

	out, err := app.Turn(ctx, ChatInput{TenantID: "tenantX", UserID: "userX", SessionID: "s1", Prompt: "hello"})
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if out.TenantID != "tenantX" {
		t.Errorf("tenant echo = %q, want tenantX", out.TenantID)
	}
	if !out.Stub {
		t.Errorf("Stub = false, want true (credential-less stub client with no ANTHROPIC_API_KEY)")
	}
	if strings.TrimSpace(out.Reply) == "" {
		t.Errorf("empty reply from the reflection pipeline")
	}

	out2, err := app.Turn(ctx, ChatInput{TenantID: "tenantY", UserID: "userY", SessionID: "s2", Prompt: "hi again"})
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if out2.TenantID != "tenantY" {
		t.Errorf("second-turn tenant echo = %q, want tenantY", out2.TenantID)
	}
}

// TestTurnMemoryPersistsAcrossTurnsInProcess proves the payoff of the reused
// runtime: two Turn calls on the SAME (tenant, user, session) accumulate in the
// same working-memory buffer, so the second turn hydrates the first. Because the
// non-streaming reply echoes only the current prompt, this is asserted at the
// store — the tier the pipeline wrote to — mirroring isolation_e2e_test.go.
func TestTurnMemoryPersistsAcrossTurnsInProcess(t *testing.T) {
	app, closeApp := newTurnApp(t)
	defer closeApp()

	ctx := context.Background()
	const tenant, user, session = "acme", "alice", "chat-1"
	markerA1 := marker(t, "turnfirst")
	markerA2 := marker(t, "turnsecond")

	// Turn 1: seed the buffer. After it returns, the first turn is already
	// persisted in-process — proven by the store read below.
	if _, err := app.Turn(ctx, ChatInput{TenantID: tenant, UserID: user, SessionID: session, Prompt: "note one: " + markerA1}); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if got := recentContents(t, app, tenant, user, session); !strings.Contains(got, markerA1) {
		t.Fatalf("after turn 1, working memory did not contain the first turn %q", markerA1)
	}

	// Turn 2: same session, against the SAME lazily-built runtime (no re-boot).
	// The buffer it hydrates already holds turn 1.
	if _, err := app.Turn(ctx, ChatInput{TenantID: tenant, UserID: user, SessionID: session, Prompt: "note two: " + markerA2}); err != nil {
		t.Fatalf("turn 2: %v", err)
	}

	// Both turns now coexist in the one buffer: in-process memory persisted
	// across the two function calls exactly as the HTTP path persists across two
	// requests.
	got := recentContents(t, app, tenant, user, session)
	if !strings.Contains(got, markerA1) {
		t.Errorf("after turn 2, working memory lost the first turn %q; the runtime was not reused across turns", markerA1)
	}
	if !strings.Contains(got, markerA2) {
		t.Errorf("after turn 2, working memory is missing the second turn %q", markerA2)
	}
}

// TestTurnIsolationNoCrossTenantLeak proves the by-construction isolation
// guarantee holds over the in-process path: a turn for tenant A writes only to
// A's scope, and a turn for a different tenant (or a different user of the same
// tenant, deliberately reusing A's session id) never reads A's data. The
// positive control (A sees its own note) proves the probe exercises real
// hydration, so the negative assertions are not vacuous.
func TestTurnIsolationNoCrossTenantLeak(t *testing.T) {
	app, closeApp := newTurnApp(t)
	defer closeApp()

	ctx := context.Background()
	const session = "shared-session" // deliberately reused across identities
	secretA := marker(t, "turnalpha")

	if _, err := app.Turn(ctx, ChatInput{TenantID: "tenantA", UserID: "userA", SessionID: session, Prompt: "store verbatim: " + secretA}); err != nil {
		t.Fatalf("seed turn for tenantA/userA: %v", err)
	}
	// Give the other identities a turn each so their buffers exist and are the
	// thing being read (not just an empty store).
	for _, id := range []struct{ tenant, user string }{
		{"tenantB", "userB"},
		{"tenantA", "userB"},
		{"tenantB", "userA"},
	} {
		if _, err := app.Turn(ctx, ChatInput{TenantID: id.tenant, UserID: id.user, SessionID: session, Prompt: "unrelated note"}); err != nil {
			t.Fatalf("turn for %s/%s: %v", id.tenant, id.user, err)
		}
	}

	// POSITIVE CONTROL: A's own session holds A's secret.
	if got := recentContents(t, app, "tenantA", "userA", session); !strings.Contains(got, secretA) {
		t.Fatalf("positive control failed: tenantA/userA's own working memory did not contain %q; the isolation probe is not exercising real writes", secretA)
	}

	// NEGATIVE: no other identity's buffer — same session id, different
	// tenant/user — may contain A's secret. Sharing only the session component
	// must not share the buffer (working-memory keys are {tenant,user,session}).
	for _, id := range []struct{ tenant, user string }{
		{"tenantB", "userB"},
		{"tenantA", "userB"},
		{"tenantB", "userA"},
	} {
		if got := recentContents(t, app, id.tenant, id.user, session); strings.Contains(got, secretA) {
			t.Errorf("CROSS-TENANT LEAK via Turn: %s/%s reading session %q saw tenantA/userA's secret %q", id.tenant, id.user, session, secretA)
		}
	}
}

// TestTurnRejectsInvalidIdentity proves the isolation identity gate holds on the
// in-process path exactly as on HTTP: an empty tenant/user is rejected by
// ChatCore's chatIdentity (memory.Key.Validate) rather than silently reading an
// unscoped store.
func TestTurnRejectsInvalidIdentity(t *testing.T) {
	app, closeApp := newTurnApp(t)
	defer closeApp()

	if _, err := app.Turn(context.Background(), ChatInput{TenantID: "", UserID: "userX", SessionID: "s1", Prompt: "hi"}); err == nil {
		t.Fatalf("expected an error for an empty tenant id, got nil")
	}
}

// TestTurnConcurrentFirstCallsLazyInitRace proves the lazy runtime build is
// concurrency-safe: N goroutines fire their FIRST Turn simultaneously, racing on
// turnOnce, and exactly one runtime must be built and shared. Run under -race,
// this fails if ensureRuntime's memoization is not properly synchronized. Each
// goroutine uses its own tenant, so a correct run also leaves N isolated buffers.
func TestTurnConcurrentFirstCallsLazyInitRace(t *testing.T) {
	app, closeApp := newTurnApp(t)
	defer closeApp()

	const n = 16
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
		rts  = make(map[*appRuntime]struct{})
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tenant := fmt.Sprintf("tenant%02d", i)
			if _, err := app.Turn(context.Background(), ChatInput{TenantID: tenant, UserID: "u", SessionID: "s", Prompt: "hi"}); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", tenant, err))
				mu.Unlock()
				return
			}
			// The runtime pointer every racer observes must be the single shared
			// one (turnOnce built it exactly once).
			rt, err := app.ensureRuntime()
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				return
			}
			mu.Lock()
			rts[rt] = struct{}{}
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	for _, err := range errs {
		t.Errorf("concurrent first Turn failed: %v", err)
	}
	if len(rts) != 1 {
		t.Fatalf("lazy init built %d distinct runtimes under concurrency, want exactly 1", len(rts))
	}
}

// TestCloseIdempotentWithoutTurn proves Close is safe on an App that never called
// Turn or Run (SDK-only lifecycle) and safe to call more than once.
func TestCloseIdempotentWithoutTurn(t *testing.T) {
	app := New(WithLLM(fastStub()))
	if err := app.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
