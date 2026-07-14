---
type: Concept
title: Consistency — Transactional Outbox and Sleep Cycle
description: The pattern that resolves eventual consistency without blocking the hot path — a local Outbox in BadgerDB, asynchronous workers, mandatory idempotency, and cold-boot reconciliation.
tags: [architecture, consistency, outbox, saga, idempotency, sleep-cycle, badgerdb, rqlite, qdrant]
timestamp: 2026-07-08T00:00:00Z
---

# Consistency — Transactional Outbox and Sleep Cycle

This document is the piece that holds everything else together: without an explicit and honest
consistency guarantee, [tiered memory](memory-tiers.md) would just be three out-of-sync databases
with an LLM reading whatever it can. Every memory component ([BadgerDB](../components/badgerdb-working-memory.md),
[rqlite](../components/rqlite-facts-memory.md), [Qdrant](../components/qdrant-semantic-memory.md),
[DataFusion](../components/datafusion-sleep-cycle.md)) and step 7 of the
[request lifecycle](request-lifecycle.md) depend on the contract described here. It's the
architectural **keystone** of Aisotropy.

> **Freshness note (2026-07-14):** audited against the code (`internal/memory/outbox.go`,
> `internal/consolidation/sink.go`, `internal/api/chat*.go`, `aisotropy/app.go`). The pattern is
> **implemented and wired**. Corrections applied where the prose drifted from the code — flagged
> inline with *(status note 2026-07-14)*. The main drifts fixed: the outbox event carries a **copy**
> of the turn text (not a bare reference); only the **user turn** is enqueued (assistant replies and
> held-out bitrate samples take a plain `Append`); the outbox drainer is **Go-only** (DataFusion does
> not scan the outbox); and per-event sink failures retry on the **poll interval**, with exponential
> backoff reserved for store-level errors.

## The golden rule

> **There is no synchronous consistency while the user waits.** Writing to rqlite and Qdrant
> within the request would destroy latency (network ms and embedding computation on the hot path)
> — and that's exactly what the [LLM OS model](overview.md#llm-os) forbids. Aisotropy instead
> splits the operation into **two decoupled phases**, the **Saga / Transactional Outbox** pattern:

| Phase | When it runs | What it guarantees | Where it lives |
|---|---|---|---|
| **1. Isolated synchronous operation** | Within the request, while the user waits on SSE | *Local* atomicity: the message and the intent to consolidate are recorded together or neither is | [BadgerDB](../components/badgerdb-working-memory.md), a single `Txn` |
| **2. Asynchronous consolidation** | After responding, in the background (sleep cycle) | *Eventual* consistency between BadgerDB → rqlite/Qdrant, with retries until success | Go workers (`RunOutboxWorker`, `internal/memory/outbox.go`). *(status note 2026-07-14: the outbox drainer is Go-only; [DataFusion](../components/datafusion-sleep-cycle.md) is a separate, dormant-by-default cold batch that dedups already-consolidated facts — it does not scan the outbox.)* |

This is the literal application of the [honest eventual consistency](../vision/principles.md#derived-operating-principles)
principle: staleness between phases is not a hidden bug, it's an explicit and bounded contract.

## Phase 1 — The local Outbox in BadgerDB

**BadgerDB isn't just working memory — it's the short-term source of truth *and* the event
queue**, in the same embedded engine. This is what makes Phase 1 possible without touching the
network:

- Within the **same ACID transaction** (`Txn.Commit()`, serializable snapshot isolation via MVCC)
  two things are written (`WorkingMemory.AppendWithOutbox`):
  1. The chat message, under `tenant:{tenant}:user:{user}:session:{session}:chat:{seq}` (the active
     buffer's per-message key; see `internal/memory/key.go`).
  2. A **pending consolidation event**, under the key `outbox:pending:{tenant}:{ts}:{id}`.
- **Hard guarantee:** if the Go process dies at that exact instant (panic, OOM, kill -9, power
  outage), BadgerDB guarantees that **either both were saved, or neither was**. There is no
  intermediate state where the message exists but the consolidation event was lost (or vice
  versa). This is the property that lets Phase 2 blindly trust that the enqueued turn has a pending
  event somewhere. *(status note 2026-07-14: only the **user turn** is enqueued this way. The
  assistant reply is written with a plain `Append` — no outbox event — and is therefore not
  consolidated into the long-term tiers; held-out bitrate samples (Bet 6) also take a plain
  `Append` and are deliberately reserved rather than enqueued. See `internal/api/chat.go` /
  `chat_stream.go`.)*
- *(status note 2026-07-14: the event is **self-contained**, not a bare reference — `OutboxEvent`
  carries a copy of the turn text (`OutboxEvent.Content`) so a worker can consolidate it even after
  the source message has been pruned from the active buffer. It intentionally does not carry the
  full message record, but it is not a pointer-only payload as an earlier draft of this doc
  claimed.)*

There is no write to rqlite or Qdrant at this point. The request ends (SSE closed) without ever
touching the consolidation network — the latency perceived by the user is the LLM's, not
long-term memory's.

## Phase 2 — The asynchronous Sleep Cycle

A background Go worker (`RunOutboxWorker`, one goroutine started in `aisotropy/app.go`) scans
`outbox:pending:*` continuously (default 1s poll). *(status note 2026-07-14: the
[sleep cycle](../components/datafusion-sleep-cycle.md)'s DataFusion batch is a separate, downstream
process that dedups already-consolidated facts — it is not an alternate outbox drainer, and it is
dormant unless `AISOTROPY_SLEEPCYCLE_BIN` is set.)*

```
1. Worker reads an event from outbox:pending:{tenant}:{ts}:{id}
2. The event is self-contained (carries the turn text) → builds the consolidation payload
3. Synchronizes to both sinks IN PARALLEL:
     a) Qdrant  → upsert the embedding (if semantic RAG applies)
     b) rqlite  → upsert the distilled fact/task (Engram-style)
4. Only if BOTH sinks confirm success → deletes outbox:pending:{id} from BadgerDB
```

While the event isn't deleted, it **remains pending work** — the worker (or a peer, if the first
one died) will pick it up again. This is what makes the pattern **at-least-once**, never
at-most-once: reprocessing is preferred over losing a fact.

**Eventual consistency, with an explicit window:** between the moment the user receives their
response and the moment rqlite/Qdrant reflect that message, there's a staleness window. This is
exactly what [rqlite](../components/rqlite-facts-memory.md#gotchas-and-risks) and
[Qdrant](../components/qdrant-semantic-memory.md#gotchas-and-risks) document as "eventual
consistency with the sleep cycle" — this is where the mechanism that produces and closes it is
formalized.

## Failure handling: at-least-once + exponential backoff

If Qdrant or rqlite fail (timeout, service down, network partition, Raft leader election):

- The event **is not deleted** from `outbox:pending:*`. Isolated point of failure — the work
  doesn't disappear, it's only delayed.
- The worker retries until the sink recovers. There's no retry cap that silently discards the
  event: the philosophy is *"the fact eventually gets in, even if late,"* not *"if it fails three
  times, it's lost."* *(status note 2026-07-14: a per-event sink failure leaves the event in place
  and it is re-attempted on the next **poll interval** (default 1s). The capped **exponential
  backoff** (`outboxInitialBackoff` → `outboxMaxBackoff`, 200ms → 30s) is reserved for store-level
  errors — a BadgerDB read/delete failure — not for downstream sink failures. See `RunOutboxWorker`
  in `internal/memory/outbox.go`.)*
- A partial failure (Qdrant OK, rqlite down, or vice versa) does **not** delete the key: the
  criterion is success on **both** sinks. The next attempt may write again to the sink that had
  already succeeded — hence idempotency is mandatory, not an optimization.

## Mandatory idempotency

Since the pipeline is at-least-once, **any event can be processed more than once** (retry after a
partial failure, or the cold-boot reconciliation worker picking up an orphaned event again). Each
sink resolves idempotency with the native primitive that fits it:

| Sink | Idempotency mechanism | Effect on reprocessing |
|---|---|---|
| **Qdrant** | Point ID = **deterministic UUID v5**, derived from `tenant_id + user_id + chat_message_id` | The upsert is a **true no-op**: same ID → same point → overwrites, never duplicates the vector |
| **rqlite / SQLite** | `INSERT OR REPLACE` (or equivalent `UPSERT`) over a **stable PK derived from the fact's context** (not a blind autoincrement) | Rewrites the existing row instead of inserting a new one — zero duplicate facts |

Without this, a retry after a mid-flight network failure would silently duplicate vectors in
Qdrant or generate repeated facts in rqlite — with no visible error. Idempotency isn't optional:
it's the condition that makes at-least-once safe.

## Cold-boot reconciliation

If the Go process shuts down (deploy, crash, OOM) with events still in `outbox:pending:*` that no
worker finished draining, those events **are not lost** — they remain persisted in BadgerDB
(which survives the process restart, unlike pure in-memory state). But they don't consolidate
themselves either — someone needs to pick them up again.

```
aisotropy.App build [Go]  (cmd/aisotropyd/main.go is a thin consumer of this)
  │
  ├─ 1. Runs the startup reconciliation process (wm.RecoverOutbox):
  │        - Scans outbox:pending:* in full (all tenants)
  │        - Any event found is, by definition, an "orphan from a previous shutdown"
  │        - Drains those events synchronously via the same sink as steady state
  │        - Same idempotency path as normal draining (Phase 2) — there is no
  │          special "recovery" code: it's drainOutboxOnce, just with more accumulated work
  │      then starts the steady-state goroutine (go wm.RunOutboxWorker)
  │
  └─ 2. Builds Huma (humachi.New, api.Register*) and starts the HTTP server
```

*(status note 2026-07-14: reconciliation runs at `aisotropy/app.go:326` — **before** the Huma API
is registered (`app.go:395`+) and before the server listens, not after. It happens inside the
`aisotropy.App` builder, not literally in `main()`, which is a thin ~51-line consumer.)*

This closes the Outbox's full lifecycle: nothing ever written to `outbox:pending:*` is left
floating indefinitely, even if the shutdown was abrupt.

## Cross-reference with ADR-0004 (rqlite): why the durable ACK matters

The design of this Outbox **depends** on at least one of its sinks giving back an acknowledgment
that can be trusted without reservation. [ADR-0004](../decisions/0004-facts-memory-rqlite-vs-litefs.md) —
already **accepted** — chooses **rqlite** over LiteFS precisely for this reason: rqlite's Raft
leader confirms a write **after** replicating it to quorum. When the Outbox worker receives that
ACK, the fact is *really* persisted — it can't disappear in a subsequent failover. That's the
underlying reason, not just a footnote: LiteFS's **asynchronous** replication model (with a
documented rollback window) could "undo" a fact the Outbox had already considered delivered,
breaking the at-least-once/idempotent semantics described above. Without a durable ACK, Phase 2 of
this document wouldn't be correct — that's why ADR-0004 was resolved in favor of rqlite.

## Cross-reference with Bet 2 (deterministic codec)

Aside from rqlite/Qdrant, [Bet 2 — working memory as a codec](../research/bet-02-working-memory-codec.md)
proposes compressing BadgerDB's history against a local model instead of summarizing it lossily.
Worth mentioning here because its hard constraint — **the same deterministic model must
reconstruct the exact history** — pushes in the same direction as this document: reproducible
builds and explicit versioning, this time on the model's side instead of the data's. It's a
research bet (🟡), not part of the accepted consistency contract, but it shares the same
discipline of "nothing implicit, everything versioned."

## Full flow diagram

```
HOT PATH (synchronous, the user waits)
──────────────────────────────────────────────
  Gollem responds to the user
        │
        ▼
  ┌───────────────────────────────┐
  │          BadgerDB              │   1 single ACID Txn (SSI/MVCC), user turn only:
  │  ...:session:{s}:chat:{seq} ◄──┼── - chat message (user turn)
  │  outbox:pending:{t}:{ts}:{id} ◄┼── - pending consolidation event
  └───────────────┬────────────────┘
                  │  (if the process dies here: both or neither — never halfway)
                  │
══════════════════╪══════════════════════════════════════════════════════════
                  │  SLEEP CYCLE (asynchronous, background — doesn't block the user)
                  ▼
        ┌───────────────────┐
        │  Worker (drains    │◄── also triggered on COLD BOOT by orphaned events
        │  outbox:pending:*) │
        └─────────┬──────────┘
                  │ synchronizes IN PARALLEL
        ┌─────────┴─────────┐
        ▼                   ▼
  ┌───────────┐       ┌───────────┐
  │  Qdrant   │       │  rqlite   │
  │ upsert    │       │ upsert    │
  │ UUID v5   │       │ INSERT OR │
  │ determ.   │       │ REPLACE   │
  └─────┬─────┘       └─────┬─────┘
        │                   │  durable ACK (Raft quorum, see ADR-0004)
        └─────────┬─────────┘
                  ▼
          success on BOTH?
           │              │
          no              yes
           │              │
           ▼              ▼
   retried on next poll  deletes outbox:pending:{id}
   (event stays alive;    from BadgerDB
   ~1s interval)          (end of cycle for that event)
```

## Guarantees summary table

| Property | Guarantee | Mechanism |
|---|---|---|
| Local atomicity (Phase 1) | Message + pending event: both or neither | BadgerDB's ACID `Txn.Commit()` |
| Delivery | At-least-once (never at-most-once) | The `outbox:pending:*` key isn't deleted until success is confirmed on both sinks |
| Idempotency | Reprocessing an event doesn't duplicate data | Deterministic UUID v5 (Qdrant) · `INSERT OR REPLACE` over a stable PK (rqlite) |
| Sink durability | The sink's ACK is trustworthy, cannot be reverted | rqlite's Raft quorum (see [ADR-0004](../decisions/0004-facts-memory-rqlite-vs-litefs.md)) |
| Crash recovery | No event remains orphaned indefinitely | Cold-boot reconciliation (`wm.RecoverOutbox`) during `aisotropy.App` build, before the Huma API is registered and the server listens |
| Consistency perceived by the user | No additional wait on the hot path | All of Phase 2 runs after the SSE, in the background |

## Gotchas and what this document doesn't resolve

- **Outbox backpressure:** if workers fall behind or the sinks are down for an extended period,
  `outbox:pending:*` can grow without bound within BadgerDB. The alerting/backpressure policy is
  still pending design (see also [BadgerDB — gotchas](../components/badgerdb-working-memory.md#gotchas-and-risks)).
- **rqlite read-consistency level per use case** (`none`/`weak`/`linearizable`) is not defined
  here — it's an open question in [ADR-0004](../decisions/0004-facts-memory-rqlite-vs-litefs.md#open-questions).
  It affects whether a recent sleep-cycle write is immediately visible in the next request's
  memory hydration (read-your-writes).
- **Exact retry semantics mid-DataFusion-batch** (if the Raft leader changes while the worker is
  writing) remains an open question shared with ADR-0004.
- **Physical multi-tenancy of the Outbox** (a single global `outbox:pending:*` filtered by
  prefix, or partitioned?) inherits the pending physical multi-tenancy decision (ADR-0007, see
  [decisions](../decisions/index.md)).

## References / cross-links

- [Architecture overview](overview.md) — step 7 of the lifecycle summarized here in detail.
- [Request lifecycle](request-lifecycle.md) — where this document fits in the full sequence.
- [Tiered memory](memory-tiers.md) — the memory triangle this pattern keeps synchronized.
- [BadgerDB — Working Memory](../components/badgerdb-working-memory.md) — implementation of the local Outbox.
- [rqlite — Facts Memory](../components/rqlite-facts-memory.md) — facts sink, idempotent via `INSERT OR REPLACE`.
- [Qdrant — Semantic Memory](../components/qdrant-semantic-memory.md) — vector sink, idempotent via UUID v5.
- [Apache DataFusion — Sleep Cycle](../components/datafusion-sleep-cycle.md) — batch consolidation that runs on already-synchronized data.
- [ADR-0004 — rqlite vs LiteFS](../decisions/0004-facts-memory-rqlite-vs-litefs.md) — why rqlite's durable ACK makes this design correct.
- [Bet 2 — Working memory as a codec](../research/bet-02-working-memory-codec.md) — cross-reference on determinism, a related research bet.
- [Honest eventual consistency](../vision/principles.md#derived-operating-principles) — the principle this document formalizes.
