// ADR-0062: the bounded, user-pluggable model-middleware seam
// (docs/decisions/0062-bounded-model-middleware.md). This is the safe
// counterpart to Vercel AI SDK's wrapLanguageModel: it lets an adopter attach
// logging, caching, retry, prompt transformation, PII redaction, or prompt A/B
// AROUND the model call, without ever being able to reorder or bypass the two
// constitutional gates (pre-spend quota, circuit breaker).
//
// # Why this is safe: the fixed insertion point
//
// The pre-spend quota gate (memory.Limiter.Allow, invoked as chatCheckQuota /
// the inline check in StreamCore) runs entirely at the HANDLER level, in
// internal/api, BEFORE the handler ever calls a method on this package's
// Client — see internal/api/core.go's ChatCore (chatCheckQuota at line ~60,
// strictly before selectClient/chatReflect) and internal/api/stream_core.go's
// StreamCore (the quota check block, strictly before active.StreamChat). A
// ModelMiddleware only ever runs INSIDE a Client method call (WrapStream is
// reached exclusively via Client.StreamChat), so it has no way to run before,
// or independently of, a call the handler already gated.
//
// The circuit breaker (WithCircuitBreaker, breaker.go) and the OTel/cost
// decorators are runtime-owned and applied AFTER this decorator, at
// aisotropy/app.go's startRuntime: WithModelMiddleware wraps the BASE
// provider client, and WithCircuitBreaker then wraps THAT — so the breaker is
// always the client the caller (and any Gollem-driven path — GollemModel is
// itself just a thin adapter over this same Client, see gollem.go) actually
// invokes. Concretely: base -> WithModelMiddleware -> WithCircuitBreaker ->
// WithOTel. When the breaker is OPEN, circuitClient.admit() fails fast and
// NEVER calls its inner client — so a registered ModelMiddleware's WrapStream
// is not even invoked while the breaker is open; it structurally cannot
// re-open or route around it (see middleware_test.go,
// TestModelMiddlewareCannotBypassCircuitBreaker).
//
// # One seam, every call path
//
// Because the decorator wraps the base Client once, at construction, every
// consumer of that Client inherits it identically — there is no second,
// divergent wiring to keep in sync: the non-streaming reflection path
// (ChatCore -> chatReflect -> GollemReflect -> GollemModel.Request ->
// Client.StreamChat), the native-tool-calling agent path's text/answer
// branch (RegisterAgent -> RunToolAgent -> GollemModel.Request), and the
// direct-streaming path (RegisterStream/RegisterAsk/RegisterAISDK/RegisterAGUI
// -> StreamCore -> Client.StreamChat, which never builds a Gollem
// core.Agent[T] at all) all resolve to the SAME decorated Client
// (aisotropy/app.go's protectedClient / streamClient — see that file's
// startRuntime for the exact call sites). A middleware registered once via
// aisotropy.WithModelMiddleware therefore observes every text/stream call in
// the runtime uniformly.
//
// # Deliberately out of scope: native tool-calling
//
// ModelMiddleware wraps StreamChat only (the API this ADR defines is
// WrapStream/StreamFunc — a streaming-token seam). It does NOT wrap
// ToolCaller.CallTools: WithModelMiddleware preserves an inner ToolCaller's
// native tool-calling capability UNCHANGED, exactly like WithCircuitBreaker
// and WithOTel already do (see decorator_toolcaller_test.go) — a decorator
// that dropped ToolCaller would silently disable the native tool-calling path
// at its call site. gollem's own core.WithAgentMiddleware (which DOES wrap a
// core.Agent[T]'s Request() as a whole, including its internal tool-calling
// branch) was evaluated and deliberately NOT adopted here: it cannot reach
// the direct-streaming path at all (that path builds no core.Agent[T]), so it
// could only ever be a partial answer requiring this same Client-level seam
// anyway, and layering it on top of an already-middleware-decorated Client
// would double-wrap the reflection/agent-text branches while asymmetrically
// widening middleware scope to tool-calls on the agent path only. See
// docs/decisions/0062-bounded-model-middleware.md, "Reuse of gollem
// primitives".
package llm

import "context"

// StreamFunc is the shape of a streaming model call: given a system prompt
// and a user prompt, it returns a channel of Token (closing on completion,
// error, or ctx cancellation — see Client.StreamChat's contract in llm.go).
// A Client's own StreamChat method value is directly assignable to StreamFunc
// (identical underlying signature), so `next` in ModelMiddleware.WrapStream is
// usually just the next decorator's (or the base client's) StreamChat.
type StreamFunc func(ctx context.Context, system, prompt string) (<-chan Token, error)

// ModelMiddleware wraps a single streaming model call. Implementations may
// transform system/prompt on the way in (e.g. redact PII, inject a prefix,
// pick a cached response) and/or observe or wrap the returned token stream on
// the way out (e.g. logging, metrics, retry-on-empty).
//
// Contract implementations MUST honor:
//   - ctx-respecting: forward ctx unchanged (or a context.Context derived from
//     it via context.WithCancel/WithTimeout, never a detached
//     context.Background()) to next, so cancellation set by the caller keeps
//     propagating all the way to the base provider client — the runtime's only
//     spend-cutoff mechanism (see llm.go's Client doc).
//   - fail-closed: an error returned by WrapStream (whether from next or from
//     the middleware's own logic) MUST propagate as WrapStream's error, never
//     be swallowed in favor of a silent unmiddlewared fallback call. If a
//     middleware needs a fallback, it must call next itself and handle that
//     call's own error explicitly — there is no runtime-provided bypass.
//   - no secret leak: a middleware must not log the raw system/prompt content
//     or any provider credential; this package's own decorators (breaker,
//     OTel) never do, and a middleware sitting in the same chain must hold the
//     same bar.
//   - at most once: call next at most once per WrapStream invocation (calling
//     it zero times — e.g. to serve a cached reply as a synthetic channel — is
//     valid; calling it more than once double-spends against the provider and
//     is a misuse of the seam).
type ModelMiddleware interface {
	// WrapStream is invoked in place of the wrapped client's StreamChat. next
	// is the next inner call — either the next registered middleware, or the
	// base provider client's StreamChat once the chain is exhausted.
	WrapStream(ctx context.Context, system, prompt string, next StreamFunc) (<-chan Token, error)
}

// middlewareClient decorates a single Client with a single ModelMiddleware.
// WithModelMiddleware chains these, one per registered middleware.
type middlewareClient struct {
	mw   ModelMiddleware
	next Client
}

func (m *middlewareClient) Name() string { return m.next.Name() }

// StreamChat invokes the middleware, handing it the wrapped client's own
// StreamChat as `next`. This is the ONLY place a ModelMiddleware is ever
// invoked from — there is no other entry point, which is what makes the
// "cannot run before the handler's quota gate" invariant hold structurally
// (see the package doc above).
func (m *middlewareClient) StreamChat(ctx context.Context, system, prompt string) (<-chan Token, error) {
	return m.mw.WrapStream(ctx, system, prompt, m.next.StreamChat)
}

// middlewareToolCaller preserves an inner ToolCaller's native tool-calling
// capability through the middleware decorator, exactly like
// circuitToolCaller (breaker.go) and otelToolCaller (otel.go): CallTools is
// forwarded UNCHANGED to the inner ToolCaller — ModelMiddleware's contract is
// StreamChat-only (see the package doc's "Deliberately out of scope"
// section), so tool-calls never pass through WrapStream.
type middlewareToolCaller struct {
	*middlewareClient
	tc ToolCaller
}

var _ ToolCaller = (*middlewareToolCaller)(nil)

func (m *middlewareToolCaller) CallTools(ctx context.Context, system, prompt string, tools []ToolSpec) (ToolCallResult, error) {
	return m.tc.CallTools(ctx, system, prompt, tools)
}

// wrapOneMiddleware decorates next with mw, preserving next's ToolCaller
// capability (if any) unchanged, per middlewareToolCaller's doc.
func wrapOneMiddleware(mw ModelMiddleware, next Client) Client {
	base := &middlewareClient{mw: mw, next: next}
	if tc, ok := next.(ToolCaller); ok {
		return &middlewareToolCaller{middlewareClient: base, tc: tc}
	}
	return base
}

// WithModelMiddleware composes client with mws at the FIXED INNER slot of the
// runtime's decorator chain (ADR-0062): it wraps ONLY the base client, so
// aisotropy/app.go applies it BEFORE WithCircuitBreaker/WithOTel — those
// runtime-owned decorators always end up OUTSIDE it, never the reverse. mws
// are applied in registration order, innermost-first: the first element of
// mws ends up wrapping client directly (closest to the base call), and each
// subsequent element wraps the previous result (so the LAST registered
// middleware is outermost — the first one a caller's StreamChat reaches, and
// the last one to see the base client's raw response on the way out).
//
// len(mws) == 0 (no middleware registered) returns client completely
// unchanged — not even a zero-op wrapper — so a runtime with no
// WithModelMiddleware calls is byte-for-byte identical to the pre-ADR-0062
// behavior (invariant 5, backward-compatible).
func WithModelMiddleware(client Client, mws ...ModelMiddleware) Client {
	wrapped := client
	for _, mw := range mws {
		wrapped = wrapOneMiddleware(mw, wrapped)
	}
	return wrapped
}
