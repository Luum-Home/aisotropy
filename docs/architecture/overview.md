---
type: Concept
title: Architecture overview
description: The full stack, its licenses, the "LLM OS" model, and the lifecycle of a request in Aisotropy.
tags: [architecture, stack, llm-os, lifecycle]
timestamp: 2026-07-08T00:00:00Z
---

# Architecture overview

## The stack (closed, 100% MIT/Apache-2.0)

| Layer | Technology | Language / License | Role |
|---|---|---|---|
| API & Contracts | [Huma](../components/huma-api.md) | Go / MIT | Validates HTTP, extracts `tenant_id`/`user_id`/scopes, autogenerates OpenAPI 3.1, SSE with cancellation via `ctx`. |
| AI Orchestration | [Gollem](../components/gollem-orchestrator.md) | Go / MIT | In-process cognitive engine, structured outputs, self-repair reflection loop. |
| Working Memory | [BadgerDB](../components/badgerdb-working-memory.md) | Go / Apache-2.0 | Live chat + summary buffer, in-process (µs). Dual role: local Transactional Outbox. |
| Semantic Memory | [Qdrant](../components/qdrant-semantic-memory.md) | Rust / Apache-2.0 | Vectors with rigid payload filtering by tenant/user during the HNSW scan. |
| Facts Memory | [rqlite](../components/rqlite-facts-memory.md) or LiteFS | Go MIT / Apache-2.0 | Engram-style task/rule journal, SQLite + FTS5, networked. |
| Batch analytics engine | [Apache DataFusion](../components/datafusion-sleep-cycle.md) | Rust / Apache-2.0 | Offline consolidation in the sleep cycle (replaces GlareDB, abandoned). |

**Golden rule of the stack:** *"specialized tools for specialized tasks, separated by access
temperature."* In-process (Go) where latency matters; dedicated services (Rust) where scale
matters. The only heavy service meant to scale independently is Qdrant.

> **Alternative considered and rejected for the core:** SurrealDB (multi-model, Rust) could
> consolidate everything into a single binary. It was rejected for the core for two reasons:
> working memory loses latency if it leaves the process, and dedicated vector engines (Qdrant)
> mature better at scale. It stays on the radar for small MVPs. See [ADR-0003](../decisions/index.md).

## The "LLM OS" model  {#llm-os}

Aisotropy is conceived as an operating system whose "CPU" is the LLM (Karpathy's mental model,
see [principles](../vision/principles.md#1-the-llm-os-model)):

```
        ┌───────────────────────────────────────────────────────────┐
        │  Huma  ── syscall layer (contract, validation, SSE)        │
        ├───────────────────────────────────────────────────────────┤
        │  Gollem ── kernel (agent loop, reflection, tool dispatch)  │
        │     CPU = LLM   ·   RAM = context window                   │
        ├───────────────────────────────────────────────────────────┤
        │  Memory hierarchy (by access temperature):                 │
        │    L1/RAM   BadgerDB   live chat + summary      (µs, local)│
        │    Disk     rqlite     facts/tasks FTS5          (ms, net) │
        │    LT assoc Qdrant     semantic knowledge        (vector)  │
        │    Sleep    DataFusion offline batch consolidation         │
        ├───────────────────────────────────────────────────────────┤
        │  context.Context ── process descriptor (isolates tenant)   │
        └───────────────────────────────────────────────────────────┘
```

## Lifecycle of a request

```
[User → Huma Endpoint]
   │
   ├─ 1. Huma validates the contract and extracts (tenant_id, user_id, scopes) → ctx
   │
   ├─ 2. Quota control: token-bucket in BadgerDB → circuit breaker
   │       (if the tenant exhausted its quota → degrade to a cheap model or 429)
   │
   ├─ 3. Memory hydration IN PARALLEL (goroutines):
   │       ├─ BadgerDB → summary + last N messages                (µs)
   │       ├─ rqlite   → relevant facts/tasks (FTS5 match)         (Engram)
   │       ├─ Qdrant   → semantic RAG filtered by tenant/user (if applicable)
   │       └─ rqlite   → primitives/tools cloned per user (based on role)
   │
   ├─ 4. Gollem concatenates surgical context (no bloated prompts) and
   │       injects ONLY the tools authorized by the user's scopes
   │
   ├─ 5. Gollem → LLM (forced structured output). If it breaks the schema →
   │       reflection loop with the exact typing error (max N retries)
   │
   ├─ 6. SSE stream to the user (minimal TTFT). If the user cancels →
   │       ctx cancelled → Gollem cuts the LLM call → stops spending tokens
   │
   └─ 7. SLEEP CYCLE (async, post-response):
           BadgerDB Outbox → workers → update Qdrant/rqlite (idempotent,
           with backoff); DataFusion consolidates in cold storage (dedup, contradictions, noise)
```

Details of each step in [request lifecycle](request-lifecycle.md) and
[consistency](consistency.md).

## Why this design wins in enterprise

- **Absolute isolation:** the `ctx` carries `tenant_id`/`user_id` from Huma down to every query.
  Zero context leaks between concurrent requests. See [multi-tenancy](multi-tenancy.md).
- **Token efficiency by design:** each layer delivers only what's relevant, instead of dumping
  full history/raw RAG into the prompt. **Design target: ~70% fewer input tokens than a default
  LangChain-style full-history flow — unmeasured today.** To be validated by the reproducible
  harness in [`bench/token-economy/`](../../bench/token-economy/); per
  [principles](../vision/principles.md#derived-operating-principles), an unmeasured efficiency
  claim is treated as a design bug, not a fact, until that harness reports a number.
- **Standard observability:** everything in-process in Go → one OTel Span per interaction with
  cost per token attributable per tenant. See [observability](observability.md).
- **Minimal infra cost:** Huma+Gollem+BadgerDB+rqlite compile into a native binary
  (Distroless, MB of RAM). Only Qdrant scales separately.
