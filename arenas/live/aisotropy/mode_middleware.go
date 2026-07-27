// mode_middleware.go — the `middleware` mode: drives ONE qa-shaped turn
// through aisotropy's NATIVE, bounded model-middleware seam
// (aisotropy.WithModelMiddleware, ADR-0062, docs/decisions/0062-bounded-model-middleware.md,
// committed bc2d0b0) rather than a hand-rolled wrapper around the LLM client.
//
// The seam's whole point (see ADR-0062 + internal/llm/middleware.go) is that
// a registered ModelMiddleware is installed at a FIXED INNER slot of the
// runtime's LLM client decorator chain:
//
//	handler quota.Allow (pre-spend gate)   <- ALWAYS outermost, runtime-owned
//	  circuitClient (breaker)              <- runtime-owned
//	    otelClient / costTracker           <- runtime-owned
//	      [ user ModelMiddleware chain ]   <- the ONLY place this mode's hook sits
//	        base provider client
//
// So arenaMiddleware below runs strictly AFTER the constitutional pre-spend
// quota check has already passed and strictly INSIDE the circuit breaker —
// it structurally cannot reorder or bypass either gate, unlike the naive
// "wrap the whole client" approach ADR-0062 explicitly rejected. This is the
// honest differentiator the task calls out: none of the other framework
// adapters in this arena have a middleware seam that is bounded this way.
package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

// middlewareSentinel is appended to the system prompt on the way IN, so the
// inbound transform is observable independent of the model's actual reply
// text (which is nondeterministic) — what matters is that the mutated
// system prompt is the one that reaches `next` (the next decorator, or the
// base provider client), not a copy the middleware discarded.
const middlewareSentinel = "\n[ARENA_MW]"

// arenaMiddleware is a real aisotropy.ModelMiddleware implementation
// (WrapStream is the ONLY entry point ADR-0062's seam ever calls — see
// internal/llm/middleware.go's middlewareClient.StreamChat) performing two
// genuine, observable transforms around the real model call:
//
//   - INBOUND: appends middlewareSentinel to `system` BEFORE calling `next`
//     — the mutated value, not the original, is what the wrapped client (and
//     ultimately the base provider) actually receives.
//   - OUTBOUND: relays every Token from the inner stream through this
//     middleware's own goroutine before handing it to the caller, so the
//     middleware genuinely sits in the data path of the response (not just a
//     pass-through channel alias) and can observe/count/transform each
//     token — the same shape a logging/caching/retry middleware would use.
type arenaMiddleware struct {
	invocations   int64
	inboundFired  int64
	outboundFired int64
}

var _ aisotropy.ModelMiddleware = (*arenaMiddleware)(nil)

// WrapStream implements aisotropy.ModelMiddleware. See the type doc above for
// what "inbound"/"outbound" mean here.
func (m *arenaMiddleware) WrapStream(ctx context.Context, system, prompt string, next aisotropy.StreamFunc) (<-chan aisotropy.Token, error) {
	atomic.AddInt64(&m.invocations, 1)

	// INBOUND transform: this concatenation IS the transform — the sentinel
	// is appended to the value passed to `next`, not to a discarded copy.
	mutatedSystem := system + middlewareSentinel
	atomic.AddInt64(&m.inboundFired, 1)

	upstream, err := next(ctx, mutatedSystem, prompt)
	if err != nil {
		// fail-closed (ModelMiddleware's contract, internal/llm/middleware.go):
		// propagate next's error as-is, never swallow it for a silent
		// unmiddlewared fallback.
		return nil, err
	}

	// OUTBOUND transform: relay through THIS goroutine so every token
	// genuinely passes through the middleware (not an untouched channel
	// alias) before reaching the caller.
	out := make(chan aisotropy.Token)
	go func() {
		defer close(out)
		for tok := range upstream {
			atomic.AddInt64(&m.outboundFired, 1)
			select {
			case out <- tok:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// fired reports the middleware's own accounting: total WrapStream
// invocations, and whether the inbound/outbound hooks fired at least once.
func (m *arenaMiddleware) fired() (invocations int, inboundFired, outboundFired bool) {
	return int(atomic.LoadInt64(&m.invocations)),
		atomic.LoadInt64(&m.inboundFired) > 0,
		atomic.LoadInt64(&m.outboundFired) > 0
}

// middlewareInfo is the `middleware`-mode "middleware" stdout field.
type middlewareInfo struct {
	Mechanism     string `json:"mechanism"`
	InboundFired  bool   `json:"inbound_fired"`
	OutboundFired bool   `json:"outbound_fired"`
	Invocations   int    `json:"invocations"`
	Supported     bool   `json:"supported"`
}

// middlewareOutput is the `middleware`-mode stdout contract — it does NOT
// reuse the shared `result` envelope (see emitFinal in main.go), matching
// the observability mode's precedent (mode_observability.go's
// observabilityOutput) of a distinct, purpose-built shape:
// {framework,mode,text,middleware{mechanism,inbound_fired,outbound_fired,
// invocations,supported}[,error]}.
type middlewareOutput struct {
	Framework  string          `json:"framework"`
	Mode       string          `json:"mode"`
	Text       string          `json:"text"`
	Middleware *middlewareInfo `json:"middleware"`
	Error      string          `json:"error,omitempty"`
}

// runMiddleware builds an App wired with aisotropy's NATIVE, bounded
// model-middleware seam (aisotropy.WithModelMiddleware, ADR-0062) and drives
// ONE qa-shaped turn through it via App.Turn (the same in-process chat
// surface `qa` mode uses — full pipeline parity, see mode_qa.go), then
// reports whether the middleware's inbound (system-prompt transform) and
// outbound (token-stream wrap) hooks genuinely fired.
func runMiddleware(req *request, out *result) error {
	res := &middlewareOutput{Framework: frameworkName, Mode: modeMiddleware}
	// Stash immediately: even if the turn below fails partway, emitFinal
	// (main.go) still encodes THIS struct — with res.Error set — instead of
	// an empty envelope.
	out.Structured = res

	turn, err := lastUserMessage(req.Messages)
	if err != nil {
		res.Error = err.Error()
		return err
	}

	agnes, err := newProviderClient()
	if err != nil {
		res.Error = err.Error()
		return err
	}

	mw := &arenaMiddleware{}
	app := newApp(agnes, req.System, aisotropy.WithModelMiddleware(mw))
	defer func() { _ = app.Close() }()

	sess := sessionID(req)
	logf("middleware: running one App.Turn through aisotropy.WithModelMiddleware (ADR-0062, fixed inner decorator slot — strictly inside the pre-spend quota gate + circuit breaker)")
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	turnOut, terr := app.Turn(ctx, aisotropy.ChatInput{
		TenantID:  arenaTenant,
		UserID:    arenaUser,
		Prompt:    turn,
		SessionID: sess,
		ProductID: productID,
	})
	cancel()
	out.LatencyMs = time.Since(start).Milliseconds()
	setUsage(out, app)

	invocations, inboundFired, outboundFired := mw.fired()
	res.Middleware = &middlewareInfo{
		Mechanism:     "aisotropy.WithModelMiddleware",
		InboundFired:  inboundFired,
		OutboundFired: outboundFired,
		Invocations:   invocations,
		Supported:     true,
	}

	if terr != nil {
		res.Error = fmt.Errorf("App.Turn failed: %w", terr).Error()
		return terr
	}
	res.Text = turnOut.Reply
	out.Reply = turnOut.Reply

	logf("middleware: fired %d invocation(s), inbound_fired=%v outbound_fired=%v — the seam ran strictly INSIDE the constitutional pre-spend quota gate and circuit breaker (ADR-0062's fixed inner decorator slot: base -> WithModelMiddleware -> WithCircuitBreaker -> WithOTel), never able to reorder or bypass either — unlike an unbounded client-wrapper middleware would be able to",
		invocations, inboundFired, outboundFired)
	return nil
}
