---
type: ADR
title: "ADR-0062: Bounded model-middleware seam (WithModelMiddleware)"
description: >
  A user-pluggable model-middleware API, the safe/bounded counterpart to Vercel AI SDK's wrapLanguageModel, without weakening the constitutional pre-spend gate.
tags: [decisions, adr, llm, middleware, extensibility, cost-governance]
timestamp: 2026-07-17T00:00:00Z
---

# ADR-0062: Bounded model-middleware seam (`WithModelMiddleware`)

**Status:** implemented (2026-07-17)
**Delivers:** a user-pluggable model-middleware API, the safe/bounded counterpart to Vercel AI SDK's
`wrapLanguageModel` — without weakening the constitutional pre-spend gate.
**Related:** ADR-0061 (reasoning — the `ReasoningState` tenant-safety lesson), the extension-mechanism
research (`docs/research/arena-extension-mechanisms-and-aisotropy-candidates-2026-07-17.md`, where a
NAÏVE generic middleware was P3-skip). This ADR is the BOUNDED design that flips that skip.

## Context

aisotropy already has middleware as **named seams** (HTTP chi chain: CORS/RequestID/audit/recoverer/SSE;
LLM decorator chain: circuit-breaker → OTel → degrade → cost-tracker; guardrail input/output scanners).
What it lacks is a **user-pluggable** model-middleware — the thing that lets an adopter add logging,
caching, retry, prompt transformation, PII redaction, or prompt A/B *around the model call* without
forking the runtime.

The reason it was previously deferred: a NAÏVE generic middleware (an arbitrary `wrap(client)` a user
supplies) could be inserted OUTSIDE the constitutional gates and thereby bypass **quota-before-spend**
(`quota.Allow`, `internal/api/chat.go`) or the circuit breaker — a hard constitutional violation
(`.claude/rules/constitutional-gates.md` → "reject/degrade before spending tokens").

## Decision

Add a **bounded** model-middleware seam. The insight: the pre-spend quota gate runs at the **handler**
level (before the LLM client is ever called), and the breaker/OTel/degrade/cost decorators are
**runtime-controlled**. So user middleware inserted at a **fixed INNER position** of the client
decorator chain runs *after* the quota gate has already passed and *inside* the breaker/cost decorators
— it can transform prompts/params/responses but structurally **cannot** reorder or bypass any gate.

### API

```go
// ModelMiddleware wraps a single model call. `next` is the inner client's
// StreamChat. A middleware may transform system/prompt on the way in and/or
// wrap/observe the returned token stream on the way out.
type ModelMiddleware interface {
    WrapStream(ctx context.Context, system, prompt string, next StreamFunc) (<-chan Token, error)
}
type StreamFunc func(ctx context.Context, system, prompt string) (<-chan Token, error)

// Façade option — registers middleware at the FIXED inner position.
aisotropy.WithModelMiddleware(mw ModelMiddleware)  // composable; applied in registration order, innermost-first
```

### Fixed decorator ordering (the load-bearing invariant)

```
handler quota.Allow (pre-spend gate)      ← ALWAYS outermost, runtime-owned, middleware CANNOT reach it
  └─ circuitClient (breaker)              ← runtime-owned
      └─ otelClient / costTracker         ← runtime-owned
          └─ [ user ModelMiddleware chain ]   ← the ONLY place a user hook is inserted
              └─ base provider client (StreamChat)
```

The builder wires the user middleware chain at this fixed inner slot; the runtime decorators are always
applied around it. A user middleware therefore sees an already-quota-approved, already-breaker-gated
call, and its own token spend still flows through the cost tracker.

## Invariants (non-negotiable — the merge-blocker tests)

1. **Cannot bypass the pre-spend gate.** A middleware that tries to "call the model early" still cannot
   reach the handler-level `quota.Allow`; and the breaker/cost decorators remain outside it. *Test:* a
   middleware over a client whose tenant is over-quota never causes an un-gated spend.
2. **Cannot bypass the circuit breaker.** *Test:* with the breaker open, a user middleware's `next`
   returns `ErrCircuitOpen`; the middleware cannot re-open or route around it.
3. **Tenant-safe.** Middleware is applied per-request over the request-scoped ctx; the seam holds **no
   shared mutable per-tenant state** (the `ReasoningState` lesson — a goroutine-local/`ctx` value, never
   a keyed map). *Test:* two tenants' concurrent middleware invocations never cross (`-race`).
4. **ctx-respecting + fail-closed + no secret leak.** ctx cancellation propagates through the
   middleware to `next`; a middleware error fails the call closed (no silent unmiddlewared fallback);
   the seam never logs prompts/keys.
5. **Backward-compatible.** No middleware registered ⇒ byte-identical behaviour to today.

## Reuse of gollem primitives

Before implementing, the codebase's dependency (`github.com/fugue-labs/gollem` v0.5.1,
MIT) was checked for an existing middleware/interceptor system rather than hand-rolling
one blind — the same discipline `.claude/rules/prune-evidence-gate.md` requires for
deletion claims applies symmetrically to reuse claims: verify against the actual source,
not a memory of what a dependency "probably" has.

**Finding (verified via `go list -m -f '{{.Dir}}' github.com/fugue-labs/gollem` +
reading the source directly):** gollem's `core` package already ships an agent-scoped
middleware/interceptor system —
`core.AgentMiddleware`/`core.RequestMiddlewareFunc`/`core.AgentStreamMiddleware` +
`core.WithAgentMiddleware[T]` (`core/agent_middleware.go`), and
`core.MessageInterceptor`/`core.ResponseInterceptor` +
`core.WithMessageInterceptor[T]`/`core.WithResponseInterceptor[T]` with the ready-made
`core.RedactPII`/`core.AuditLog` (`core/interceptor.go`). This is **not new information
to the codebase**: `internal/llm/gollem.go`'s `gollemGuardrailOptions` already attaches
`core.RedactPII`/`core.AuditLog` to every `core.Agent[T]` this runtime builds (ADR-0010) —
that wiring is pre-existing and unrelated to this ADR.

**Layer analysis — why this ADR does NOT route through `core.WithAgentMiddleware`:**

Aisotropy has three LLM call paths, not one:

1. **Reflection route** (`Register`/`App.Turn` → `ChatCore` → `chatReflect` →
   `GollemReflect` → a `core.Agent[T]` built over `GollemModel`).
2. **Native-tool-calling agent route** (`RegisterAgent` → `RunToolAgent` → another
   `core.Agent[string]` over `GollemModel`).
3. **Direct-streaming route** (`RegisterStream`/`RegisterAsk`/`RegisterAISDK`/
   `RegisterAGUI` → `StreamCore` → `Client.StreamChat` directly — confirmed by reading
   `internal/api/stream_core.go`: it never constructs a `core.Agent[T]` at all).

`core.WithAgentMiddleware` is a `core.Agent[T]` option. It can only ever reach paths 1
and 2 — it structurally **cannot** reach path 3, because path 3 builds no `Agent[T]` for
it to attach to. So adopting it would, at best, be a partial answer that still needs a
`Client`-level seam for the direct-streaming route.

The `Client`-level seam this ADR builds (`internal/llm/middleware.go`'s
`WithModelMiddleware`, inserted once at `aisotropy/app.go`'s `startRuntime`, wrapping the
raw provider client before `WithCircuitBreaker`/`WithOTel`) does not have that gap: every
one of the three paths above ultimately resolves to the SAME decorated `Client` —
confirmed by reading `aisotropy/app.go`'s wiring: `api.Register(..., rt.client, ...)`,
`api.RegisterAgent(..., rt.client, ...)`, and
`api.RegisterStream/RegisterAsk/RegisterAISDK/RegisterAGUI(..., rt.streamClient, ...)`
all pass a client derived from the one `middlewaredClient := llm.WithModelMiddleware(llmClient,
a.ext.modelMiddleware...)` built once. `GollemModel` (`internal/llm/gollem.go`) is itself
just a thin adapter over this package's `Client` — its `Request`/`RequestStream` call
straight through to `m.client.StreamChat` — so a middleware registered once is inherited
by the reflection and agent-text paths automatically, with zero additional wiring.

Given that, layering `core.WithAgentMiddleware` on top as well would be **strictly
worse**, not merely redundant:

- It would **double-wrap** the reflection/agent-text branches (the `Client` those
  `Agent[T]`s call is already middleware-decorated).
- It would **silently widen scope** on the agent path only: `core.Agent[T].Request()`
  internally branches into `GollemModel.requestWithTools` (native tool-calling) for that
  one path, so an `AgentMiddleware` attached there would also intercept tool-calls —
  something this ADR's own API (`ModelMiddleware.WrapStream`, a streaming-token seam)
  deliberately does not cover anywhere else. A middleware author's mental model
  ("my middleware sees every model call the same way") would silently break depending on
  which route served the request.

**Conclusion:** the single `Client`-level seam is adopted as the ADR's sole mechanism.
gollem's `core.WithAgentMiddleware`/interceptors are not layered in additionally. The
pre-existing internal use of `core.RedactPII`/`core.AuditLog` inside
`gollemGuardrailOptions` (ADR-0010) is untouched by this decision — it is a different,
already-shipped feature (the built-in PII/audit guardrail on `Agent[T]` runs) operating at
a different abstraction layer (`[]core.ModelMessage`, not `(system, prompt string)`). The
worked example (`aisotropy/model_middleware_example_test.go`) demonstrates *bridging* into
that existing library rather than reimplementing it: it adapts `core.RedactPII` into a
`ModelMiddleware` via a small conversion shim, so an adopter who wants gollem's regex-based
redaction at this seam can reuse it instead of writing their own.

## Consequences

- Adopters get the `wrapLanguageModel` value (logging/caching/retry/transform/PII-redaction/A-B) as a
  first-class, tenant-isolated, gate-safe seam — a real extension-mechanism upgrade (extends ADR-0016's
  extensibility story alongside MCP-serve, ADR-candidate P1).
- **Non-goals:** middleware does NOT get to see or move the quota gate; there is no "outer" middleware
  slot (that would be the naïve, unsafe design); middleware is not a place for cross-tenant caching
  unless the cache is itself tenant-scoped (documented caveat).

## Implementation plan (Small–Medium, additive)

1. `internal/llm/middleware.go` — the `ModelMiddleware`/`StreamFunc` types + a `withMiddleware(client, mws...)`
   decorator applied at the fixed inner slot in the client-construction path (`internal/llm/registry.go`
   / `FromEnv`), inside breaker/cost.
2. `aisotropy/extend.go` — `WithModelMiddleware` option (accumulates), matching the existing `With*` style.
3. Tests (merge blockers): quota-bypass-impossible, breaker-bypass-impossible, tenant-isolation `-race`,
   ctx-cancel + fail-closed, backward-compat (none registered).
4. `examples/` snippet — a trivial logging/transform middleware.
