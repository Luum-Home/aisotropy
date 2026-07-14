---
type: Component
title: "Consolidation outbox — the Transactional Outbox worker"
description: How a chat turn becomes long-term memory — the single-transaction AppendWithOutbox guarantee, the at-least-once drain worker, idempotent facts/semantic sinks, backoff, and the Bet-6 held-out sampling exception on the streaming path.
tags: [components, memory, outbox, consolidation, consistency, badgerdb, idempotency]
timestamp: 2026-07-14T00:00:00Z
---

# Consolidation outbox — the Transactional Outbox worker

The consistency keystone of the memory hierarchy: the mechanism that moves a user's chat turn
from [working memory](badgerdb-working-memory.md) into the long-term
[facts](rqlite-facts-memory.md) and [semantic](qdrant-semantic-memory.md) tiers without ever
losing a turn or duplicating one. Design contract:
[consistency](../architecture/consistency.md). Status (2026-07-14): **REAL** — wired
unconditionally in the runtime ([`aisotropy/app.go`](../../aisotropy/app.go)): cold-boot
`RecoverOutbox` + a steady-state `RunOutboxWorker` goroutine polling every 1s.

## Phase 1 — single-transaction enqueue (`AppendWithOutbox`)

[`internal/memory/outbox.go`](../../internal/memory/outbox.go): BadgerDB is not only working
memory, it is also the durable event queue. `AppendWithOutbox` writes the chat message AND its
"intent to consolidate" (`OutboxEvent`, key `outbox:pending:{tenant}:{ts}:{id}`) in **one ACID
BadgerDB transaction** — either both are durable or neither is, even on a crash mid-commit.

Scope, verified against the handlers:

- **USER turns only.** Both chat routes call `AppendWithOutbox` for the user's prompt
  ([`internal/api/chat_stream.go`](../../internal/api/chat_stream.go),
  [`internal/api/chat.go`](../../internal/api/chat.go)). The **assistant reply is persisted
  via plain `Append` with no outbox event** — only user turns feed the facts/semantic tiers.
- The event is **self-contained**: it carries the turn text (not just a reference), so a
  worker can process it even after the source message has been pruned from the active buffer.
- The event ID is derived from the message reference, so re-enqueueing the same turn writes
  the same key — the event layer itself does not duplicate, before the sinks' own idempotency.

### The Bet-6 held-out sampling exception (streaming path only)

On `chat/stream`, when the bitrate scorer is enabled (`AISOTROPY_BITRATE_BIN`) and
`bitrate.ShouldSample(ref)` selects the turn (a deterministic ~5%), the handler **replaces the
consolidation enqueue with a `bptSampler.Reserve` call**: the user message still enters
working memory via plain `Append` (it really happened), but it is deliberately **not**
consolidated into facts/semantic. That is what keeps the scored held-out text out of the
tenant's long-term memory, so the later bits-per-token lift is not spurious (leakage) — see
[bitrate bpt scorer](bitrate-bpt-scorer.md). The non-streaming `chat` route has no sampler
branch: it always enqueues.

## Phase 2 — the drain worker

`RunOutboxWorker` (a goroutine started in `app.go`, cancelled on graceful shutdown before the
stores it writes to are closed) drains pending events until ctx is cancelled:

- **At-least-once, never silent discard**: an event is deleted only after the sink confirms
  success; a failing event is left in place for a later pass while other events still proceed.
- **Backoff**: a store-level error backs off exponentially from 200ms, capped at 30s; an empty
  queue polls at the configured interval (1s in `app.go`); progress resets the backoff and
  loops immediately.
- **Cold-boot reconciliation** (`RecoverOutbox`): on startup, any event still pending is by
  definition an orphan from a previous shutdown, and it is drained through the exact same code
  path — no special recovery mode.

## The sink — idempotent by construction

[`internal/consolidation/sink.go`](../../internal/consolidation/sink.go) implements
`memory.ConsolidationSink` one package above all three tiers, so `internal/memory` never
imports facts/semantic and the dependency graph stays acyclic. `Sink.Consolidate` fans out to
the two sinks **in parallel** and reports success only when both succeed — any error leaves
the event pending for an idempotent retry:

- **Facts**: `facts.HeuristicExtractor` distils at most one fact from the user message
  ([`internal/facts/extract.go`](../../internal/facts/extract.go)); every distilled fact is
  routed through the [engram facade](engram-facts-journal.md) (never `facts.Store.Save`
  directly), so it is dual-indexed as a Fact-kind vector from birth (ADR-0017 phase 2, gated
  by `MinImportance` / `AISOTROPY_FACT_MIN_IMPORTANCE`). Idempotency: the fact `ID` is
  `"fact-" + hex(sha256(tenant, user, lowercased content))[:16]` and the write is an
  `INSERT ... ON CONFLICT(id) DO UPDATE`
  ([`internal/facts/schema.go`](../../internal/facts/schema.go)), so a redelivery overwrites
  in place.
- **Semantic**: the event content is embedded ([embedder](embedder.md)) and upserted as a
  `KindTurn` point whose ID is a deterministic UUIDv5 of (scope, kind, event ID)
  (`semantic.PointID`), so a retry is a true no-op upsert. Tenant/user identity is routed
  through the blessed `ScopeFromContext` constructors (ADR-0011) on both sides.
- Any **nil tier is skipped** — a deployment can run facts-only or semantic-only.

## Code map

| Path | What it is |
|---|---|
| [`internal/memory/outbox.go`](../../internal/memory/outbox.go) | `OutboxEvent`, `AppendWithOutbox`, `RunOutboxWorker`, `RecoverOutbox`, backoff constants |
| [`internal/consolidation/sink.go`](../../internal/consolidation/sink.go) | `Sink` — parallel facts + semantic consolidation, idempotent end to end |
| [`internal/facts/extract.go`](../../internal/facts/extract.go) | `HeuristicExtractor` + the sha256-derived stable fact ID |
| [`internal/api/chat_stream.go`](../../internal/api/chat_stream.go) | Enqueue on the streaming route + the Bet-6 held-out branch |
| [`internal/api/chat.go`](../../internal/api/chat.go) | Enqueue on the non-streaming route |
| [`aisotropy/app.go`](../../aisotropy/app.go) | Wiring: sink construction, `RecoverOutbox`, worker goroutine, shutdown ordering |

## Gotchas

- **At-least-once means the sinks MUST stay idempotent.** Any new sink write added to
  `Consolidate` needs a stable key + upsert semantics, or redeliveries will duplicate.
- **Consolidation is asynchronous.** A fact stated in one turn is not guaranteed to be
  hydrated into the very next request's system prompt — the worker loop is the propagation
  delay (normally within ~1s, plus backoff under failure).
- **`chat/agent` neither hydrates nor consolidates memory** — the outbox path covers the two
  chat routes only (see [runtime as built](../architecture/runtime-as-built.md)).

## Related decisions

- [Consistency](../architecture/consistency.md) — the Transactional Outbox design contract.
- [ADR-0017](../decisions/0017-semantic-tier-multi-kind-memory.md) — multi-kind semantic tier;
  why facts route through the engram facade.
- [ADR-0011](../decisions/0011-tenant-scoped-statement-builder.md) — blessed scope
  constructors on every sink write.
- [ADR-0014](../decisions/0014-bits-per-token-reference-scorer-boundary.md) — the held-out
  sampling design the streaming-path exception implements.
