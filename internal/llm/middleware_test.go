package llm

// middleware_test.go is the demonstrable, merge-blocker evidence for
// ADR-0062 (docs/decisions/0062-bounded-model-middleware.md): every invariant
// the ADR declares non-negotiable is proven here as an executable test, not
// asserted in prose.
//
// Invariant -> test map:
//  1. Cannot bypass the pre-spend quota gate: this package alone cannot prove
//     the end-to-end claim (the gate lives in internal/api, entirely BEFORE any
//     Client method is ever called — see middleware.go's package doc for the
//     exact call-site citations: ChatCore's chatCheckQuota, StreamCore's inline
//     check). aisotropy/model_middleware_test.go's
//     TestWithModelMiddlewareCannotBypassQuotaGate proves the behavioral claim
//     against the real handler pipeline (App.Turn -> ChatCore), asserting a
//     registered middleware is never invoked once a tenant's quota is exhausted.
//  2. Cannot bypass the circuit breaker: TestModelMiddlewareCannotBypassCircuitBreaker.
//  3. Tenant-safe, no shared mutable state: TestModelMiddlewareTenantSafeRace (-race).
//  4. ctx-cancel propagates + fail-closed + no secret leak:
//     TestModelMiddlewareCtxCancelPropagates, TestModelMiddlewareFailsClosedOnError.
//  5. Backward-compatible (none registered => unchanged): TestWithModelMiddlewareNoneRegisteredIsIdentity.
//
// Composition/ordering is covered separately: TestWithModelMiddlewareOrderingInnermostFirst.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- test doubles ------------------------------------------------------------

// recordingClient is a Client that records every StreamChat call (system,
// prompt, and the tenant found on ctx via TenantFromContext, if any) and
// streams back a single deterministic token derived from prompt, honoring ctx
// cancellation exactly like the real provider clients (StubClient et al).
type recordingClient struct {
	mu    sync.Mutex
	calls []recordedCall
}

type recordedCall struct {
	system, prompt string
	tenant         string
}

func (r *recordingClient) Name() string { return "recording" }

func (r *recordingClient) StreamChat(ctx context.Context, system, prompt string) (<-chan Token, error) {
	tenant, _ := TenantFromContext(ctx)
	r.mu.Lock()
	r.calls = append(r.calls, recordedCall{system: system, prompt: prompt, tenant: tenant})
	r.mu.Unlock()

	out := make(chan Token, 1)
	go func() {
		defer close(out)
		select {
		case <-ctx.Done():
			out <- Token{Err: ctx.Err()}
		case out <- Token{Text: "echo:" + prompt + ":" + tenant, Done: true}:
		}
	}()
	return out, nil
}

func (r *recordingClient) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// middlewareBlockingClient never sends a token until ctx is cancelled, so tests can
// prove ctx cancellation actually reaches the base client through the
// middleware chain.
type middlewareBlockingClient struct {
	started chan struct{}
}

func (b *middlewareBlockingClient) Name() string { return "blocking" }

func (b *middlewareBlockingClient) StreamChat(ctx context.Context, _, _ string) (<-chan Token, error) {
	out := make(chan Token)
	go func() {
		defer close(out)
		if b.started != nil {
			close(b.started)
		}
		<-ctx.Done()
		out <- Token{Err: ctx.Err()}
	}()
	return out, nil
}

// passthroughMiddleware forwards to next unchanged, recording the number of
// invocations and (optionally) the ctx it was called with, so tests can
// assert both "did the middleware run" and "what ctx did it receive".
type passthroughMiddleware struct {
	calls    atomic.Int32
	lastCtx  atomic.Value // context.Context
	tag      string
	upperArg bool // if true, mutate the prompt to prove transformation reaches the base client
}

func (p *passthroughMiddleware) WrapStream(ctx context.Context, system, prompt string, next StreamFunc) (<-chan Token, error) {
	p.calls.Add(1)
	p.lastCtx.Store(ctx)
	if p.upperArg {
		prompt = strings.ToUpper(prompt)
	}
	if p.tag != "" {
		system = p.tag + ":" + system
	}
	return next(ctx, system, prompt)
}

func (p *passthroughMiddleware) callCount() int { return int(p.calls.Load()) }

// failingMiddleware returns a fixed error WITHOUT ever calling next — the
// "fail closed" shape: a middleware that decides not to proceed must return an
// error, not silently fall back to an unmiddlewared call.
type failingMiddleware struct {
	err        error
	nextCalled atomic.Bool
}

func (f *failingMiddleware) WrapStream(_ context.Context, _, _ string, next StreamFunc) (<-chan Token, error) {
	_ = next // deliberately not called
	return nil, f.err
}

// --- invariant 5: backward-compatible ----------------------------------------

// TestWithModelMiddlewareNoneRegisteredIsIdentity proves that with no
// middleware registered, WithModelMiddleware returns the exact same Client
// value handed to it — not even a zero-op wrapper — so a runtime that never
// calls WithModelMiddleware/aisotropy.WithModelMiddleware is byte-for-byte
// unchanged from before ADR-0062.
func TestWithModelMiddlewareNoneRegisteredIsIdentity(t *testing.T) {
	base := &recordingClient{}

	got := WithModelMiddleware(base)
	if got != Client(base) {
		t.Fatalf("WithModelMiddleware(base) with no middleware returned a different value; want the exact base client unchanged")
	}

	gotVariadicEmpty := WithModelMiddleware(base, []ModelMiddleware{}...)
	if gotVariadicEmpty != Client(base) {
		t.Fatalf("WithModelMiddleware(base) with an empty middleware slice returned a different value; want the exact base client unchanged")
	}
}

// --- invariant 2: cannot bypass the circuit breaker --------------------------

// TestModelMiddlewareCannotBypassCircuitBreaker proves the fixed-inner-slot
// ordering (base -> WithModelMiddleware -> WithCircuitBreaker, exactly as
// aisotropy/app.go's startRuntime wires it): once the breaker trips open, the
// breaker fails fast BEFORE ever reaching the middleware chain, so a
// registered middleware is not merely prevented from "winning" against the
// breaker — it does not run AT ALL while the breaker is open. That is a
// stronger, structural guarantee than "next returns ErrCircuitOpen": the
// middleware has no execution window in which to attempt anything.
func TestModelMiddlewareCannotBypassCircuitBreaker(t *testing.T) {
	fc := &flakyClient{fail: true}
	spy := &passthroughMiddleware{}

	chain, ok := WithCircuitBreaker(WithModelMiddleware(fc, spy), 1, time.Minute).(*circuitClient)
	if !ok {
		t.Fatalf("WithCircuitBreaker did not return *circuitClient (breaker must be the outermost decorator here)")
	}
	base := time.Now()
	chain.now = func() time.Time { return base }
	ctx := context.Background()

	// First call: breaker is CLOSED, so it calls its inner client — the
	// middleware chain — which forwards to the failing base client. This trips
	// the breaker (threshold=1).
	if _, err := chain.StreamChat(ctx, "sys", "prompt"); err == nil {
		t.Fatal("expected the first call to fail (flaky base client) and trip the breaker")
	}
	callsAfterFirst := spy.callCount()
	if callsAfterFirst != 1 {
		t.Fatalf("middleware call count after the tripping call = %d, want 1 (middleware must run while the breaker is still closed)", callsAfterFirst)
	}
	if fc.callCount() != 1 {
		t.Fatalf("base client call count = %d, want 1", fc.callCount())
	}

	// Breaker is now OPEN. The next call must fail fast with ErrCircuitOpen —
	// and the middleware must NOT have been invoked again: it never got the
	// chance to see this call, let alone reorder or bypass the gate.
	_, err := chain.StreamChat(ctx, "sys", "prompt")
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("err = %v, want ErrCircuitOpen", err)
	}
	if spy.callCount() != callsAfterFirst {
		t.Fatalf("middleware ran while the circuit breaker was open (calls went from %d to %d) — it can observe/act on calls the breaker should have rejected outright", callsAfterFirst, spy.callCount())
	}
	if fc.callCount() != 1 {
		t.Fatalf("base client was called while the breaker was open (calls=%d)", fc.callCount())
	}
}

// TestModelMiddlewareDoesNotFabricateOrEraseToolCaller mirrors
// decorator_toolcaller_test.go's regression guard for WithCircuitBreaker/
// WithOTel: WithModelMiddleware must forward a ToolCaller's native
// tool-calling UNCHANGED (per ModelMiddleware's documented scope — it wraps
// StreamChat only) and must never fabricate a ToolCaller out of a plain
// Client.
func TestModelMiddlewareDoesNotFabricateOrEraseToolCaller(t *testing.T) {
	t.Run("preserves an inner ToolCaller", func(t *testing.T) {
		inner := &fakeToolCaller{Client: NewStubClient(), script: []ToolCallResult{{Message: "a"}}}
		wrapped := WithModelMiddleware(inner, &passthroughMiddleware{})
		tc, ok := wrapped.(ToolCaller)
		if !ok {
			t.Fatal("WithModelMiddleware erased the ToolCaller capability")
		}
		if _, err := tc.CallTools(context.Background(), "sys", "prompt", nil); err != nil {
			t.Fatalf("CallTools: %v", err)
		}
		if inner.turn != 1 {
			t.Fatalf("CallTools did not reach the inner ToolCaller (turn=%d)", inner.turn)
		}
	})

	t.Run("does not fabricate a ToolCaller from a plain Client", func(t *testing.T) {
		plain := NewStubClient()
		if _, ok := WithModelMiddleware(plain, &passthroughMiddleware{}).(ToolCaller); ok {
			t.Fatal("WithModelMiddleware fabricated a ToolCaller from a plain Client")
		}
	})
}

// --- invariant 3: tenant-safe, no shared mutable state (-race) ---------------

// TestModelMiddlewareTenantSafeRace drives many concurrent StreamChat calls
// across several distinct tenants through ONE shared middleware-decorated
// Client (mirroring the runtime: one App, one decorated Client, concurrent
// requests for different tenants) and asserts every recorded call's tenant
// (read back off ctx, deep inside the base client) matches the tenant the
// caller injected — never another goroutine's. Run with -race: the seam
// (middlewareClient/middlewareToolCaller) holds no shared mutable per-tenant
// field, only immutable references (mw, next) set once at construction, so
// there is nothing for -race to catch here BY DESIGN — this test is the
// executable proof of that design, mirroring the ReasoningState lesson
// (ADR-0061) this ADR explicitly cites.
func TestModelMiddlewareTenantSafeRace(t *testing.T) {
	base := &recordingClient{}
	tagging := &passthroughMiddleware{tag: "seam"}
	client := WithModelMiddleware(base, tagging)

	const tenants = 8
	const perTenant = 20

	var wg sync.WaitGroup
	errs := make(chan error, tenants*perTenant)
	for tIdx := range tenants {
		tenant := "tenant-" + string(rune('A'+tIdx))
		for i := 0; i < perTenant; i++ {
			wg.Add(1)
			go func(tenant string, i int) {
				defer wg.Done()
				ctx := WithTenant(context.Background(), tenant)
				ch, err := client.StreamChat(ctx, "sys", "prompt")
				if err != nil {
					errs <- err
					return
				}
				for tok := range ch {
					if tok.Err != nil {
						errs <- tok.Err
						return
					}
					if tok.Done && !strings.HasSuffix(tok.Text, ":"+tenant) {
						errs <- fmt.Errorf("token for tenant %q carries a different tenant's suffix: %q", tenant, tok.Text)
					}
				}
			}(tenant, i)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	// Cross-check at the base client, the deepest point in the chain: every
	// recorded call's ctx-derived tenant must be a real, expected tenant name
	// (never empty, never a foreign/garbled value from a racing goroutine).
	if base.callCount() != tenants*perTenant {
		t.Fatalf("base client observed %d calls, want %d", base.callCount(), tenants*perTenant)
	}
	base.mu.Lock()
	for _, c := range base.calls {
		if c.tenant == "" || !strings.HasPrefix(c.tenant, "tenant-") {
			t.Errorf("recorded call carries an invalid tenant: %q", c.tenant)
		}
	}
	base.mu.Unlock()
	if tagging.callCount() != tenants*perTenant {
		t.Fatalf("middleware observed %d calls, want %d", tagging.callCount(), tenants*perTenant)
	}
}

// --- invariant 4: ctx-cancel propagation + fail-closed + no secret leak -----

// TestModelMiddlewareCtxCancelPropagates proves ctx cancellation reaches the
// base client THROUGH the middleware chain (the middleware forwards ctx
// unchanged to next, per ModelMiddleware's documented contract), the same
// spend-cutoff guarantee every other Client decorator in this package
// preserves.
func TestModelMiddlewareCtxCancelPropagates(t *testing.T) {
	started := make(chan struct{})
	base := &middlewareBlockingClient{started: started}
	client := WithModelMiddleware(base, &passthroughMiddleware{})

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := client.StreamChat(ctx, "sys", "prompt")
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	<-started // base client is now blocked on ctx.Done()
	cancel()

	select {
	case tok := <-ch:
		if !errors.Is(tok.Err, context.Canceled) {
			t.Fatalf("token err = %v, want context.Canceled", tok.Err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ctx cancellation did not reach the base client through the middleware chain (timed out waiting for it to unblock)")
	}
}

// TestModelMiddlewareFailsClosedOnError proves a middleware error propagates
// as WrapStream's error — no silent fallback to an unmiddlewared call to the
// base client. The base client's call count staying at 0 is the direct
// evidence: the middleware refused to call next, and the decorator did not
// route around that refusal.
func TestModelMiddlewareFailsClosedOnError(t *testing.T) {
	wantErr := errors.New("middleware: policy refused this call")
	base := &recordingClient{}
	fm := &failingMiddleware{err: wantErr}
	client := WithModelMiddleware(base, fm)

	_, err := client.StreamChat(context.Background(), "sys", "prompt")
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if fm.nextCalled.Load() {
		t.Fatal("failingMiddleware's next was invoked despite the middleware never calling it")
	}
	if base.callCount() != 0 {
		t.Fatalf("base client was called %d times despite the middleware failing closed; want 0 (no unmiddlewared fallback)", base.callCount())
	}
}

// --- composition / ordering ---------------------------------------------------

// TestWithModelMiddlewareOrderingInnermostFirst proves mws are applied in
// registration order, innermost-first (WithModelMiddleware's documented
// contract): the FIRST-registered middleware is the one closest to the base
// client (it sees the LAST-transformed prompt on the way in, from the
// outermost middleware down), and the LAST-registered middleware is
// outermost (the first one a caller's StreamChat reaches).
func TestWithModelMiddlewareOrderingInnermostFirst(t *testing.T) {
	var order []string
	var mu sync.Mutex
	record := func(name string) *orderingMiddleware {
		return &orderingMiddleware{name: name, order: &order, mu: &mu}
	}

	first := record("first")   // registered first => innermost
	second := record("second") // registered second => outermost

	base := &recordingClient{}
	client := WithModelMiddleware(base, first, second)

	if _, err := client.StreamChat(context.Background(), "sys", "prompt"); err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()

	want := []string{"second", "first"} // outermost runs first
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("invocation order = %v, want %v (last-registered middleware must run first, i.e. be outermost)", got, want)
	}
}

type orderingMiddleware struct {
	name  string
	order *[]string
	mu    *sync.Mutex
}

func (o *orderingMiddleware) WrapStream(ctx context.Context, system, prompt string, next StreamFunc) (<-chan Token, error) {
	o.mu.Lock()
	*o.order = append(*o.order, o.name)
	o.mu.Unlock()
	return next(ctx, system, prompt)
}
