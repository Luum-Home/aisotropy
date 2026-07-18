package aisotropy

// model_middleware_test.go is the demonstrable, merge-blocker evidence for
// ADR-0062's invariant 1 (docs/decisions/0062-bounded-model-middleware.md):
// a registered ModelMiddleware cannot bypass the handler-level pre-spend
// quota gate. internal/llm/middleware_test.go proves the Client-decorator
// invariants (breaker-bypass, tenant-safety, ctx-cancel/fail-closed,
// backward-compat) at the internal/llm layer; this file proves the
// end-to-end behavioral claim at the layer the quota gate actually lives in
// (internal/api, driven here through the real pipelines: App.Turn ->
// ChatCore for the non-streaming route, and api.StreamCore directly for the
// direct-streaming route) — the ONLY layer that can prove it, since
// internal/llm must not import internal/api (see internal/llm/otel.go's
// package doc on that layering rule).
//
// It also proves the positive-path complement — the middleware IS invoked,
// for a request the quota gate allows — so the "not invoked" result on the
// denied call is evidence of the gate, not of a broken wiring.

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Luum-Home/aisotropy/internal/api"
)

// spyModelMiddleware is a ModelMiddleware that records each invocation and
// forwards to next unchanged.
type spyModelMiddleware struct {
	calls atomic.Int32
}

func (s *spyModelMiddleware) WrapStream(ctx context.Context, system, prompt string, next StreamFunc) (<-chan Token, error) {
	s.calls.Add(1)
	return next(ctx, system, prompt)
}

// TestWithModelMiddlewareInvokedOnAllowedTurn proves a registered
// ModelMiddleware IS invoked on a turn the quota gate allows — the positive
// complement to TestWithModelMiddlewareCannotBypassQuotaGate below, so a
// zero-invocation result there is evidence of the gate, not of the seam being
// silently unwired.
func TestWithModelMiddlewareInvokedOnAllowedTurn(t *testing.T) {
	spy := &spyModelMiddleware{}
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9), WithModelMiddleware(spy))
	defer closeApp(t, app)()

	if _, err := app.Turn(context.Background(), ChatInput{TenantID: "t", UserID: "u", SessionID: "s", Prompt: "hello"}); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if spy.calls.Load() == 0 {
		t.Fatal("registered ModelMiddleware was never invoked on an allowed turn")
	}
}

// TestWithModelMiddlewareCannotBypassQuotaGate is the ADR-0062 invariant-1
// merge-blocker for the non-streaming pipeline: it exhausts a tenant's
// per-tenant token bucket (WithRateLimit(0, 0) — burst is floored to 1 by
// memory.Limiter's NewLimiter, and a zero refill rate means the SECOND call
// for the same tenant is unconditionally denied) and proves that on the
// denied call:
//
//  1. ChatCore's quota gate rejects the request (a 429 huma.StatusError,
//     internal/api/chat.go's chatCheckQuota — reached BEFORE ChatCore ever
//     calls chatReflect / the LLM Client, see internal/api/core.go).
//  2. The registered ModelMiddleware's WrapStream is NOT invoked for that
//     denied call — proving the middleware has no way to observe, let alone
//     act on, a call the quota gate already rejected. A middleware sitting at
//     the FIXED INNER Client-decorator slot (internal/llm/middleware.go)
//     structurally cannot run before a gate that lives entirely outside the
//     Client call it decorates.
//
// The first call (before quota exhausts) is asserted to succeed AND invoke
// the middleware, so the test's premise (working middleware, working quota
// gate) is itself verified rather than assumed.
func TestWithModelMiddlewareCannotBypassQuotaGate(t *testing.T) {
	spy := &spyModelMiddleware{}
	// burst floored to 1 by NewLimiter (internal/memory/quota.go), rate 0 => no
	// refill: exactly one request per tenant is ever allowed.
	app := New(WithLLM(fastStub()), WithRateLimit(0, 0), WithModelMiddleware(spy))
	defer closeApp(t, app)()

	ctx := context.Background()
	tenant := "quota-tenant"

	// First call: within quota. Must succeed AND invoke the middleware.
	if _, err := app.Turn(ctx, ChatInput{TenantID: tenant, UserID: "u", SessionID: "s1", Prompt: "hello"}); err != nil {
		t.Fatalf("first turn (within quota): %v", err)
	}
	callsAfterFirst := spy.calls.Load()
	if callsAfterFirst == 0 {
		t.Fatal("middleware was not invoked on the first (within-quota) turn — test premise broken, cannot trust the second assertion")
	}

	// Second call, same tenant: quota is exhausted (bucket had exactly 1
	// token, no refill). ChatCore's chatCheckQuota must reject this BEFORE
	// the middleware-decorated Client is ever called.
	_, err := app.Turn(ctx, ChatInput{TenantID: tenant, UserID: "u", SessionID: "s2", Prompt: "hello again"})
	if err == nil {
		t.Fatal("expected the second turn to be rejected by the quota gate")
	}
	var se huma.StatusError
	if !errors.As(err, &se) || se.GetStatus() != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want a 429 huma.StatusError (rate limit exceeded)", err)
	}

	if spy.calls.Load() != callsAfterFirst {
		t.Fatalf("middleware was invoked on the quota-denied call (calls went from %d to %d) — a registered ModelMiddleware must never run for a request the pre-spend quota gate rejected",
			callsAfterFirst, spy.calls.Load())
	}
}

// TestWithModelMiddlewareStreamCoreCannotBypassQuotaGate mirrors the test
// above for the DIRECT-STREAMING pipeline (internal/api/stream_core.go's
// StreamCore — the /chat/stream, /ask, and AI-SDK/AG-UI protocol-adapter
// routes), which never builds a Gollem core.Agent[T] and calls the decorated
// Client directly (unlike ChatCore's reflection loop). It drives
// api.StreamCore in-process against api.StreamDeps built from the App's own
// runtime — exactly the dependencies aisotropy/app.go's api.RegisterStream
// wires the real /chat/stream HTTP route with (rt.streamClient, rt.limiter,
// ...) — so this exercises the identical pipeline without standing up an HTTP
// server.
func TestWithModelMiddlewareStreamCoreCannotBypassQuotaGate(t *testing.T) {
	spy := &spyModelMiddleware{}
	app := New(WithLLM(fastStub()), WithRateLimit(0, 0), WithModelMiddleware(spy))
	defer closeApp(t, app)()

	rt, err := app.ensureRuntime()
	if err != nil {
		t.Fatalf("ensureRuntime: %v", err)
	}

	deps := api.StreamDeps{
		Client:  rt.streamClient,
		WM:      rt.wm,
		Store:   rt.factsStore,
		Limiter: rt.limiter,
		ToolReg: rt.toolRegistry,
		Meter:   rt.costMeter,
		Persona: rt.persona,
		GW:      rt.gateway,
		Guard:   rt.guard,
	}
	sink := api.StreamSink{
		OnToken: func(string) error { return nil },
		OnDone:  func(api.StreamFinal) {},
	}
	tenant := "quota-tenant-stream"
	ctx := context.Background()

	// First call: within quota, must invoke the middleware.
	firstIn := &api.ChatInput{TenantID: tenant, UserID: "u", Body: api.ChatRequest{Prompt: "hello", SessionID: "s1"}}
	if err := api.StreamCore(ctx, deps, firstIn, sink); err != nil {
		t.Fatalf("first stream (within quota): %v", err)
	}
	callsAfterFirst := spy.calls.Load()
	if callsAfterFirst == 0 {
		t.Fatal("middleware was not invoked on the first (within-quota) stream — test premise broken")
	}

	// Second call, same tenant: quota exhausted. StreamCore's inline quota
	// check must reject this before ever calling active.StreamChat.
	secondIn := &api.ChatInput{TenantID: tenant, UserID: "u", Body: api.ChatRequest{Prompt: "hello again", SessionID: "s2"}}
	if streamErr := api.StreamCore(ctx, deps, secondIn, sink); streamErr == nil {
		t.Fatal("expected the second stream to be rejected by the quota gate")
	}
	if spy.calls.Load() != callsAfterFirst {
		t.Fatalf("middleware was invoked on the quota-denied stream (calls went from %d to %d)", callsAfterFirst, spy.calls.Load())
	}
}
