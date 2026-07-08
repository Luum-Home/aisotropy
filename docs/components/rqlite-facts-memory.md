---
type: Component
title: rqlite — Facts Memory
description: The Disk/SSD tier of the LLM OS — SQLite distributed via Raft with FTS5, Engram-style episodic memory, destination for Outbox consolidation.
tags: [components, memory, rqlite, facts-memory, fts5, engram, sql]
timestamp: 2026-07-08T00:00:00Z
---

# rqlite — Facts Memory

**Store:** [rqlite](https://github.com/rqlite/rqlite) (Go, MIT, SQLite replicated via Raft consensus,
exposed over HTTP). **Alternative under evaluation:** LiteFS (Apache-2.0, FUSE-based replication) — the
decision is open in [ADR-0004](../decisions/index.md); this document describes rqlite as the
primary option.

## Role in the system

rqlite is the **Disk/SSD** tier of the [LLM OS](../architecture/overview.md#llm-os) model: the
facts/episodic memory, lukewarm (ms latency, over the network), as opposed to working memory
in BadgerDB (µs, in-process) and below the vector-based semantic memory in Qdrant. See the
full triangle in [tiered memory](../architecture/memory-tiers.md).

It implements the **Engram pattern** (from Gentleman Programming, MIT — the pattern is adopted, not
the binary): a "learning journal" of decisions, hard preferences, and summarized episodes of the
user, retrievable via **text matching (FTS5)** rather than vector similarity. It's the
direct application of the [KB-over-RAG](../vision/principles.md#3-curated-knowledge-base--rag-for-stable-knowledge) principle: for
stable, high-density knowledge, keyword search is instant and doesn't require computing
embeddings.

## Responsibilities

- Persist pending tasks, rules, hard user preferences, and summarized episodes
  ("configured stock alerts for product X").
- Serve FTS5 search during the memory hydration step of every request (in parallel with
  BadgerDB and Qdrant, see [request lifecycle](../architecture/overview.md#lifecycle-of-a-request)).
- Receive consolidation from the **BadgerDB Outbox**: pending events processed by the
  sleep cycle workers end up as new or updated rows here.
- Also store **per-user cloned primitives/tools** (metadata: description, few-shot
  examples) when the user's role requires it — see
  [primitive self-improvement](../architecture/memory-tiers.md#primitive-self-improvement-behavioral-memory).
- Detect and resolve **contradictions** (supersede) between new and old facts.

## Data model / keys

Conceptual schema (SQLite + FTS5 virtual table, exact DDL to verify at implementation):

| Column | Content | Notes |
|---|---|---|
| `id` | PK, or idempotent key | used by `INSERT OR REPLACE` |
| `tenant_id`, `user_id` | Isolation — **mandatory in every query** | never rely on an implicit filter |
| `content` | Distilled text (not raw chat) | indexed by the FTS5 virtual table |
| `importance` | Manual/heuristic weight of the fact | used in scoring |
| `created_at` / `updated_at` | Recency | used in scoring |
| `superseded_by` | Reference to the fact that replaces it, if any | null while the fact is current |

**Hard rule, no exceptions:** every read query filters `WHERE tenant_id = ? AND user_id = ?`
— multi-tenant isolation lives in every statement, not in an intermediate layer (see the
[ctx-based isolation principle](../vision/principles.md#derived-operating-principles)).

```sql
-- Hot-path retrieval (conceptual, see tiered memory)
SELECT content, importance, created_at
FROM memories
WHERE tenant_id = ? AND user_id = ?
  AND memories MATCH 'refactor database'
ORDER BY importance DESC, created_at DESC
LIMIT 5;
```

**Idempotency:** every consolidation write uses `INSERT OR REPLACE` (or an equivalent `UPSERT`)
on a stable key — necessary because the Outbox worker may retry the same event
after a partial failure, and must not duplicate the fact.

## Integration surface

- Exposed over **HTTP** (not an embedded library like BadgerDB) — Gollem consults it as a
  network service, although it can run colocated on the same host to minimize latency.
- **`Relevance × Importance × Recency` scoring:** before injecting results into the system prompt,
  they are ranked and trimmed to the top 3-4 most relevant engrams — the full table is never
  dumped into the context.
- **Sleep cycle workers** (triggered by the BadgerDB Outbox) write here after deduplicating,
  reconciling contradictions, and scrubbing noise via DataFusion — see
  [batch consolidation](../architecture/memory-tiers.md#4-batch-consolidation--the-sleep-cycle).
- **DataFusion** reads rqlite's `.db` files (or compacted Parquet) for offline batch analytics;
  it doesn't write directly, it delivers results that the worker writes back with `INSERT OR REPLACE`.

## Key configuration / patterns

- **Raft consensus:** every write goes through the Raft log before being applied — durability
  via replication, not a single node's fsync. Tolerates node failures as long as a majority stays alive.
- **Read consistency levels** (to verify against the exact version in use): `none`
  (local, no guarantees, fastest), `weak` (default — queries the leader, small staleness
  window if the node stopped being leader), `linearizable` (waits for the commit index before
  reading, guarantees *read-your-writes*), `strong` (routes the read through Raft's own log —
  discouraged in production for its cost). Aisotropy should explicitly document which level each
  path uses (context hydration vs. writing a critical preference) — **still to be defined**.
- **Checkpoint before compaction:** the Engram pattern requires extracting decisions/tasks from the
  active context *before* BadgerDB's summary buffer purges them — the opportunity window for
  this is the `outbox:pending:*` event.
- **Distilled, not raw chat:** the full conversation is never inserted; only the summary of
  cognitive value (filtering out "hi," "thanks," trivial chat, in the sleep cycle).

## Gotchas and risks

- **Read-your-writes isn't free:** with `weak` consistency (the most common/fastest), a fact just
  written by the sleep cycle might not immediately show up in a hot-path read if it hits a
  different node or there's a replication window. If the product promises "remember that..." with
  immediate effect, `linearizable` should be evaluated for that specific route — a conscious
  latency trade-off, not an accidental one.
- **FTS5 is not semantic:** it only matches text/tokens. User synonyms or paraphrases may fail to
  find a fact saved with different wording — unlike Qdrant. This is an accepted limitation of the
  KB-over-RAG principle, not a bug, but it needs to be communicated clearly.
- **Open store decision (ADR-0004):** rqlite (Raft over HTTP, MIT) vs. LiteFS (FUSE +
  block-level replication, Apache-2.0) have different consistency and operational models.
  Migrating from one to the other after data is in production is not trivial — this decision
  should close early. See [ADRs](../decisions/index.md).
- **Write scale:** every relevant message potentially generates a new row via the
  Outbox. Under high tenant concurrency, Raft's write throughput (everything goes through
  the leader) may become the bottleneck before BadgerDB or Qdrant — no real load data
  exists yet (to verify).
- **Single-DB vs. per-tenant DB multi-tenancy:** still open as ADR-0007 ([decisions](../decisions/index.md))
  — directly affects this component's design (a single filtered table vs. separate databases
  per tenant in rqlite/Turso).

## Related decisions

- [ADR-0004](../decisions/index.md) — rqlite vs. LiteFS for facts memory (**pending**,
  the most urgent one for this component).
- [ADR-0007](../decisions/index.md) — multi-tenancy strategy: single filtered DB vs. DB-per-tenant
  (**pending**).
- Discarding Turso as core due to its BSL license on `sqld` (documented in
  [decisions](../decisions/index.md)) — the direct reason why rqlite (MIT) and LiteFS
  (Apache-2.0) remained as the only evaluated options, in line with the
  [sovereignty](../vision/principles.md#derived-operating-principles) principle (MIT/Apache-2.0 only).

## References

- [Tiered memory](../architecture/memory-tiers.md) — rqlite's role in the memory triangle
  and the full Engram pattern.
- [Architecture overview](../architecture/overview.md) — request lifecycle.
- [Consistency](../architecture/consistency.md) — Outbox and consolidation guarantees.
- [BadgerDB — Working Memory](badgerdb-working-memory.md) — origin of the events this
  component consolidates.
- [ADRs](../decisions/index.md) — related stack decisions.
- Repo: [github.com/rqlite/rqlite](https://github.com/rqlite/rqlite) ·
  [Read consistency docs](https://rqlite.io/docs/api/read-consistency/)
