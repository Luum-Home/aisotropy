---
type: Feature matrix
title: "LangGraph — features vs aisotropy"
description: Coverage map of LangGraph capabilities compared to aisotropy's built-ins.
tags: [arenas, langgraph, features, comparison]
timestamp: 2026-07-15T00:00:00Z
---

# LangGraph — Features

## Built-in capabilities

### Structured output (FAIL-CLOSED)
- **Status:** Built-in (via LangChain)
- **Mechanism:** `with_structured_output` + `OutputFixingParser` / `RetryOutputParser`
- **Retry:** Opt-in — developer must attach `OutputFixingParser` manually
- **AISOTROPY COMPARISON:** Equivalent capability, but LangChain's is **opt-in** (developer must
  remember to attach the parser). aisotropy's is **default/runtime-wired** — no opt-in needed.
  This is the "default posture" nuance: same capability, different default.

### Tool calling
- **Status:** Built-in
- **Mechanism:** Graph nodes can be tools; `ToolNode` for execution
- **Integration:** First-class in the graph builder
- **AISOTROPY COMPARISON:** Equivalent to aisotropy's `WithTypedTool[T]`. Both are type-safe.
  LangGraph uses graph nodes; aisotropy uses Go generics.

### Streaming
- **Status:** Built-in
- **Mechanism:** `StreamingStdioCallbackHandler` + async iteration
- **Cancellation:** Python `asyncio.CancelledError` propagates
- **AISOTROPY COMPARISON:** Equivalent to aisotropy's Huma SSE + `ctx` cancellation. Both stream
  deltas; aisotropy adds HTTP-level `Accept: text/event-stream` semantics.

### Working memory
- **Status:** Built-in
- **Mechanism:** Checkpointers (SQLite, Postgres, Redis) + Store
- **Features:** Durable execution, state persistence, graph checkpoints
- **AISOTROPY COMPARISON:** LangGraph's checkpointers are more mature (SQLite/Postgres/Redis).
  aisotropy's BadgerDB is in-process µs — faster but less durable. Both are built-in.

### Semantic memory (vectors)
- **Status:** Built-in
- **Mechanism:** `Store` with semantic cache + vector retrieval
- **Features:** Embedding integration, similarity search
- **AISOTROPY COMPARISON:** Both have built-in vector memory. LangGraph uses LangChain embeddings;
  aisotropy uses in-process cosine brute-force (active) + Qdrant gRPC (dormant).

## DIY / missing capabilities

### Facts memory (journal)
- **Status:** Not built-in
- **Pattern:** No equivalent — would need PostgreSQL/SQLite with FTS5 manually wired
- **AISOTROPY COMPARISON:** aisotropy ships Engram-style SQLite+FTS5 facts journal out of the box.

### Cost governance
- **Status:** DIY
- **Pattern:** `UsageLimits` per-run (via LangChain), not per-tenant
- **AISOTROPY COMPARISON:** aisotropy has **pre-spend quota gate** + **per-tenant cost meter**.
  LangChain's `UsageLimits` is per-run only — no tenant aggregation.

### Observability
- **Status:** Paid (LangSmith SaaS)
- **Pattern:** LangSmith for production tracing — Enterprise-gated pricing
- **AISOTROPY COMPARISON:** aisotropy ships OTel gen_ai semconv compliant tracing, `/costs` and
  `/health` endpoints by default — no SaaS dependency. LangSmith's sales-gating is a sovereignty
  concern for regulated deployments.

### Multi-tenant isolation
- **Status:** Convention (prefix-match)
- **Pattern:** `Store.search()` uses namespace prefix matching
- **AISOTROPY COMPARISON:** LangGraph's prefix-match is the weakest isolation of the three
  competitors. A namespace like `('acme',)` scans ALL tenants starting with `'acme'`.
  aisotropy's exact-key scoping + `Scope{}` value objects prevent this entirely.

## Summary

LangGraph has the **largest ecosystem** and the most mature checkpointing/story, but its
isolation model (prefix-match `Store.search()`) is the weakest of the three. Production
observability (LangSmith) is paid/SaaS-gated, which is a sovereignty concern.

**vs. aisotropy:** LangGraph wins on ecosystem and checkpointing maturity; aisotropy wins on
isolation-by-construction, cost governance, observability, and sovereignty. The trade is:
largest ecosystem with paid SaaS dependency vs. complete self-hostable runtime with constitutional
guarantees.
