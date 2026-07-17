---
type: Component
title: "Primitives — per-user behavioral memory"
description: Friction-driven self-improvement of tool guidance metadata — the per-(tenant, user) primitive store, the friction detector on the agent route, and the hourly Consolidate pass that rewrites high-friction primitives with the HeuristicRewriter.
tags: [components, primitives, behavioral-memory, friction, tools, phase-6]
timestamp: 2026-07-14T00:00:00Z
---

# Primitives — per-user behavioral memory

The Phase 6 behavior axis ([memory tiers](../architecture/memory-tiers.md), "Primitive
self-improvement"): when a user repeatedly hits friction with a tool, the per-(tenant, user)
*guidance metadata* for that tool — its description and few-shot examples — is rewritten in the
primitive store. Executable code is never cloned or mutated; only the description/example overlay
is (the package has no dependency on `internal/tools`).

Status (2026-07-17): **REAL write-half + read-side overlay landed; runtime-live pending a
one-line store handoff.** The write + consolidation halves ARE wired unconditionally in
[`aisotropy/app.go`](../../aisotropy/app.go): a `primitives.Store` + `FrictionDetector` are
constructed at boot, every agent-route tool failure records friction, and a background goroutine
(`runPrimitiveConsolidation`) calls `primitives.Consolidate` with the `HeuristicRewriter` on an
hourly ticker (threshold `primitiveFrictionThreshold = 3.0`).

**The read side of the loop is now closed in code** (previously the gap called out in
[runtime-as-built.md §6](../architecture/runtime-as-built.md)): `RegisterAgent` accepts an
optional `*primitives.Store` and, per `POST .../chat/agent` request, calls `overlayRegistry`,
which reads `Store.Get(tenant, user, tool.Name)` for the request's OWN identity and overlays each
rewritten primitive's improved `Description` + accumulated `Examples` onto the base tool metadata
(via `overlayDescription`) BEFORE the tools reach the model — on both the native tool-calling and
the fallback `PromptFragment` paths. The overlay is a **literal text append**, not templating, and
is strictly **(tenant, user)-scoped** (user A's rewrite can never surface for user B). It is
**nil-safe**: with no store supplied, the model sees byte-for-byte the base registry metadata,
exactly as before. Consolidation revisions still do not survive a restart (in-memory Store).

The one remaining step to make this runtime-live is the **store handoff in `app.go`**: pass the
`FrictionDetector`'s store into `RegisterAgent` (`rt.frictionDetector.Store()`). Until that
argument is wired, the endpoint runs with no store and the overlay is inert — but the read-back
mechanism itself is implemented and covered by tests (see `chat_agent_overlay_test.go`).

## The data model

[`internal/primitives/primitives.go`](../../internal/primitives/primitives.go). A `Primitive`
is keyed by **(tenant, user, name)** — two users each get their own clone of the same tool's
guidance, and friction recorded for one pair never affects another (the tenant/user isolation
gate). Fields: `Description` (mutates over time), `Examples` (rewrites *append*, so corrective
guidance accumulates), `FrictionScore`, `Revision`, `UpdatedAt`.

`Store` is an **in-memory, thread-safe** registry (`NewStore`); it deliberately holds no
persistence backend — it is the working-set view the request path and the consolidation pass
share in-process, and a durable-backed Store can later slot in behind the same method set.
Consequence: primitive revisions do **not** survive a process restart today.

## Friction detection (the agent route)

[`internal/api/chat_agent.go`](../../internal/api/chat_agent.go): `RegisterAgent` receives the
`FrictionDetector`, and each `POST .../chat/agent` request wraps the host-supplied tool
executor in a per-request `frictionExecutor` scoped to that (tenant, user). When a tool
invocation **fails**, it records `primitives.SignalFailure` via `detector.Observe` —
best-effort, never affecting the request outcome.

Signal weights (`FrictionSignal.weight`): `SignalRetry` 1.0 (a reflection-loop retry),
`SignalFailure` 2.0 (outright failure), `SignalUserCorrection` 3.0 (the strongest, most
explicit signal). Today only `SignalFailure` has a wired producer; retry and user-correction
signals are defined but not yet emitted by any call site. `RecordFriction` creates the
primitive on first friction if none was `Put` yet, so tracking starts from the first failure.

## Consolidation (the rewrite pass)

`primitives.Consolidate(ctx, store, rewriter, threshold)` scans a `Snapshot` of the store and
rewrites every primitive whose `FrictionScore >= threshold` through a `Rewriter`, then `Put`s
the revision back. It is **idempotent**: a rewrite bumps `Revision` and resets `FrictionScore`
to 0, so a second pass with no new friction rewrites zero primitives.

The wired `Rewriter` is the **`HeuristicRewriter`** — deterministic and LLM-free: it sharpens
the description with a revision marker and appends a corrective example naming the primitive.
It exists so the loop is fully testable without a live model; an LLM-backed "optimizer agent"
can implement the same `Rewriter` interface as a drop-in replacement.

In `app.go`, `runPrimitiveConsolidation` runs this on the shared background-worker context
(cancelled during graceful shutdown) every `sleepCycleInterval` (1 hour) with threshold 3.0 —
i.e. roughly two failed invocations (2.0 + 2.0) cross it. Note this pass is independent of the
DataFusion [sleep-cycle](datafusion-sleep-cycle.md) binary: it runs even when
`AISOTROPY_SLEEPCYCLE_BIN` is unset.

## Code map

| Path | What it is |
|---|---|
| [`internal/primitives/primitives.go`](../../internal/primitives/primitives.go) | `Primitive`, `Store`, `FrictionDetector`, `FrictionSignal`, `Rewriter`, `HeuristicRewriter`, `Consolidate` |
| [`internal/api/chat_agent.go`](../../internal/api/chat_agent.go) | `frictionExecutor` — per-request friction recording on tool failure; `overlayRegistry`/`overlayDescription` — the read-side overlay that injects rewritten per-(tenant, user) guidance into the tool metadata the model sees |
| [`aisotropy/app.go`](../../aisotropy/app.go) | Boot wiring: store + detector construction, `runPrimitiveConsolidation` goroutine, `primitiveFrictionThreshold = 3.0`, hourly ticker, and the `RegisterAgent` store handoff that activates the read-side overlay |

## Gotchas

- **In-memory only.** Accrued friction and rewritten descriptions are lost on restart; a
  durable Store is the designed follow-up, not a config option today.
- **Consolidate is not designed to interleave with live traffic on the same primitive.** It
  snapshots, rewrites outside the lock, and `Put`s back — friction recorded concurrently with
  a primitive's own rewrite can be overwritten. It targets the offline batch window.
- **The read-side overlay is implemented but only activates when a store is handed to
  `RegisterAgent`.** `overlayRegistry` reads `Store.Get(tenant, user, name)` and injects the
  per-user overlay into the tool metadata the model sees; the detection + rewrite + read-back
  halves are all present in code. What remains is the `app.go` store handoff
  (`rt.frictionDetector.Store()`) that supplies a non-nil store — until then the endpoint runs
  nil-safe (base metadata only). The overlay is a literal append and never templates; it copies
  each tool's `RequiredScope`, so scope gating is unchanged by the overlay.

## Related decisions / references

- [Memory tiers](../architecture/memory-tiers.md) — "Primitive self-improvement (behavioral
  memory)", the design this implements.
- [Granular control](../architecture/granular-control.md) — scope-gated tools; primitives
  overlay the same tool surface.
- [Runtime as built](../architecture/runtime-as-built.md) — the reality ledger tracking this
  component's wiring status.
