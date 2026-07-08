---
type: Section Index
title: Components
description: Deep-dive on each piece of the Aisotropy stack — role, API, integration, gotchas, and associated decisions.
tags: [components, stack]
timestamp: 2026-07-08T00:00:00Z
---

# Components

Each component has its own document with: role in the system, integration surface with the rest of
the stack, key configuration/patterns, and gotchas. **All 6 leaf docs are written.**

| Component | Language / License | Role | Doc status |
|---|---|---|---|
| [Huma](huma-api.md) | Go / MIT | Contract and API layer (OpenAPI 3.1, validation, SSE). | ✅ done |
| [Gollem](gollem-orchestrator.md) | Go / MIT ✓verified | Cognitive orchestrator (structured output, reflection loop / output repair). | ✅ done |
| [BadgerDB](badgerdb-working-memory.md) | Go / Apache-2.0 | Working memory + Transactional Outbox. | ✅ done |
| [Qdrant](qdrant-semantic-memory.md) | Rust / Apache-2.0 | Semantic memory (vectors, payload filtering in HNSW). | ✅ done |
| [rqlite / LiteFS](rqlite-facts-memory.md) | Go MIT / Apache-2.0 | Engram-style facts memory (SQLite+FTS5). | ✅ done |
| [Apache DataFusion](datafusion-sleep-cycle.md) | Rust / Apache-2.0 | Sleep cycle analytics engine. | ✅ done |

## Open questions per component

- **Gollem:** license **MIT confirmed** (GitHub API, `fugue-labs/gollem`); the site only said "open source." Streaming via Go 1.23+ iterators with 4 modes; "output repair" = the reflection loop. Pending: OTel hooks and dynamic tool injection.
- **rqlite vs LiteFS:** **resolved → rqlite** (Raft, consistency under failure, LiteFS in maintenance mode). See [ADR-0004](../decisions/0004-facts-memory-rqlite-vs-litefs.md).
- **DataFusion:** **resolved → separate Rust worker (batch subprocess, Parquet/Arrow IPC)**; DataFusion has no official Go bindings. See [ADR-0005](../decisions/0005-datafusion-go-rust-boundary.md).
- **Reranker:** Cohere (SaaS) vs. local BGE-reranker (sovereignty) — pending ADR-0006.
