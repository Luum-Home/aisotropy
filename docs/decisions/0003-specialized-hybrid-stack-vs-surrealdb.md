---
type: ADR
title: "ADR-0003 — Specialized hybrid stack vs. multi-model SurrealDB"
description: Retroactive record of the decision to reject consolidating the core on a single multi-model database (SurrealDB) in favor of specialized components (BadgerDB, Qdrant, rqlite) split by access temperature.
tags: [decisions, adr, surrealdb, hybrid, memory-tiers, retroactive]
timestamp: 2026-07-08T00:00:00Z
---

# ADR-0003 — Specialized hybrid stack vs. multi-model SurrealDB

## Status

**✅ Accepted — retroactive formalization (2026-07-08), decision: hybrid (specialized stack).**
Aisotropy's core does **not** consolidate on a single multi-model database; it uses specialized
components (BadgerDB, Qdrant, rqlite) split by access temperature, per
[ADR-0001](0001-go-rust-in-process-first-stack.md). Listed in
[docs/decisions/index.md](index.md) as "accepted (hybrid)" since that index was written, with no
corresponding ADR file. This document is that missing file.

## Context

[docs/architecture/overview.md](../architecture/overview.md) records the alternative that was
considered and rejected for the core:

> **Alternative considered and rejected for the core:** SurrealDB (multi-model, Rust) could
> consolidate everything into a single binary. It was rejected for the core for two reasons:
> working memory loses latency if it leaves the process, and dedicated vector engines (Qdrant)
> mature better at scale. It stays on the radar for small MVPs.

This sits directly on top of [ADR-0001](0001-go-rust-in-process-first-stack.md)'s golden rule
(in-process where latency matters, dedicated services where scale matters): SurrealDB is a
single external multi-model engine (documents, graph, key-value, and vector search in one
server), which is the opposite shape of the tiered, specialized-per-workload stack Aisotropy
commits to in [tiered memory](../architecture/memory-tiers.md).

## Goals / Non-goals

**Goals**

- Decide whether the memory tiers (working memory, facts, semantic/vector) should be
  consolidated into one multi-model database (SurrealDB) or kept as specialized, separate
  components.
- Preserve the two hard constraints already fixed elsewhere: working-memory latency must stay
  in-process (µs), and vector search must be served by an engine that scales well as a dedicated
  service (per [ADR-0001](0001-go-rust-in-process-first-stack.md)).

**Non-goals**

- **Not** deciding the specific engine for the facts layer — that's
  [ADR-0004](0004-facts-memory-rqlite-vs-litefs.md) (rqlite vs. LiteFS).
- **Not** deciding the facts layer's physical multi-tenancy topology — that's
  [ADR-0007](0007-multitenancy-fisico-db-unica-vs-por-tenant.md).
- **Not** ruling out SurrealDB everywhere — it is explicitly left "on the radar for small MVPs,"
  i.e., for contexts smaller than Aisotropy's own core design goals.

## Options considered

### Option A — Consolidate on SurrealDB (single multi-model database) — discarded for the core

SurrealDB is multi-model (documents, graph, key-value, vector search) and written in Rust,
which could in principle replace BadgerDB (working memory), rqlite (facts/FTS5), and part of
Qdrant's role (vector search) with one engine and one binary/service. Discarded for two reasons
recorded in [docs/architecture/overview.md](../architecture/overview.md):

1. **Working memory loses latency if it leaves the process.** BadgerDB's role (live chat +
   summary buffer) is read/written on the hot path in µs, in-process. SurrealDB, as an external
   database (even if colocated), reintroduces a network/IPC hop that the µs latency budget for
   working memory doesn't tolerate — the same latency argument that keeps working memory
   in-process per [ADR-0001](0001-go-rust-in-process-first-stack.md).
2. **Dedicated vector engines mature better at scale.** Qdrant is a purpose-built vector engine
   (HNSW, payload filtering at scan time); a multi-model database's vector-search feature is one
   capability among several, not its core specialization. The overview treats Qdrant as
   maturing better at scale than a general-purpose multi-model engine's vector module.

### Option B — Specialized hybrid stack (BadgerDB + rqlite + Qdrant) — accepted

Keep the tiered-memory design: BadgerDB in-process for working memory (µs), rqlite networked
for facts/FTS5 (ms, per [ADR-0004](0004-facts-memory-rqlite-vs-litefs.md)), and Qdrant as the
dedicated vector engine for semantic memory. Each tier uses the engine specialized for its
access pattern and temperature, consistent with the golden rule in
[ADR-0001](0001-go-rust-in-process-first-stack.md) and with [tiered memory](../architecture/memory-tiers.md)'s
central design.

## Trade-off comparison table

| Criterion | A — SurrealDB (consolidated) | B — Specialized hybrid — accepted |
|---|---|---|
| Working-memory latency | Network/IPC hop even if colocated — breaks the µs budget | In-process (BadgerDB), µs, no hop |
| Vector search maturity at scale | One feature among several in a general-purpose engine | Purpose-built engine (Qdrant), matures better at scale per the overview |
| Operational surface | Potentially simpler — one database to run | More components (BadgerDB + rqlite + Qdrant), but each specialized |
| Fit with ADR-0001's golden rule | Conflicts — a single external multi-model DB doesn't separate by latency vs. scale | Directly implements the golden rule |
| Fit for small MVPs | Left explicitly viable ("stays on the radar for small MVPs") | Overkill for very small deployments — more moving parts |

## Recommended decision

**Option B (specialized hybrid) is recommended and already implemented for the core.** SurrealDB
is not adopted as the consolidation point for Aisotropy's memory tiers, for the two reasons
recorded in the overview: it costs working memory its in-process latency, and it doesn't match
Qdrant's maturity at scale for vector search. SurrealDB is explicitly **not discarded outright**
— it remains a live option "on the radar for small MVPs," i.e., deployments where the
specialized stack's operational overhead outweighs its latency/scale benefits.

## Consequences

**Positive**

- **Latency preserved for working memory:** BadgerDB stays in-process; no consolidation forces a
  network hop onto the hot path.
- **Vector search stays on its best-fit engine:** Qdrant's HNSW + payload filtering is used as
  designed rather than substituted with a general-purpose engine's vector feature.
- **Consistent with the golden rule:** this decision is a direct application of
  [ADR-0001](0001-go-rust-in-process-first-stack.md) rather than a separate, ad hoc call.
- **Escape hatch preserved:** SurrealDB remains a documented, valid option for small MVPs where
  the operational simplicity of one database outweighs the core's latency/scale requirements.

**Negative / technical debt**

- **More components to operate:** BadgerDB, rqlite, and Qdrant are three separate storage
  engines instead of one, each with its own operational surface (backups, observability,
  failure modes) — the direct trade-off already acknowledged in
  [ADR-0001](0001-go-rust-in-process-first-stack.md)'s consequences.
- **No head-to-head benchmark recorded.** The rejection of SurrealDB is reasoned from its
  architectural shape (external, general-purpose, multi-model) rather than from a measured
  latency/throughput comparison against BadgerDB/Qdrant — see Open Questions.

## Open questions

1. **No benchmark on record.** The two reasons for discarding SurrealDB (working-memory latency,
   vector-engine maturity) are architectural/reputational arguments in the source material, not
   numbers from a run comparison. Per the project's own Bellard discipline ("measure in bits/
   numbers, not adjectives," [compression thesis](../vision/compression-thesis.md)), a
   quantified comparison (BadgerDB in-process read/write latency vs. a colocated SurrealDB
   instance; Qdrant HNSW recall/latency at scale vs. SurrealDB's vector index) would strengthen
   this decision if it's ever revisited.
2. **"Small MVP" threshold undefined.** The overview leaves SurrealDB viable "for small MVPs"
   without defining what "small" means (tenant count, request volume, or team operational
   capacity) — no threshold is recorded anywhere in this knowledge base.

## References

- [docs/decisions/index.md](index.md) — original "accepted (hybrid)" ledger entry for ADR-0003.
- [docs/architecture/overview.md](../architecture/overview.md) — "Alternative considered and
  rejected for the core: SurrealDB."
- [ADR-0001 — Go+Rust stack, in-process-first](0001-go-rust-in-process-first-stack.md) — the
  golden rule this decision applies.
- [docs/architecture/memory-tiers.md](../architecture/memory-tiers.md) — the tiered-memory
  design this decision preserves.

---

Cross-links: [decisions index](index.md) · [architecture overview](../architecture/overview.md) ·
[ADR-0001](0001-go-rust-in-process-first-stack.md)
