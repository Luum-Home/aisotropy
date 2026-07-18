# ADR-0062: Bounded model-middleware seam (`WithModelMiddleware`)

**Status:** proposed (2026-07-17)
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
