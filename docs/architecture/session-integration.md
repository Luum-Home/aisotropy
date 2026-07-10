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
| **`facts.NewScope` / `ScopeFromContext`** (ADR-0011 reconciliation) | The identity path of every handler + the consolidation sink | 4 call sites route facts identity through `internal/tenancy`; `runtime-as-built.md §2.1` cites it as the built behavior |
| **Real token-usage plumbing** (`llm.Usage` on the terminal token) | The **cost meter** (`internal/costmeter`) | The streaming drain loop calls `meter.Record(tenant, tok.Usage.Input, tok.Usage.Output, cost)` — the meter consumes exactly the real counts the plumbing exposes |
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
| Cost metering | ❌ | ✅ | ❌ |
| Memory hydration (read) | working+facts (semantic store passed nil) | full (working+facts+semantic) | ❌ |
| Consolidation (write to long-term memory) | ✅ | ✅ | ❌ |
| OTel gen_ai span | ❌ | ✅ | ❌ |

The consequence that matters: **the fail-closed schema-enforcement gate lives only on `/chat`, but
the full-featured route people actually use is `/chat/stream`, which forwards raw tokens with no
schema enforcement.** The gate the thesis leans on hardest is on the least-used route.

### 2.2 Two cost systems that diverge

Real usage flows into two separate accountants:

- **`costmeter`** (streaming path): has real prices ($0.15/$0.60 per 1M) → produces non-zero cost,
  surfaced at `GET /costs`.
- **Gollem `CostTracker`** (reflection path): `gollemModelPricing` is intentionally empty → cost
  stays $0.

The usage-plumbing verification proved token *counts* flow correctly into both, but the dollar
figure only materializes on the streaming side. `GET /costs` reflects only the streaming meter — the
reflection path's spend is invisible to it.

### 2.3 Consolidation now on both chat routes (recently closed gap)

An earlier state had `/chat` read facts but persist via plain `Append`, so its turns never fed the
outbox → facts/semantic pipeline. Commit `85caf46` closed it: `/chat` now calls `AppendWithOutbox`,
so **both** chat routes grow long-term memory. Only `/chat/agent` remains non-consolidating (folds
into the §2.1 asymmetry). Kept as a worked example of a cross-session seam that was found *and*
closed: this integration pass caught the stale claim in `runtime-as-built.md §6` (verified against
code) and corrected it there — the two docs now agree.

### 2.4 Built-but-unwired seams

`internal/routing` (ADR-0007 tenant tiering) and `internal/primitives` (friction-driven rewriting)
are implemented and tested but nothing in the request path imports/drives them — the same
"implemented but not exercised" pattern the reflection loop had before it was wired. Designed seams
awaiting adoption, not dead ends.

## 3. Unified debt

The verification layer's debt ledger ([`phases-3-7-as-built.md`](phases-3-7-as-built.md) §Debt) and
the runtime doc's "not yet wired" ([`runtime-as-built.md`](runtime-as-built.md) §6) are the same
backlog from two angles. Merged and de-duplicated:

1. **Schema enforcement is route-asymmetric** — `/chat/stream` (the primary route) has no
   fail-closed reflection. (Seam 2.1 + runtime §6.)
2. **Cost is double-counted / route-asymmetric** — reconcile `costmeter` and Gollem `CostTracker`,
   or wire a real per-model price table so both agree. (Seam 2.2.)
3. **SSE ctx-cancellation gate has no dedicated test.** (Matrix debt #1.)
4. **274 Rust crates unreviewed for license; sleep-cycle dormant by default.** (Matrix #2 + runtime §3.)
5. **`facts.Scope`/`semantic.Scope` unexport hardening deferred.** (Matrix #3 + ADR-0011 residual.)
6. **Routing + primitives built but unwired.** (Seam 2.4 + runtime §6.)
7. **Placeholders in production seams** — `HashEmbedder`, empty pricing table. (Both docs.)
8. **`/chat/agent` carries none of memory/quota/cost.** (Seam 2.1.)

## 4. The one-line takeaway

This is **one runtime with its verification, reconciliation, and positioning layer already
interwoven** — not two separate codebases. The highest-value seam to close is the **endpoint
capability asymmetry**: the constitutional gates (fail-closed output, quota, cost) apply unevenly
across the three routes, and the most-used route (`/chat/stream`) is missing the schema-enforcement
gate the thesis depends on.

## Cross-links

- [runtime-as-built.md](runtime-as-built.md) — the as-wired narrative.
- [phases-3-7-as-built.md](phases-3-7-as-built.md) — the gate→test verification matrix.
- [competitive-landscape.md](../research/competitive-landscape.md) — the moat this build realizes.
- [ADR-0011](../decisions/0011-tenant-scoped-statement-builder.md) — the facts↔tenancy reconciliation.
