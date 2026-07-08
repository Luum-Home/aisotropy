---
type: Section Index
title: Architecture decisions (ADRs)
description: Chronological log of Aisotropy's architecture decisions, with context, options, trade-offs, and consequences. Fulfills the role of the OKF bundle's log.md.
tags: [decisions, adr, log]
timestamp: 2026-07-08T00:00:00Z
---

# Architecture decisions (ADRs)

Decision log. Each ADR: **Context → Options considered → Decision → Consequences**
(Google design-doc style format: includes *goals/non-goals* and *discarded alternatives*).

| ADR | Title | Status |
|---|---|---|
| 0000 | Framework name | **accepted** — "Aisotropy" (AI + isotropy) |
| 0001 | Go+Rust stack, in-process-first | accepted (implicit in the source conversations) |
| 0002 | Anti-framework-magic (discard LangChain; struct as single contract) | accepted |
| 0003 | Specialized hybrid stack vs. multi-model SurrealDB | accepted (hybrid) |
| [0004](0004-facts-memory-rqlite-vs-litefs.md) | rqlite vs LiteFS for facts memory | **✅ accepted → rqlite** |
| [0005](0005-datafusion-go-rust-boundary.md) | Rust↔Go boundary for DataFusion (FFI vs worker vs WASM) | **✅ accepted → separate Rust worker** |
| [0006](0006-reranker-cohere-vs-bge-local.md) | Reranker: Cohere SaaS vs local BGE-reranker | **✅ accepted → local BGE** |
| [0007](0007-multitenancy-fisico-db-unica-vs-por-tenant.md) | Physical multi-tenancy: single filtered DB vs DB-per-tenant | **✅ accepted → hybrid/tiered** |
| [0008](0008-reflection-loop-gollem-deferral.md) | Phase 1 reflection loop: in-house loop vs Gollem | **✅ accepted → in-house loop now, Gollem in Phase 2** |

## Decisions already made in the source conversations

These were closed in the original design and are documented as retroactive "accepted" ADRs:

- **Discard LangChain** for its overhead, hidden prompts, and invisible token loops (ADR-0002).
- **Discard Turso as core** for its BSL license on `sqld`; use rqlite (MIT) or LiteFS
  (Apache-2.0) to avoid licensing gray areas.
- **Discard GlareDB** (abandoned at the end of 2025); use Apache DataFusion, its core engine.
- **Do not consolidate on SurrealDB** for the core (ADR-0003): working memory loses latency
  out of process, and Qdrant scales better as a dedicated vector engine.

## ADRs written

- **[ADR-0004 — rqlite vs LiteFS](0004-facts-memory-rqlite-vs-litefs.md):** ✅ **accepted → rqlite**.
  Decisive finding: LiteFS is in **maintenance mode** (Fly.io: "limited updates, no significant
  development plan, no support"; last release April 2025) — the same pattern that sank
  GlareDB. rqlite also provides configurable strong consistency (Raft), key for the durable ACK the
  Transactional Outbox needs.
- **[ADR-0005 — DataFusion boundary](0005-datafusion-go-rust-boundary.md):** ✅ **accepted →
  separate Rust worker** (batch subprocess with Parquet/Arrow IPC). Since the sleep cycle is
  batch/offline, there's no in-process latency requirement; CGO would break Go's static binary,
  and WASM is still experimental in 2026. Evolution path: Arrow Flight SQL if interactive
  analytics appears.
- **[ADR-0006 — Reranker](0006-reranker-cohere-vs-bge-local.md):** ✅ **accepted → local
  BGE-reranker-v2-m3** (Apache-2.0), with Qwen3-Reranker-0.6B as a drop-in upgrade; **Cohere
  discarded**. In the hot path, a SaaS adds 80–150 ms of network latency to the TTFT and sends
  tenant data outside (violates sovereignty). The open-vs-Cohere quality gap **closed in 2026**.
  jina discarded for its non-commercial license. It's the first step of the
  [sovereign NN core (Bet 5)](../research/bet-05-sovereign-nn-core.md).
- **[ADR-0007 — Physical multi-tenancy](0007-multitenancy-fisico-db-unica-vs-por-tenant.md):** ✅
  **accepted → hybrid/tiered**: pooled (single DB filtered by `tenant_id` over an rqlite cluster)
  as the default; a dedicated cluster only for large or compliance-critical tenants. Decisive
  finding: **rqlite is a single DB per Raft cluster** (issue #927 open) → a literal
  "DB-per-tenant" = one full cluster per tenant = unviable at scale. It also resolves the leader
  write ceiling flagged by ADR-0004.

- **[ADR-0008 — Phase 1 reflection loop](0008-reflection-loop-gollem-deferral.md):** ✅ **accepted →
  in-house loop (`Reflect[T]`) now, Gollem in Phase 2**. Gollem (`core.RunWithReflection[T]`)
  requires wrapping `llm.Client` in `core.Model` and can't be exercised without an API key; the
  walking skeleton prioritizes the verifiable SSE flow. Tracked as debt with an explicit
  integration plan for Phase 2.

## Next ADRs to write

- The retroactive "accepted" ones (0001/0002/0003) can be written as formal ADRs when convenient.
- New ones arising from the research phase (e.g., the codec format for [Bet 2](../research/bet-02-working-memory-codec.md), deterministic model versioning).
