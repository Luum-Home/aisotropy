---
type: Concept
title: Request lifecycle
description: The step-by-step journey of a request in Aisotropy, from HTTP to SSE and the asynchronous sleep cycle — responsible component, what travels in the ctx, and failure modes per step.
tags: [architecture, request-lifecycle, huma, gollem, ctx, sse, reflection-loop]
timestamp: 2026-07-08T00:00:00Z
---

# Request lifecycle

The [overview](overview.md#lifecycle-of-a-request) summarizes the journey of a request in 7
steps. This document expands each step: what it does, which component is responsible, what
information travels in Go's `context.Context` at that point, and what can fail. The common thread
across all 7 steps is one and the same: **Go's `ctx`**, which carries identity and cancellation
from the first millisecond to the last, with no global state (see
[isolation via ctx](../vision/principles.md#derived-operating-principles)).

## Diagram of the full journey

```
[HTTP Client] ── request ──►[Huma]
                                 │
                    1. validates contract + extracts ctx{tenant_id,user_id,scopes}
                                 │
                                 ▼
                    2. quota control (BadgerDB token-bucket) + circuit breaker
                                 │
                    ┌────────────┴────────────┐
                    │  quota exhausted?        │
                    │  yes → degrade model or  │
                    │       429                │
                    └────────────┬────────────┘
                                 │ quota OK
                                 ▼
                    3. memory hydration IN PARALLEL (goroutines, same ctx)
              ┌──────────────┬──────────────┬───────────────┐
              ▼              ▼              ▼               ▼
         BadgerDB        rqlite         Qdrant           rqlite
      (chat+summary)  (facts FTS5)  (semantic RAG,    (primitives/tools
          µs             ms          if applicable)     cloned per role)
              │              │              │               │
              └──────────────┴──────┬───────┴───────────────┘
                                    ▼
                    4. Gollem assembles surgical context + filters tools by scopes
                                    │
                                    ▼
                    5. Gollem → LLM: forced structured output (Agent[T])
                                    │
                          ┌─────────┴─────────┐
                          │ invalid schema?    │
                          │ yes → reflection loop│──► retries (max N) with exact typing error
                          │ no → continues     │
                          └─────────┬─────────┘
                                    ▼
                    6. SSE stream to the client (minimal TTFT)
                          │
                          │ ctx.Done() (client closed the connection)?
                          │ yes → cancels the LLM call, stops spending tokens
                          ▼
                    complete (or cancelled) response to the client
                                    │
════════════════════════════════════╪══════════════════════════════════════════
                                    ▼  (async, does not block the already-sent response)
                    7. SLEEP CYCLE: BadgerDB Outbox → workers → Qdrant/rqlite
                       (full detail in consistency.md)
```

## Step 1 — Huma validates the contract and extracts identity

- **What it does:** [Huma](../components/huma-api.md) receives the raw HTTP request, validates it
  against the endpoint's Go struct (JSON Schema derived via reflection), and decodes the
  authentication token through an explicit security handler.
- **Component:** Huma (contracts / "syscalls" layer of the [LLM OS model](overview.md#llm-os)).
- **What travels in the ctx (from here on):** `tenant_id`, `user_id`, `scopes` — injected by Huma
  and **only** by Huma. No business handler parses auth headers on its own (
  [zero magic](../vision/principles.md#derived-operating-principles) principle).
- **What can fail:** input that doesn't match the schema → `application/problem+json` (RFC 9457)
  without ever touching memory or the LLM. Invalid/expired token → rejected in the security
  handler, before populating the ctx. *(Exact shape of the security handler and SDK function
  names: to be verified against the real Huma API before coding.)*

## Step 2 — Quota control and circuit breaker

- **What it does:** before spending a single LLM token, the tenant's **token-bucket** state is
  checked. If the quota is exhausted, the circuit breaker decides whether to degrade to a cheaper
  model or return `429` directly.
- **Component:** huma-aware middleware that reads `quota:{tenant}` in [BadgerDB](../components/badgerdb-working-memory.md)
  (short TTL, refreshed per request).
- **What travels in the ctx:** nothing new is added; `tenant_id` is **read** from the ctx already
  populated in step 1 to build the `quota:{tenant}` key.
- **What can fail:** BadgerDB unavailable (unlikely, it's in-process) → fail-open or fail-closed
  is a product decision still pending explicit definition (to be verified). Exhausted quota →
  degradation path (cheaper model) or `429`, never a silent wait.

## Step 3 — Parallel memory hydration

- **What it does:** **concurrent goroutines** are launched, all receiving the same `ctx`, each
  querying one tier of [tiered memory](memory-tiers.md):
  - **BadgerDB** → summary buffer + last N messages of the session (µs).
  - **rqlite** → relevant facts/tasks via FTS5 `MATCH`, Engram-style (ms).
  - **Qdrant** → semantic RAG filtered by `tenant_id`/`user_id` during the HNSW scan, **only if
    the prompt requires it** (not every request triggers RAG).
  - **rqlite** (second query) → primitives/tools cloned per `user_id`, based on the user's role
    (see [primitive self-improvement](memory-tiers.md#primitive-self-improvement-behavioral-memory)).
- **Component:** orchestrated by the Huma handler / layer preceding Gollem; each goroutine calls
  its corresponding store ([BadgerDB](../components/badgerdb-working-memory.md),
  [rqlite](../components/rqlite-facts-memory.md), [Qdrant](../components/qdrant-semantic-memory.md)).
- **What travels in the ctx:** `tenant_id`/`user_id` are used to build **each filter**
  independently (never a global state shared across goroutines) — in Qdrant as a
  `Filter{tenant_id, user_id}` applied *during* the HNSW scan; in rqlite as a mandatory `WHERE
  tenant_id = ? AND user_id = ?` in every query. The `ctx` also carries the request's *deadline*,
  which every goroutine must respect.
- **What can fail:** any of the four queries can be slow or fail independently — the design must
  decide whether a partial failure (e.g. Qdrant down) degrades to responding with only
  BadgerDB+rqlite or aborts the whole request (to be verified/still pending explicit definition).
  A badly built filter in Qdrant (missing `tenant_id`) is still a silent isolation leak — see
  [Qdrant gotchas](../components/qdrant-semantic-memory.md#gotchas-and-risks); for rqlite, the
  equivalent gap is now prevented **by construction**, not by review, via
  [`internal/tenancy`](../decisions/0011-tenant-scoped-statement-builder.md) — every facts-tier
  statement carries `WHERE tenant_id=?` (and `user_id=?` where applicable) because there is no
  code path that can render one without it (see
  [ADR-0011](../decisions/0011-tenant-scoped-statement-builder.md)). `weak` consistency in rqlite
  can still return slightly stale facts if the sleep cycle wrote recently
  (see [consistency](consistency.md)).

## Step 4 — Gollem assembles context and injects tools by scope

- **What it does:** [Gollem](../components/gollem-orchestrator.md) receives the four hydration
  results and concatenates them into a **surgical context** (never a bloated prompt with hidden
  templates — [context engineering](../vision/principles.md#2-context-engineering--prompt-engineering)).
  In parallel, it filters the tenant's tool catalog and leaves available **only** those the
  `scopes` in the `ctx` authorize for that `user_id`.
- **Component:** Gollem (cognitive kernel of the [LLM OS](overview.md#llm-os)).
- **What travels in the ctx:** `scopes` (populated in step 1) are **read** to decide which
  `FuncTool[P]` get injected; Gollem writes nothing new to the ctx, it's purely a consumer of
  identity and deadline. Details of the scope-based authorization logic in
  [granular control](granular-control.md).
- **What can fail:** a badly mapped scope can over-expose (a tool that shouldn't be available) or
  under-expose (a needed tool missing, the LLM can't resolve the task) — requires an audit of the
  scope→tool mapping. An assembled context that's too large (if hydration didn't trim well)
  bloats input tokens — a responsibility shared with the scoring of
  [tiered memory](memory-tiers.md#2-facts--episodic-memory-engram-style).

## Step 5 — LLM with structured output and reflection loop

- **What it does:** Gollem invokes the LLM through `Agent[T]`, forcing the output to comply with
  the JSON Schema derived from the **same Go struct** Huma used to validate the input (
  [the struct as the single source of truth](../vision/thesis.md#the-struct-as-the-single-source-of-truth)).
  If the LLM returns something that doesn't validate against the schema, Gollem retries by
  passing the **exact typing error**, up to a configurable maximum of N retries.
- **Component:** Gollem, against the configured LLM provider (normal or degraded model, per
  step 2).
- **What travels in the ctx:** the ctx's *deadline* bounds how long the full reflection loop can
  run — there is no independent parallel timer outside the ctx (see
  [isolation via ctx](../vision/principles.md#derived-operating-principles)).
- **What can fail:** the LLM exhausts the N retries without producing a valid output → a
  controlled error is returned to the client, never an indefinite hang. The actual schema-failure
  rate per model is an open question of the project — see
  [Gollem gotchas](../components/gollem-orchestrator.md#gotchas-and-risks). If the ctx is
  cancelled mid-reflection-loop (see step 6), the loop is cut just like a normal call.

## Step 6 — SSE stream with cancellation via ctx

- **What it does:** the response is emitted incrementally to the client via Server-Sent Events,
  aiming for the lowest possible **TTFT** (time-to-first-token). If the client closes the
  connection, the HTTP handler's `ctx` is cancelled; that cancellation propagates to Gollem, which
  cuts the in-flight LLM call and **stops consuming tokens**.
- **Component:** Huma (SSE transport, connection closing) + Gollem (honors `ctx.Done()` in the
  generation loop).
- **What travels in the ctx:** the cancellation signal (`ctx.Done()`) is the **only** cutoff
  mechanism — there are no kill-switches or parallel timers outside the ctx.
- **What can fail:** if some intermediate point in the chain (for example, a residual goroutine
  from step 3's memory hydration) doesn't respect `ctx.Done()`, the token savings from
  cancellation don't actually materialize — see
  [Huma gotchas](../components/huma-api.md#gotchas-and-risks). Auditing **every** consumer of the
  ctx is a cross-cutting responsibility, not a single component's.

## Step 7 — Asynchronous sleep cycle (post-response)

- **What it does:** within the same ACID transaction where the message was saved (step 3/5),
  BadgerDB already wrote an event to the **local Outbox** (`outbox:pending:*`). After the SSE
  finished — without the user waiting on any of this — background workers drain that Outbox,
  synchronize to Qdrant and rqlite idempotently, and trigger [DataFusion](../components/datafusion-sleep-cycle.md)
  for cold batch consolidation (dedup, contradictions, noise, friction patterns).
- **Component:** Go workers + DataFusion (separate Rust worker, see
  [ADR-0005](../decisions/0005-datafusion-go-rust-boundary.md)).
- **What travels in the ctx:** **nothing** — this step deliberately runs **outside** the original
  request's ctx (which has already finished). The worker operates with its own background
  context, though it retains `tenant_id`/`user_id` as business data (not as an HTTP cancellation
  ctx).
- **What can fail:** see the full detail of failures, backoff, idempotency, and reconciliation in
  [consistency](consistency.md) — this step is, in itself, the reason that document exists.

## What travels in the ctx, step by step

| Step | Added to ctx | Read from ctx | Cancelled/expires |
|---|---|---|---|
| 1. Huma validates | `tenant_id`, `user_id`, `scopes` | — | — |
| 2. Quota | — | `tenant_id` | — |
| 3. Hydration | — | `tenant_id`, `user_id`, deadline | goroutines must respect the deadline |
| 4. Gollem assembles | — | `scopes` (tool filtering) | — |
| 5. LLM + reflection | — | deadline (bounds retries) | reflection loop cuts off if it expires |
| 6. SSE | — | `ctx.Done()` (client cancellation) | cuts the in-flight LLM call |
| 7. Sleep cycle | *(own background ctx, not the request's)* | `tenant_id`/`user_id` as data | — |

## References / cross-links

- [Architecture overview](overview.md) — the 7-step summary this document expands.
- [Consistency](consistency.md) — full detail of step 7 (Outbox, idempotency, reconciliation).
- [Tiered memory](memory-tiers.md) — what each store hydrates in step 3.
- [Granular control](granular-control.md) — how tool filtering by scope is resolved in step 4,
  and the security × cost × behavior balance.
- [Huma — Contracts and API layer](../components/huma-api.md) — steps 1, 2 (quotas), and 6 (SSE).
- [Gollem — Cognitive orchestrator](../components/gollem-orchestrator.md) — steps 4, 5, and 6.
- [BadgerDB — Working Memory](../components/badgerdb-working-memory.md) — step 3 (read) and
  step 7 (Outbox, write).
- [rqlite — Facts Memory](../components/rqlite-facts-memory.md) · [Qdrant — Semantic Memory](../components/qdrant-semantic-memory.md) — step 3 (read) and step 7 (consolidation target).
- [Design principles](../vision/principles.md) — isolation via ctx, context engineering.
- [ADR-0011](../decisions/0011-tenant-scoped-statement-builder.md) — the tenant-scoped statement
  builder (`internal/tenancy`) that prevents step 3's rqlite queries from ever being unscoped.
