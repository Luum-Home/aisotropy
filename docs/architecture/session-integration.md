---
type: Architecture Reference
title: "Session integration — how the runtime build and its verification layer compose"
description: A connection map between the two bodies of work on this repo — the runtime implementation (Phases 3-7, the memory tiers, Phase 7 control) and its verification/reconciliation/positioning layer (usage plumbing, summarizer bound, facts↔tenancy reconciliation, competitive landscape, gate→test matrix). Shows where they compose, the cross-session seams visible only when looking at both, and the unified debt.
tags: [architecture, integration, as-built, verification, seams, coherence]
timestamp: 2026-07-09T00:00:00Z
---

# Session integration — how the build and its verification layer compose

Two bodies of work landed on this repo in close succession: a **runtime build** (Phases 3-7 — the
facts/semantic/outbox/sleep-cycle tiers and the Phase 7 control machinery) and a
**verification/reconciliation/positioning layer** (real token-usage plumbing, the bounded
summarizer, the facts↔tenancy reconciliation, the competitive landscape, and the gate→test
coverage matrix). This document is the seam map between them: where they compose cleanly, the
coherence gaps only visible when you look at both together, and the single unified debt list.

Its companions: [`runtime-as-built.md`](runtime-as-built.md) (what boots) and
[`phases-3-7-as-built.md`](phases-3-7-as-built.md) (what proves it). This one answers: **do the two
efforts form one coherent system, or two overlapping ones?**

## 1. Where the verification layer is live inside the runtime

These are not parallel efforts — the verification/reconciliation work is wired into the running
path, not bolted alongside it. Each row is confirmed in code (not just intended):

| Verification-layer contribution | Composes with | How it connects |
|---|---|---|
| **`facts.NewScope` / `ScopeFromContext`** (ADR-0011 reconciliation) | The identity path of every handler + the consolidation sink | 6 call sites route facts identity through `internal/tenancy` (both chat handlers, the bitrate hydrator, the health probe, the consolidation sink, and the sleep-cycle coordinator); `runtime-as-built.md §2.1` cites it as the built behavior |
| **Real token-usage plumbing** (`llm.Usage` on the terminal token) | The **cost meter** (`internal/costmeter`) | The streaming drain loop calls `meter.Record(in.TenantID, tok.Usage.InputTokens, tok.Usage.OutputTokens, cost)` — the meter consumes exactly the real counts the plumbing exposes |
| **Bounded summarizer** (54%→72% measured) | The working-memory tier the runtime hydrates from | `hydrateSystemPrompt` reads `Summary()`; the bound keeps that block from growing unbounded |
| **Reflection wiring** (`/chat` → `ReflectAuto`, fail-closed 502) | One of the three live endpoints | It is the runtime's only fail-closed structured-output route |
| **Bet 6 instrumentation** (`internal/bitrate`, North Star metric) | The competitive thesis + `research/` Bet 6 | The streaming path now reserves ~5% held-out turns for offline bits-per-token measurement, excluded from consolidation to avoid leakage — the metric the [competitive landscape](../research/competitive-landscape.md) named as the differentiator is now wired |
| **Gate→test matrix** (`phases-3-7-as-built.md`) | The runtime narrative (`runtime-as-built.md`) | Complementary docs: one narrates *how* it is wired, the other proves *what* enforces each gate |
| **Competitive landscape** | The whole built stack | It named "tiered memory + tenant-isolation-by-construction + pre-spend quota" as the open lane no incumbent covers — the runtime build is the realization of that lane |

**Verdict:** they compose. The runtime is the system; the verification layer is woven into it, not a
separate track.

## 2. Cross-session seams (visible only looking at both)

These are the coherence gaps that neither effort surfaces alone — they emerge at the join.

### 2.1 Endpoint capability asymmetry (the biggest)

The three live routes each carry a *different subset* of the constitutional gates:

| Gate / capability | `/chat` (reflection) | `/chat/stream` (streaming) | `/chat/agent` (tool loop) |
|---|:--:|:--:|:--:|
| Fail-closed structured output (502) | ✅ | ❌ | partial (post-parse tool re-check) |
| Quota before spend | ✅ | ✅ | ❌ |
| Cost metering | ✅ | ✅ | ❌ |
| Memory hydration (read) | working+facts (semantic store passed nil) | full (working+facts+semantic) | ❌ |
| Consolidation (write to long-term memory) | ✅ | ✅ | ❌ |
| OTel gen_ai span | ❌ | ✅ | ❌ |

**Resolution (2026-07-10): most of this asymmetry is an INTENTIONAL per-route contract, not a bug.**
The three routes are three different contracts, and the constitutional gates apply *per contract*:
- **`/chat`** is the **structured-output** contract → the "structured output fails closed" gate
  applies, and it does (`ReflectAuto` → 502). Fail-closed schema enforcement is *inherently* a
  whole-object contract; a streaming route cannot validate a complete JSON object mid-stream without
  buffering the whole reply and defeating streaming.
- **`/chat/stream`** is the **free-text streaming** contract → the gate that applies to it is
  "streaming respects ctx cancellation immediately" (constitutional-gates.md), which it does
  (client disconnect cuts the upstream call, partial reply not persisted — now covered by
  `TestChatStream_ClientCancellationStopsStream`). It deliberately does *not* enforce a JSON schema,
  because it does not expect structured output.
- **`/chat/agent`** is the **tool-loop** contract → its enforcement is the post-parse tool-name
  re-check against the permitted set.

So "`/chat/stream` lacks fail-closed" is by design, not a gap. The genuinely-worth-closing
asymmetries were the *cost-metering* one (`/chat` did not record to the cost meter — now unified,
see the debt list) and OTel coverage on `/chat`; the fail-closed one is a category difference
between a structured contract and a streaming one. (Confirmed against code 2026-07-14: `/chat`
records to the same shared `costMeter` as `/chat/stream` via `chatReflectAndRecord` →
`meter.Record`, so the cost-metering cell above is now ✅ and that asymmetry is closed; `/chat` is
still registered with the un-instrumented `protectedClient` — not the OTel-wrapped `streamClient` —
so the OTel-span gap remains the one open item.)

### 2.2 Cost accounting: two meters, unified at `/costs` (updated 2026-07-14)

There are still two accountants in the tree, but they no longer diverge at the `/costs` surface:

- **`costmeter`** — the shared meter (`aisotropy/app.go` builds ONE `costmeter.New()` and passes it
  to BOTH `Register` and `RegisterStream`). It has real prices ($0.15/$0.60 per 1M) and is what
  `GET /costs` reads (`costMeter.Snapshot()`).
- **Gollem `CostTracker`** — the reflection path's *internal* tracker, whose `gollemModelPricing`
  is intentionally empty → its own figure stays $0.

Both chat routes now record to the shared `costmeter`: `/chat/stream` in its drain loop, and
`/chat` via `chatReflectAndRecord` → `costmeter.EstimateCostUSD(...)` → `meter.Record` on the SAME
$0.15/$0.60 basis, using Gollem's real provider-reported `Usage`. So `GET /costs` now reflects BOTH
routes, not only streaming. What remains: Gollem's internal `CostTracker` still shows $0 (cosmetic —
`/costs` does not read it), and the homegrown `Reflect[T]` fallback backend reports zero `Usage`, so
it records $0 cost for that (non-default) path.

### 2.3 Consolidation now on both chat routes (recently closed gap)

An earlier state had `/chat` read facts but persist via plain `Append`, so its turns never fed the
outbox → facts/semantic pipeline. Commit `85caf46` closed it: `/chat` now calls `AppendWithOutbox`,
so **both** chat routes grow long-term memory. Only `/chat/agent` remains non-consolidating (folds
into the §2.1 asymmetry). Kept as a worked example of a cross-session seam that was found *and*
closed: this integration pass caught the stale claim in `runtime-as-built.md §6` (verified against
code) and corrected it there — the two docs now agree.

### 2.4 Wired-but-dormant seams (updated 2026-07-14)

An earlier state had `internal/routing` (ADR-0007 tenant tiering) and `internal/primitives`
(friction-driven rewriting) implemented and tested but undriven. Both are now wired, though
under-exercised by default:

- **`internal/routing`** — `aisotropy/app.go` builds a
  `routing.NewRouter(routing.NewStaticPolicy(dedicatedTenants...))` and threads it through
  `openFactsStore`/`openSemanticStore`. It only routes to per-tenant dedicated stores when dedicated
  tenants are configured; the default policy is empty, so the store falls back to the shared pool
  and routing is a no-op — wired, dormant by default.
- **`internal/primitives`** — the `/chat/agent` route wraps the host tool executor in a
  `frictionExecutor` that calls `detector.Observe(tenant, user, tool, SignalFailure)` on a failing
  tool, and a background `runPrimitiveConsolidation` worker later rewrites high-friction primitives.
  So primitives IS driven on the agent request path, not merely built.

Neither is a dead end: routing awaits a tenant-tiering config, and primitives awaits adoption beyond
the agent route.

## 3. Unified debt

The verification layer's debt ledger ([`phases-3-7-as-built.md`](phases-3-7-as-built.md) §Debt) and
the runtime doc's "not yet wired" ([`runtime-as-built.md`](runtime-as-built.md) §6) are the same
backlog from two angles. Merged and de-duplicated:

1. **Schema enforcement is route-asymmetric** — `/chat/stream` (the primary route) has no
   fail-closed reflection. Per §2.1 this is an intentional per-route contract (streaming cannot
   validate a whole JSON object mid-stream), so it is a tracked-and-accepted design point, not an
   open bug. (Seam 2.1 + runtime §6.)
2. **Gollem's internal `CostTracker` still shows $0** (empty `gollemModelPricing`) — cosmetic only:
   `/costs` reads the shared `costmeter`, which BOTH chat routes now record to (route-symmetric as
   of 2026-07-14), so tenant spend is surfaced regardless. Residual: the homegrown `Reflect[T]`
   fallback reports zero `Usage`. (Seam 2.2.)
3. ~~**SSE ctx-cancellation gate has no dedicated test.**~~ Closed (2026-07-14): covered by
   `TestChatStream_ClientCancellationStopsStream` in `internal/api/chat_stream_test.go` (see §2.1).
   (Matrix debt #1.)
4. **274 Rust crates unreviewed for license; sleep-cycle dormant by default.** (Matrix #2 + runtime §3.)
5. **`facts.Scope`/`semantic.Scope` unexport hardening deferred.** (Matrix #3 + ADR-0011 residual.)
6. **Routing + primitives wired but dormant/under-exercised** — `internal/routing` routes only when
   dedicated tenants are configured (empty by default → shared pool); `internal/primitives` is driven
   by the `/chat/agent` route (friction on tool failure) + the background consolidation worker.
   (Seam 2.4 + runtime §6.)
7. **Placeholders in production seams** — `HashEmbedder`, empty pricing table. (Both docs.)
8. **`/chat/agent` carries none of memory/quota/cost.** (Seam 2.1.)

## 4. The one-line takeaway

This is **one runtime with its verification, reconciliation, and positioning layer already
interwoven** — not two separate codebases. On the endpoint "asymmetry": most of it is an
**intentional per-route contract** (structured `/chat` vs. streaming `/chat/stream` vs. tool-loop
`/chat/agent`), with each constitutional gate applying to the contract it belongs to (§2.1). The
genuine gaps this pass surfaced were `/chat` not recording cost (closed 2026-07-14 — it now records
to the shared `costMeter`) and its still-missing OTel span (the one remaining item worth closing);
the fail-closed-schema difference is a category distinction, not a hole.

## Cross-links

- [runtime-as-built.md](runtime-as-built.md) — the as-wired narrative.
- [phases-3-7-as-built.md](phases-3-7-as-built.md) — the gate→test verification matrix.
- [competitive-landscape.md](../research/competitive-landscape.md) — the moat this build realizes.
- [ADR-0011](../decisions/0011-tenant-scoped-statement-builder.md) — the facts↔tenancy reconciliation.
