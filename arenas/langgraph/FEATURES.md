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

### Cost governance (#8) — RUN-VERIFIED via `cost_obs_demo.py`
- **Per-call cost visibility:** **Built-in.** Every `AIMessage` carries
  `.usage_metadata` (`input_tokens` / `output_tokens` / `total_tokens`). Verified: read
  straight off the reply after each `GRAPH.invoke`.
- **Aggregation:** **Built-in helpers exist** (`UsageMetadataCallbackHandler`,
  `get_usage_metadata_callback()`) but they sum usage **keyed by MODEL NAME — no tenant
  concept.** Per-tenant attribution/totals is **DIY** (keep your own ledger; demo does).
- **Pre-spend budget gate:** **DIY — not provided.** Correction to the earlier note:
  LangChain 1.x has **no `UsageLimits`** and **no `check_before_request` equivalent** (that
  is a *Pydantic AI* API, not LangChain). The closest built-ins are
  `langchain.agents.middleware.ModelCallLimitMiddleware` (limits the *number of model
  CALLS* per run/thread, per-agent, reactive — not a token budget) and the reactive
  usage callbacks. Verified against langchain 1.3.13 / langchain-core 1.4.9: no pre-spend
  token gate. The demo implements the per-tenant token gate DIY and **proves the model is
  not reached on a rejected turn** (call-counter delta = 0).
- **AISOTROPY COMPARISON:** aisotropy ships a **pre-spend quota gate (token-bucket)** +
  **per-tenant cost meter** as runtime defaults. LangGraph gives per-call token visibility
  for free but leaves per-tenant aggregation and the pre-spend gate to the developer.

### Observability (#10) — RUN-VERIFIED via `cost_obs_demo.py`
- **Native OTel spans:** **Built-in — and they DO reach your own OTel exporter offline.**
  Correction to the earlier "Paid only" note: with `LANGSMITH_OTEL_ENABLED=true` +
  `LANGSMITH_OTEL_ONLY=true` and a global `TracerProvider` installed *before* importing
  LangChain, LangGraph emits **gen_ai-semconv spans** into an in-memory exporter with **no
  cloud call and no real API key**. Verified: 3 spans captured (`chat-turn` chain, `chat`
  node, `CountingFakeModel` LLM) carrying `gen_ai.system=langchain`,
  `gen_ai.operation.name`, and `gen_ai.usage.{input,output,total}_tokens` on the model span.
- **Important nuance:** the OTel emission **bridge ships inside the `langsmith` SDK package**
  (the `langsmith` dependency, not a standalone LangChain OTel module). It is free to run and
  can point at your own collector — but the machinery is LangSmith's.
- **`tenant_id` on spans:** **DIY.** Not automatic — you inject it into run
  `metadata`/`tags`; it then surfaces as `langsmith.metadata.tenant_id` (verified on all 3
  spans). LangGraph has no tenant concept to put there for you.
- **Managed dashboard:** **Paid.** LangSmith cloud is the native managed backend/UI for these
  traces (SaaS, key-gated) — the sales-gated path.
- **AISOTROPY COMPARISON:** aisotropy ships OTel gen_ai-semconv tracing **with tenant scope by
  default**, plus `/costs` and `/health` endpoints — no SaaS dependency and no per-call
  wiring. LangGraph reaches the same span quality offline, but the tracer lives in the
  LangSmith SDK and tenant scoping is developer-owned; the managed path is sales-gated
  (a sovereignty concern for regulated deployments).

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
