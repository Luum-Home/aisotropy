---
type: ADR
title: "ADR-0010 — Gollem adoption scope, and the two-path observability decision"
description: An explicit adopt / adopt-later / do-not-adopt matrix for Gollem v0.5.1's capability surface beyond the Phase-2 reflection loop, plus how OTel GenAI semantic conventions apply across aisotropy's two distinct LLM call paths.
tags: [decisions, adr, gollem, observability, otel, cost, guardrails, llm]
timestamp: 2026-07-08T00:00:00Z
---

# ADR-0010 — Gollem adoption scope, and the two-path observability decision

## Status

**✅ Accepted (2026-07-08).** Grounded in the capability analysis at
`external/GOLLEM-ANALYSIS.md` (gitignored working note, reverse-engineered against the vendored
`external/gollem` v0.5.1 clone). This ADR does not change any code — it records the scope decision
for future Gollem-adoption work and the observability instrumentation split between aisotropy's two
LLM call paths.

## Context

Aisotropy integrates Gollem (`github.com/fugue-labs/gollem`, MIT) per
[ADR-0008](0008-reflection-loop-gollem-deferral.md), but today's integration
(`internal/llm/gollem.go`) uses roughly 2% of Gollem's surface: `core.Model` (via `GollemModel`
wrapping our narrow `llm.Client`), `core.NewAgent[T]`, and `core.RunWithReflection[T]`. Gollem
v0.5.1 ships production-grade implementations of far more — cost tracking, guardrails,
interceptors, OTel GenAI-semconv tracing, autocontext summary-buffer compaction, a durable
orchestrator (SQLite-backed stores + lease recovery), a `KnowledgeBase` interface, snapshots/resume,
and multi-agent orchestration.

A deep read-only analysis (see `external/GOLLEM-ANALYSIS.md`) cross-referenced every Gollem
capability against aisotropy's own architecture docs
([`observability.md`](../architecture/observability.md),
[`consistency.md`](../architecture/consistency.md),
[`granular-control.md`](../architecture/granular-control.md)) to answer: which Gollem capabilities
should aisotropy adopt outright, which are worth stealing as a pattern only, and which conflict with
already-accepted architectural decisions (ADR-0004's rqlite Raft-ACK, ADR-0009's provider-agnostic
`Client` seam, the temperature-tiered BadgerDB/rqlite/Qdrant memory design)?

This ADR is the record of that scope decision, so future work doesn't re-litigate it piecemeal
each time a Gollem feature looks tempting.

## Options considered

### A — Adopt Gollem's `ext/*` tree broadly (otel + memory + orchestrator + middleware)
Rejected wholesale. `ext/memory` (SQLite stores) and `ext/orchestrator` (SQLite-backed durable task
engine) import a second, weaker durability model that directly competes with the rqlite
Raft-quorum-ACK the Transactional Outbox requires (ADR-0004, see
[`consistency.md`](../architecture/consistency.md)). Importing broadly also pulls
gin/fiber/echo/bubbletea/temporal/websocket transitively, which undercuts the sovereign
small-binary target.

### B — Do not adopt anything beyond the current `core.Model` + `RunWithReflection[T]` integration
Rejected as too conservative. Several `core` capabilities (cost tracking, guardrails, interceptors,
OTel attribute conventions) are one `AgentOption` away from being live on the `Agent[T]` we already
build in `GollemReflect`, cost nothing architecturally, and directly serve already-accepted design
docs (`observability.md`'s `gen_ai.usage.*`, the "never log secrets" constitutional gate). Leaving
them unused is wasted leverage on a dependency we already carry.

### C — Selective, per-capability adopt / adopt-later / do-not-adopt matrix (chosen)
Score each Gollem capability against: (1) does it operate only within `Agent[T].Run` (our current
integration surface) or does it require widening the `llm.Client` seam / owning provider HTTP
(ADR-0009 conflict)? (2) does it assume a storage substrate that competes with BadgerDB/rqlite/Qdrant
(ADR-0004/0007 conflict)? (3) is it accounting/instrumentation (safe, additive) or does it own
prompt assembly / run-loop decisions (competes with aisotropy's own memory-hydration and
tool-scoping design)? This is the minimal-effort, maximal-leverage path: take the free wins, defer
the conditional ones, and explicitly reject the ones that fight already-accepted ADRs.

## Decision

**Option C.** The following adoption matrix is accepted for Gollem v0.5.1 capabilities beyond the
current `core.Model` / `RunWithReflection[T]` integration:

### ADOPT NOW

| Capability | Where it attaches | Why now |
|---|---|---|
| Gollem OTel GenAI semantic-convention **attribute constants** (`gen_ai.system`, `gen_ai.request.model`, `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`) | Reflection/`Agent[T]` path (`GollemReflect`) | These are the exact attributes [`observability.md`](../architecture/observability.md) already re-specifies by hand. Using the same official `go.opentelemetry.io/otel` SDK and Gollem's constants avoids re-deriving a schema that already exists. |
| `core.CostTracker` + `ModelPricing` + `EstimateRunCost`, attached via `WithCostTracker` | Same `Agent[T]` built in `GollemReflect` | One `AgentOption` on an agent we already construct; gives provider-normalized per-model cost accounting immediately, feeding the per-tenant cost dashboard `observability.md` describes. Accounting only — see the explicit **non**-adoption of `UsageQuota` as a circuit breaker, below. |
| `core` guardrails (`WithInputGuardrail`, `WithTurnGuardrail`, built-in `ContentFilter`/`MaxTurns`) + interceptors (`RedactPII`, `AuditLog` via `core.interceptor.go`) | Same `Agent[T]` | Directly serves the constitutional gate "secrets/PII never logged" (see `.claude/rules/constitutional-gates.md`) at zero new architectural surface — both are `AgentOption`s on the agent already built for reflection. |

### ADOPT PATTERN, NOT CODE

| Capability | Aisotropy's own answer | Why pattern-only |
|---|---|---|
| `ext/orchestrator`'s durable task engine (`EventStore`, `RecoveryManager` with lease-reclaim, immutable `ArtifactStore`, cold-restart reconciliation) | Phase-5 BadgerDB **Transactional Outbox** + sleep-cycle consolidation + cold-boot reconciliation (see [`consistency.md`](../architecture/consistency.md)) | Same *machine* (durable event log, orphan reclaim after a crash, at-least-once semantics), wrong *substrate*: `ext/orchestrator` is SQLite-backed (`modernc.org/sqlite`) and run-scoped, while aisotropy's Outbox must be a single BadgerDB ACID `Txn` co-writing chat message + `outbox:pending` event, with rqlite's Raft-quorum ACK as the durable sink of Phase 2 (ADR-0004). Importing `ext/orchestrator` would mean carrying two competing durability models. **Steal the interface shapes** (`EventStore`, lease-recovery semantics, the "recovery is just the same worker with more accumulated work" design) as a reference blueprint; implement on BadgerDB + rqlite. |

### DO NOT ADOPT

| Capability | Conflict |
|---|---|
| `core.autocontext.go` summary-buffer compaction (`AutoContextConfig`, `WithAutoContext`) | Conflicts with the Phase-2 **async, off-hot-path** Summary Buffer design and with [Bet 2's](../research/bet-02-working-memory-codec.md) lossless-codec thesis. Gollem's autocontext is lossy LLM-summarization keyed to one `Agent[T].Run` (in-loop, synchronous); aisotropy's buffer is persisted per `tenant:user:session` in BadgerDB and condensed asynchronously. Different lifetime, different philosophy. |
| `ext/memory` SQLite-backed stores (`SQLiteStore`, `BufferMemory`, store-backed `KnowledgeBase` impls) | Competes directly with the temperature-tiered BadgerDB (working memory) / rqlite (facts, FTS5) / Qdrant (semantic vectors) design — see [`memory-tiers.md`](../architecture/memory-tiers.md) and ADR-0004/0007. A second persistence engine for the same data is pure duplication with weaker guarantees. |
| Auto-`Retrieve` `core.KnowledgeBase` (calling `Retrieve` *inside* the agent run, before each turn) | Breaks the hard boundary that Gollem is a pure **consumer** of already-hydrated memory — hydration happens earlier, in parallel, by aisotropy's own memory-tier goroutines (BadgerDB + rqlite + Qdrant, per [`granular-control.md`](../architecture/granular-control.md) and the request lifecycle). Auto-retrieve would relocate hydration onto the Gollem hot path and duplicate/contradict the parallel-hydration step. |

### BUILD OURSELVES (not Gollem)

| What | Why not `core.UsageQuota` |
|---|---|
| Per-tenant, **pre-provider** token-bucket circuit breaker (local tokenizer estimates prompt cost *before* calling the provider; degrade to a cheaper model or `429` at ~95% quota) | This is the cost axis of [`granular-control.md`](../architecture/granular-control.md), tied to [Bet 4 — compressibility routing](../research/bet-04-compressibility-routing.md)'s graceful-degradation threshold. Gollem's `UsageQuota` is **run-scoped and post-hoc**: it terminates an in-progress `Agent[T].Run` after limits are hit *within one run*, with no cross-request per-tenant bucket, no persistence, and no model-downgrade path. It cannot serve as the breaker that must reject/degrade **before spending a single token** — that requires our own BadgerDB-backed bucket in front of the provider call, not an in-agent enforcement point. |

### Guardrail on deeper adoption

Any adoption beyond this matrix — in particular anything that would require widening `llm.Client`
toward native tool-calling, or letting a Gollem-native `Model`/provider own the HTTP call to the LLM
— is **out of scope without a dedicated ADR revisiting ADR-0009**. That would subordinate
ADR-0009's provider-agnostic `Registry` (and Bet-4's per-request routing plan) to Gollem's own
provider layer, and is exactly the kind of seam-widening the sovereign/small-binary thesis and the
"zero OpenAI-isms in the interface" guardrail were designed to prevent. Keep the adapter at
`internal/llm/gollem.go` the thinnest seam it already is.

## Observability decision: two LLM call paths, two instrumentation strategies

Aisotropy has **two distinct LLM call paths**, and they are not symmetric with respect to Gollem:

1. **Streaming chat path** — `internal/api/chat_stream.go` calls `llm.Client.StreamChat` **directly**.
   It does not go through Gollem's `Agent[T]` at all; this is the main, hot, user-facing SSE path.
2. **Structured-output / reflection path** — `GollemReflect` (`internal/llm/gollem.go`) builds a
   `core.Agent[T]` and drives it via `core.RunWithReflection[T]`.

Gollem's `ext/otel` package instruments **only path (b)** — it wraps `Agent[T].Run`'s internal spans
(agent → subagent → tool → guardrail → output-validation) with the GenAI semantic-convention
attributes. It has no visibility into path (a), because path (a) never constructs a Gollem agent.

**Decision:** adopt Gollem's `gen_ai.*` **attribute conventions** for consistency across both paths,
but instrument them with two different mechanisms:

- **Path (a) — our own provider-agnostic `WithOTel(Client)` decorator.** A wrapper around
  `llm.Client` (consistent with ADR-0009's `Client` seam — it decorates the interface, it does not
  bypass it) that emits **one `gen_ai` span per `StreamChat` call**, carrying:
  - `gen_ai.system` (provider name, from `Client.Name()`)
  - `gen_ai.request.model`
  - `tenant_id` / `user_id` / `product_id` (per [`observability.md`](../architecture/observability.md)'s
    identity-and-isolation attributes, sourced from the same `ctx` [multi-tenancy](../architecture/multi-tenancy.md)
    already propagates)
  - TTFT (time to first token) and total duration
  - token counts, when the underlying provider response exposes them
  This keeps the main, user-facing path's observability aligned with ADR-0009's `Client` seam — it
  instruments the interface every provider implementation already satisfies, with no dependency on
  Gollem at all.
- **Path (b) — Gollem's `ext/otel`**, used as-is when the reflection/`Agent[T]` path is exercised in
  production. Its span tree already covers the agent/tool/guardrail/validation internals that our
  own decorator has no equivalent for (there is no "tool" or "guardrail" concept on the bare
  `Client` path).

Both paths converge on the same attribute vocabulary, so a cost/latency dashboard built against
`gen_ai.*` does not need to special-case which path produced a given span.

**Bits/token/tenant (Bet 6) note:** per [`observability.md`](../architecture/observability.md#cross-reference-with-the-compression-thesis-bitstokentenant-lives-here),
`aisotropy.bpt` (derived from logprobs, no extra inference pass) is a derived OTel attribute
attached to the same `gen_ai.completion` span — this applies identically to whichever of the two
paths produced the span, and is not itself a Gollem capability; it rides alongside `gen_ai.usage.*`
regardless of instrumentation source. See [Bet 6](../research/bet-06-bits-per-token-metric.md).

## Consequences

**Positive**
- Cost accounting, guardrails/PII-redaction, and OTel attributes become available on the reflection
  path essentially for free — they are `AgentOption`s on the `Agent[T]` `GollemReflect` already
  constructs, with no new dependency and no seam change.
- The Phase-5 consistency design (Outbox + cold-boot reconciliation, ADR-0004's rqlite ACK) is
  preserved untouched — `ext/orchestrator`'s SQLite substrate is explicitly rejected as code, while
  its interface shapes are preserved as a reference blueprint.
- The streaming chat path (the main user-facing path) gets first-class, provider-agnostic
  observability without depending on Gollem, keeping ADR-0009's `Client` seam as the single point of
  instrumentation for every current and future provider.
- The provider-agnostic `Client` seam (ADR-0009) and the sovereign/small-binary thesis are protected
  by an explicit guardrail: no further Gollem adoption without a dedicated ADR if it would touch
  tool-calling or provider ownership.

**Negative / technical debt**
- Two instrumentation code paths (our `WithOTel(Client)` decorator vs. Gollem's `ext/otel`) must be
  kept attribute-consistent by convention, not by a shared implementation — a schema drift between
  the two is possible if one is updated without the other.
- The pre-provider per-tenant token-bucket circuit breaker (granular-control.md's cost axis) remains
  entirely unbuilt; this ADR only clarifies that it must be built in-house rather than borrowed from
  `core.UsageQuota` — it does not implement it.
- `ext/orchestrator`'s pattern-adoption is a design reference only; the actual BadgerDB Outbox +
  cold-boot reconciliation implementation is still Phase-5 future work (tracked in
  [`consistency.md`](../architecture/consistency.md)).
- No code changed as part of this ADR — the "ADOPT NOW" items are accepted as scope, not yet wired.
  Follow-up implementation work should reference this ADR rather than re-deriving the matrix.

## References

- [`external/GOLLEM-ANALYSIS.md`](../../external/GOLLEM-ANALYSIS.md) — the full capability analysis this ADR is grounded in (gitignored working note).
- [ADR-0008 — Phase 1 reflection loop](0008-reflection-loop-gollem-deferral.md) — the original decision to defer Gollem to Phase 2.
- [ADR-0009 — Provider-agnostic, multi-connection LLM layer](0009-provider-agnostic-llm-layer.md) — the `Client` seam this ADR's guardrail protects.
- [`docs/architecture/observability.md`](../architecture/observability.md) — the `gen_ai.*` attribute vocabulary and per-tenant cost dashboard this ADR aligns with.
- [`docs/architecture/consistency.md`](../architecture/consistency.md) — the BadgerDB Outbox + cold-boot reconciliation design `ext/orchestrator`'s pattern is mined for.
- [`docs/architecture/granular-control.md`](../architecture/granular-control.md) — the cost-axis circuit breaker this ADR says must be built in-house, not via `core.UsageQuota`.
- [Bet 4 — Compressibility routing](../research/bet-04-compressibility-routing.md) — the graceful-degradation mechanism the pre-provider breaker integrates with.
- [Bet 6 — Bits-per-token-per-tenant](../research/bet-06-bits-per-token-metric.md) — the North Star metric riding as a derived OTel attribute on both LLM call paths.
- Current implementation: `internal/llm/gollem.go` (`GollemModel`, `GollemReflect`), `internal/llm/llm.go` (`Client` interface).
- [Decisions index](index.md)
