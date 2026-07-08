---
type: ADR
title: "ADR-0008 — Phase 1 reflection loop: in-house loop now, Gollem in Phase 2"
description: Phase 1 uses a minimal in-house reflection loop (Reflect[T]) instead of integrating Gollem right away; Gollem integration (core.RunWithReflection[T]) is deferred to Phase 2 with an explicit plan.
tags: [decisions, adr, gollem, reflection, phase-1, tech-debt]
timestamp: 2026-07-08T00:00:00Z
---

# ADR-0008 — Phase 1 reflection loop: in-house loop now, Gollem in Phase 2

## Status

**✅ Accepted (2026-07-08).** [Phase 1](../roadmap/milestones.md) of the walking skeleton uses an
in-house reflection loop (`internal/llm/reflect.go`, `Reflect[T]`). Integrating
[Gollem](../components/gollem-orchestrator.md) is **deferred to Phase 2**. This ADR reconciles
the decision with the architecture, which names Gollem as the cognitive orchestrator.

## Context

Aisotropy's architecture names [Gollem](../components/gollem-orchestrator.md) as the cognitive
"kernel," responsible for structured output and the **reflection loop / output repair**
(see [overview](../architecture/overview.md#llm-os) and [request lifecycle](../architecture/request-lifecycle.md)).
While implementing Phase 1 (SSE streaming + the first LLM call), a decision had to be made on
whether to integrate Gollem now or cover the self-repair pattern with in-house code.

Two conditions of the environment weighed on the decision:
1. **No `ANTHROPIC_API_KEY`** is present in the current development environment → the real LLM
   path can't be exercised, only compiled. Any benefit Gollem provides over the reflection
   loop isn't yet verifiable.
2. **The walking skeleton prioritizes a verifiable end-to-end flow** (SSE + `ctx` cancellation)
   over completing the orchestration.

## Options considered

### A — Integrate Gollem in Phase 1
`github.com/fugue-labs/gollem` v0.5.1 exposes `core.RunWithReflection[T]` and `Agent[T]`, which
do exactly what the component's docs call for. It requires: wrapping `llm.Client` behind the
`core.Model` interface (`Request` / `RequestStream` / `ModelName`) and setting up an `Agent[T]`
with its own schema derivation. Non-trivial load that, moreover, can't be exercised without an
API key.

### B — Minimal in-house reflection loop now, Gollem in Phase 2 (chosen)
`internal/llm/reflect.go` implements `Reflect[T]`: **generate → validate against the struct →
if it fails, re-request with the exact error → cap at N attempts**. Generic, a handful of
lines, with 3 passing unit tests. Gollem comes in Phase 2 once there's an API key to validate
parity.

## Decision

**Option B.** Phase 1 uses the in-house `Reflect[T]`; Gollem is integrated in Phase 2. It's a
sequencing decision, not a rejection of Gollem: **Gollem remains the target orchestrator.**

## Consequences

**Positive**
- Phase 1 was **verified with no blocker** (build/vet/test OK, end-to-end SSE, `ctx`
  cancellation).
- The reflection contract stays **provider-independent** (the `llm.Client` interface), which
  makes it easy to plug in Gollem — or another provider — behind it without touching the
  handler.

**Negative / technical debt**
- **Two reflection paths** until migration (the in-house one and, later, Gollem's). Risk of
  drift.
- **Explicit TODO** in `internal/llm/reflect.go` documenting the upgrade.
- **Temporary divergence** from [gollem-orchestrator.md](../components/gollem-orchestrator.md):
  this ADR makes it explicit and time-boxed (Phase 2).

## Integration plan (Phase 2)

1. Wrap `llm.Client` in a `core.Model` adapter (`Request`/`RequestStream`/`ModelName`).
2. Replace `Reflect[T]` with Gollem's `core.RunWithReflection[T]` + `Agent[T]`.
3. Validate **with a real `ANTHROPIC_API_KEY`** that Gollem's output repair behaves like the
   in-house one (parity) and measures the same or better in retries.
4. Demote `Reflect[T]` to a fallback (or remove it) and update the component's docs.

## References

- Gollem v0.5.1 — `core.RunWithReflection[T]`, `Agent[T]`, `core.Model`: <https://github.com/fugue-labs/gollem>
- Current implementation: `internal/llm/reflect.go` (+ `reflect_test.go`).
- [Gollem component](../components/gollem-orchestrator.md) · [Request lifecycle](../architecture/request-lifecycle.md) · [Milestones — Phase 1/2](../roadmap/milestones.md)
