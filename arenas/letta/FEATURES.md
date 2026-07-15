---
type: Feature matrix
title: "Letta (MemGPT) — features vs aisotropy"
description: Coverage map of Letta capabilities compared to aisotropy's built-ins, grounded in the real letta-client 1.12.1 API.
tags: [arenas, letta, features, comparison]
timestamp: 2026-07-15T00:00:00Z
---

# Letta — Features

> Grounded in the **real** `letta-client==1.12.1` SDK. API symbols named below
> (`client.blocks.list`, `agents.create(response_format=...)`, `Passage`, etc.) are
> the genuine current surface, verified against the installed package. See
> [`README.md`](./README.md) for what runs offline vs. what needs a live server.

## Coverage classes

Each feature is one of: **BUILT-IN** (framework provides it), **CONVENTION**
(possible but developer-enforced, no guardrail), **DIY** (you build it), **PARTIAL**,
**PAID** (only in the hosted tier), **NOT-POSSIBLE**.

| # | Feature | Class | Real API surface / note |
|---|---|---|---|
| 1 | Multi-tenant isolation | **CONVENTION** | `identity_id`/`project_id`/`tags` are optional, developer-passed filters on `blocks.list`/`agents.list`/`passages.search`; per-user **key** isolation is **PAID** (Letta Cloud projects) |
| 2 | Structured output + retry | **BUILT-IN** (no repair loop) | `agents.create(response_format={"type":"json_schema", ...})`; validation provider-side, no fail-closed reflection retry |
| 3 | Tool calling | **BUILT-IN** | LLM self-manages memory via base tools; `include_base_tools=True` |
| 4 | Streaming (SSE) | **BUILT-IN** | `agents.messages.create(..., stream=True)` / `.stream()` |
| 5 | Working memory | **BUILT-IN** | Core Memory `Block`s (in-context editable); `client.blocks` / `agents.blocks` |
| 6 | Semantic memory (vectors) | **BUILT-IN** | Archival Memory `Passage`s; `client.passages.search`, `client.archives` |
| 7 | Facts memory (journal) | **PARTIAL** | Recall Memory = searchable message history (`agents.messages`), not a dedicated FTS task/rule journal |
| 8 | Cost governance | **DIY** | No pre-spend gate, no per-tenant meter |
| 9 | Observability | **PARTIAL** | Server bundles OTel exporters; no per-tenant `/costs`/`/health` product surface |
| 10 | Sovereignty / licensing | **BUILT-IN** (Apache 2.0) | Self-hostable via Docker/Postgres |
| 11 | Language | Python | Server-side durable objects (not in-process) |

## Built-in capabilities

### Memory hierarchy — isomorphic to aisotropy (real types)
- **Core Memory** → `Block` (in-context editable blocks; RAM) ≈ BadgerDB working memory
- **Recall Memory** → message history (out-of-context searchable; disk) ≈ SQLite+FTS5 facts memory
- **Archival Memory** → `Passage` (unbounded vector store; cold) ≈ Qdrant semantic memory
- **AISOTROPY COMPARISON:** the closest conceptual match to aisotropy's memory tiers.
  Letta *validates the memory-OS thesis* independently — and here we use its **real**
  `BlockResponse`/`Passage` types, not a facsimile.

### Sleep-time compute — productized
- **Mechanism:** paired background agent consolidates memory during idle time
  (`agents.create(enable_sleeptime=True)` in the real API).
- **AISOTROPY COMPARISON:** direct analog of aisotropy's DataFusion sleep cycle. Letta is
  the **only** framework in this matrix that ships productized sleep-time consolidation.
  aisotropy's is dormant behind `AISOTROPY_SLEEPCYCLE_BIN`.

### Tool calling (LLM-managed)
- **Mechanism:** the LLM manages memory via base tools (`core_memory_append`,
  `archival_memory_insert/search`) — probabilistic.
- **AISOTROPY COMPARISON:** opposite approach. Letta: LLM self-manages memory (emergent).
  aisotropy: runtime-hydrated, deterministic and auditable.

## Convention / DIY / missing

### Multi-tenant isolation — CONVENTION (proven leak)
- **Status:** `client.blocks.list(identity_id=?, project_id=?, tags=?)` — every filter
  optional and developer-passed. Omit them and you scan the whole org. `blocks.retrieve(id)`
  applies no tenant check. Proven in `scripts/smoke.py` and `tests/test_isolation.py`
  against the real types.
- **AISOTROPY COMPARISON:** aisotropy enforces isolation **by construction** — ctx
  propagation + per-statement/key scoping; the unscoped read is *unrepresentable*.

### Structured output (fail-closed) — no repair loop
- **Status:** `response_format` schema is honored by the provider, but there is no
  fail-closed reflection retry on a schema break.
- **AISOTROPY COMPARISON:** aisotropy fails closed (502 on `/chat`) with a Gollem
  reflection loop.

### Cost governance — DIY
- **AISOTROPY COMPARISON:** aisotropy has a pre-spend quota gate + per-tenant cost meter.

### Observability — PARTIAL
- **Status:** the server bundles OpenTelemetry exporters; there's no per-tenant cost/health
  product surface.
- **AISOTROPY COMPARISON:** aisotropy ships OTel gen_ai semconv tracing, `/costs`, `/health`.

## Summary

Letta is the **closest conceptual neighbor** to aisotropy. Its memory hierarchy
(Core/Recall/Archival) is nearly isomorphic to aisotropy's (BadgerDB/SQLite+FTS5/Qdrant),
and it's the only framework that has productized sleep-time consolidation.

But Letta is Python, LLM-self-managed memory (probabilistic vs aisotropy's deterministic),
server-side durable objects, and convention-based multi-tenancy. It validates the
memory-OS thesis while leaving aisotropy's exact combination (Go, isolation-by-construction,
runtime-hydrated deterministic memory) open.

**vs. aisotropy:** Letta wins on memory-hierarchy productization and sleep-time compute;
aisotropy wins on isolation-by-construction, deterministic memory, Go performance, and cost
governance. The trade: productized memory-OS with LLM-driven uncertainty vs. deterministic
runtime with constitutional isolation guarantees.
