---
type: Component
title: BadgerDB — Working Memory
description: The L1/RAM tier of the LLM OS — live chat, summary buffer, and local Transactional Outbox, embedded in-process in Go.
tags: [components, memory, badgerdb, working-memory, outbox, go]
timestamp: 2026-07-08T00:00:00Z
---

# BadgerDB — Working Memory

**Store:** [BadgerDB](https://github.com/dgraph-io/badger) (Go, Apache-2.0, LSM-tree, embedded in-process).

## Role in the system

BadgerDB is the **L1/RAM** tier of the [LLM OS](../architecture/overview.md#llm-os) model: the
working memory of the active session. It lives *inside* the same Go process as Huma and Gollem — no
network round-trip, no external Redis. It is the hottest tier of the
[memory hierarchy](../architecture/memory-tiers.md), consulted on **every** request.

It has a **dual role** that sets it apart from a simple chat cache:

1. **Active context window** — the last ~10-15 messages plus the condensed summary buffer.
2. **Local Transactional Outbox** — the mechanism that guarantees no consolidation event is
   lost between working memory and the colder tiers (rqlite, Qdrant). See
   [consistency](../architecture/consistency.md).

Without this dual role, any write to rqlite/Qdrant on the hot path would introduce latency
or a point of failure without ACID guarantees. BadgerDB solves both problems in a single local
transaction.

## Responsibilities

- Serve chat history to build Gollem's prompt in microseconds.
- Maintain the **summary buffer**: a condensed paragraph of the session state that replaces
  older messages, so the context window never grows indefinitely (see
  [context engineering](../vision/principles.md#2-context-engineering--prompt-engineering)).
- Persist, in the same ACID transaction as the message, a **pending consolidation event**
  (`outbox:pending:*`) that the sleep cycle will process later.
- Serve as a **token-bucket** for per-tenant rate limiting (quota control before invoking the
  LLM, step 2 of the [request lifecycle](../architecture/overview.md#lifecycle-of-a-request)).

## Data model / keys

Everything under a hierarchical composite key, so that tenant isolation is part of the key (not
just the query filter):

| Key prefix | Content | TTL / lifecycle |
|---|---|---|
| `chat:{tenant}:{user}:{session}` | Active messages (last N), ordered | Pruned once N is exceeded; deleted entries move to the summary |
| `summary:{tenant}:{user}:{session}` | Condensed session paragraph (summary buffer) | Rewritten every time the condensation goroutine runs |
| `outbox:pending:{tenant}:{ts}:{id}` | Pending consolidation event (minimal payload + reference) | Deleted by the sleep cycle worker after successful processing |
| `quota:{tenant}` | Token-bucket state (remaining tokens, last refill) | Short TTL, refreshed per request |

The `tenant:user:session` key as a prefix takes advantage of BadgerDB ordering keys
lexicographically (LSM-tree): it allows iterating over a full session history with an
`Iterator` bounded by prefix, without scanning the rest of the keyspace — key to keeping reads in
microseconds even with many tenants coexisting in the same process.

## Integration surface

- **Gollem** reads `chat:*` + `summary:*` during the memory hydration step (in parallel with
  rqlite and Qdrant, see [overview](../architecture/overview.md#lifecycle-of-a-request)) to
  assemble the surgical context that enters the LLM's window.
- **Huma** consults `quota:{tenant}` before accepting the request (circuit breaker /
  degradation to a cheap model if the quota was exhausted).
- **Sleep cycle workers** consume `outbox:pending:*` asynchronously, with backoff, and
  idempotently — if a worker fails midway, the event stays in the outbox and gets retried.
- It exposes no HTTP of its own: it's an embedded library, its only surface is the Go API
  (`Txn`, `Iterator`, `Stream`) invoked in-process.

## Key configuration / patterns

- **Local ACID transactions:** BadgerDB supports concurrent transactions with *serializable
  snapshot isolation* (SSI) via MVCC. This is what makes the Outbox pattern possible: writing the
  chat message and the `outbox:pending:*` event in the **same** `Txn.Commit()` — either both
  commit, or neither does.
- **Async Summary Buffer:** a separate goroutine monitors the size of `chat:*` per session; once
  it exceeds N messages, it condenses the oldest ones (via Gollem/a cheap LLM or a heuristic) and rewrites
  `summary:*`, purging the active buffer. It doesn't block the hot request path.
- **LSM-tree tuning:** the value log separates keys from large values, so the LSM tree can live
  almost entirely in RAM while large values (long messages) go to SSD — the real reason
  history reads are so fast.
- **No external service:** compiles into the same Go binary as the rest of the in-process stack
  (Huma+Gollem+BadgerDB), distroless, MB-scale RAM footprint — part of the
  ["golden rule" of the stack](../architecture/overview.md#the-stack-closed-100-mitapache-20).

## Gotchas and risks

- **LSM compaction under heavy write load:** the Outbox adds a constant write pattern
  (every message generates an event). If concurrent tenant volume grows a lot, BadgerDB's
  compaction can compete for I/O with history reads — requires tuning
  `ValueLogFileSize` / value-log GC (to verify under real load).
- **No replica or native HA:** BadgerDB is single-process/single-node. If the Go process goes
  down, the working memory of active sessions on that node is lost unless the Outbox has already
  consolidated the state into rqlite. The design **accepts this**: working memory is
  intentionally ephemeral; durable state lives in the facts tier.
- **Horizontal scaling:** being embedded, scaling Huma+Gollem+BadgerDB implies *sharding*
  tenants across processes/nodes (e.g. consistent hashing by `tenant_id`), not a shared
  BadgerDB cluster. There's no native Raft-style clustering here — that's what rqlite is for at
  the facts tier.
- **Sleep cycle backpressure:** if consolidation workers fall behind (or rqlite/Qdrant are
  down), `outbox:pending:*` grows unbounded inside BadgerDB. A backpressure policy or alert
  when the outbox exceeds a threshold still needs defining (to verify/design pending).

## Related decisions

- The local Outbox is the piece that upholds the "honest eventual consistency" guarantee — see
  [consistency](../architecture/consistency.md) and the
  [honest eventual consistency](../vision/principles.md#derived-operating-principles) principle.
- Discarding SurrealDB as the core (ADR-0003, [decisions](../decisions/index.md)) is partially
  based on this component: pulling working memory out of the process would lose exactly the
  microsecond latency BadgerDB provides.

## References

- [Tiered memory](../architecture/memory-tiers.md) — BadgerDB's role in the memory triangle.
- [Architecture overview](../architecture/overview.md) — request lifecycle and full stack.
- [rqlite — Facts Memory](rqlite-facts-memory.md) — the next tier, destination of the
  Outbox's consolidation.
- [ADRs](../decisions/index.md) — related stack decisions.
- Repo: [github.com/dgraph-io/badger](https://github.com/dgraph-io/badger)
