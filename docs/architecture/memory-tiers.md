---
type: Concept
title: Tiered memory
description: Aisotropy's three-tier + batch memory system, organized by access temperature — the framework's central differentiator.
tags: [architecture, memory, badgerdb, qdrant, rqlite, datafusion, engram]
timestamp: 2026-07-08T00:00:00Z
---

# Tiered memory

Aisotropy's real differentiator isn't the LLM: it's **how it remembers**. Instead of a single
database, memory is split by **"temperature" and lifecycle**, just like the memory hierarchy of a
computer (the [LLM OS](overview.md#llm-os) model). Each tier uses the optimal tool for its
temperature.

## The triangle (+ the sleep cycle)

| Tier | Analogy | Store | Content | Latency | Access |
|---|---|---|---|---|---|
| **Working** | RAM / L1 | [BadgerDB](../components/badgerdb-working-memory.md) (Go, in-process) | Last N messages + summary buffer of the active session | µs | hot, every request |
| **Facts / Episodic** | Disk / SSD | [rqlite](../components/rqlite-facts-memory.md) or LiteFS (SQLite+FTS5, networked) | Tasks, rules, hard preferences, summarized episodes | ms | warm, via FTS5 |
| **Semantic** | Long-term associative | [Qdrant](../components/qdrant-semantic-memory.md) (Rust) | Embeddings of the tenant's docs/massive knowledge | ms | cold, via similarity |
| **Consolidation** | Sleep / defragmentation | [DataFusion](../components/datafusion-sleep-cycle.md) (Rust) | Batch analysis of everything above | offline | batch, sleep cycle |

## 1. Working memory (short-term)

**What:** the last ~10-15 messages of the active chat + the condensed *summary buffer*.
**Where:** BadgerDB, embedded in the Go process, under the composite key `tenant:user:session`.
**Why this way:** reading the history to build the prompt takes **microseconds** — zero network
round-trip to an external Redis.

**Token optimization — Summary Buffer:** when the conversation exceeds N messages, an asynchronous
goroutine condenses the oldest ones into a "session state" paragraph and removes them from the
active buffer, so the working-memory window aisotropy hydrates into each prompt **never grows
indefinitely** — Gollem is handed an already-capped prompt per call and keeps no cross-request window
of its own (each reflection/agent run is a fresh, stateless `Agent[T]`). This is pure
[context engineering](../vision/principles.md#2-context-engineering--prompt-engineering).

## 2. Facts / episodic memory (Engram-style)

**What:** the user's "learning journal" — decisions, hard preferences, pending tasks, summarized
episodes ("configured stock alerts for product X").
**Where:** SQLite + FTS5, distributed via rqlite (Raft) or LiteFS (FUSE). See the pending decision
in [ADR-0004](../decisions/index.md).
**Why FTS5 and not vectors:** for stable, high-density facts, **keyword search** is instant and
doesn't require computing embeddings. It's the direct application of the
[KB-over-RAG](../vision/principles.md#3-curated-knowledge-base--rag-for-stable-knowledge) principle. If the user says *"remember that
production uses AWS"*, it's retrieved instantly via text match.

**The Engram pattern** (from Gentleman Programming — MIT, the *pattern* is adopted, not the
binary):

- **Distilled, not raw chat.** A distillation of knowledge is stored, not the entire conversation.
- **`Relevance × Importance × Recency` scoring.** Not all records carry equal weight; only the
  3-4 most relevant engrams are injected into the system prompt.
- **Contradiction detection (supersede).** If a new fact contradicts an old one
  ("no longer using product X"), the old one is archived or down-weighted. Avoids cognitive
  dissonance.
- **Checkpoint before compaction.** Extracts decisions/tasks before losing context.

```sql
-- Hot-path retrieval (conceptual)
SELECT content, importance, created_at
FROM memories
WHERE tenant_id = ? AND user_id = ?
  AND memories MATCH 'refactor database'
ORDER BY importance DESC, created_at DESC
LIMIT 5;
```

## 3. Semantic memory (evolving profile)

**What:** the tenant's deep knowledge base and the user's profile — docs, manuals, massive
databases.
**Where:** Qdrant, with **rigid payload filtering**: the `tenant_id`/`user_id` filter is applied
*during* the HNSW graph scan (not afterward), so it's multi-tenant by design and ultra fast.
Quantization to cut RAM by up to ~90%.
**When:** only when the prompt requires RAG over unstructured knowledge. Not everything is a
vector — stable facts go to tier 2.

**Multi-kind (ADR-0017/0018/0019).** Every vector carries a `Kind`, each with its own lifecycle,
bound, and retrieval weight — the tier is not one undifferentiated blob:
- **Document** — curated knowledge ingested through `IngestKnowledge` / `POST .../knowledge`
  (user-private or tenant-shared, ADR-0019). Persists; the tier's intended high-value input.
- **Fact** — distilled facts *dual-indexed* through the `engram` aggregate facade (ADR-0017),
  keyed by the fact id: superseding or deleting the fact removes its vector, so the two never drift.
- **Turn** — raw conversational turns, **bounded** by a TTL/cap pruner (default 168h / 500), and
  kept out of the primary RAG so low-value turns never crowd out curated knowledge.

Retrieval (`hydrateSystemPrompt`) searches `Document`+`Fact` at user scope and merges the
tenant-shared `Document` bucket. The embedder is BYO (`semantic.Embedder`): a deterministic
`HashEmbedder` for dev/tests, a real OpenAI-compatible `APIEmbedder` via `AISOTROPY_EMBED_URL` (which
can point at a local server — sovereignty preserved). See ADR-0017, ADR-0018, ADR-0019.

```go
// Search with isolation (conceptual)
qdrant.Search(ctx, userEmbedding, Filter{
    "tenant_id": tenantID,
    "user_id":   userID,
})
```

## 4. Batch consolidation — the "Sleep Cycle"

Sophisticated agents **don't learn in real time while the user waits** (that would destroy
latency). On the hot path the system only *reads* memory and *records* raw events. Intelligence
is consolidated cold.

**Hot-path operation (day):** the request reads memory and writes the message + a pending event
in the [BadgerDB Outbox](consistency.md).
**Cold consolidation (night / idle time):** workers spin up **DataFusion** (a columnar engine in
Rust) that reads the `.db` files from rqlite/LiteFS or compacted Parquet and runs massive
analytics to:

- **Deduplicate** — if the user repeated something 3 times, only one clean record remains.
- **Update contradictions** — reconciles stale facts with new ones.
- **Clean up noise** — deletes "hi", "thanks," trivial chatter with no cognitive value.
- **Detect patterns** — flags obsolete tasks, measures friction for [primitive self-improvement](../vision/thesis.md).

The refined conclusions are reinjected into Qdrant (updated vectors) and rqlite (consolidated
facts). Flow detail and guarantees in [consistency](consistency.md).

## Primitive self-improvement (behavioral memory)

A fourth type of "memory": the user's **tools/primitives are not static**. Their *metadata*
(the description the LLM reads, few-shot examples, system prompt) is **cloned per `user_id`** and
mutates. When the sleep cycle detects friction (many reflection-loop retries, or the user
explicitly corrects the bot), an optimizer agent rewrites the description or adds a
counter-example. **Executable code is never cloned** — only the metadata the LLM interprets. See
[thesis](../vision/thesis.md) and [granular control](granular-control.md).
