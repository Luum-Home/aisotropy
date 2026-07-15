---
type: Feature matrix
title: "Pydantic AI — features vs aisotropy"
description: Coverage map of Pydantic AI v2 capabilities compared to aisotropy's built-ins.
tags: [arenas, pydantic-ai, features, comparison]
timestamp: 2026-07-15T00:00:00Z
---

# Pydantic AI — Features

## Built-in capabilities

### Structured output (FAIL-CLOSED)
- **Status:** Built-in
- **Mechanism:** Pydantic models + `TypeAdapter` for JSON schema generation
- **Retry:** `raise ModelRetry(message)` triggers automatic self-correction on next turn
- **Reflection loop:** Yes — `UnexpectedModelBehavior` catches schema violations and retries
- **AISOTROPY COMPARISON:** Equivalent to aisotropy's Gollem reflection loop. Both are
  fail-closed. Difference: aisotropy's is **default/runtime-wired** (no opt-in needed),
  Pydantic AI's is decorator-based (`@agent.tool` + `raise ModelRetry`).

### Tool calling
- **Status:** Built-in
- **Mechanism:** `@agent.tool` decorator with type-safe function signatures
- **DI integration:** `RunContext[deps]` gives first-class dependency injection into tools
- **AISOTROPY COMPARISON:** Equivalent to aisotropy's `WithTypedTool[T]`. Both are type-safe.
  Pydantic AI uses decorators; aisotropy uses builder pattern.

### Streaming
- **Status:** Built-in
- **Mechanism:** `AsyncAgent.stream(prompt)` returns async iterator over deltas
- **Cancellation:** Python `asyncio.CancelledError` propagates naturally
- **AISOTROPY COMPARISON:** Equivalent to aisotropy's Huma SSE with `ctx` cancellation.
  Both stream deltas; aisotropy adds HTTP-level `Accept: text/event-stream` semantics.

## DIY / missing capabilities

### Working memory
- **Status:** Not built-in
- **Pattern:** App-level dictionary, Redis, or custom state management
- **AISOTROPY COMPARISON:** aisotropy ships BadgerDB in-process working memory with µs latency,
  summary buffer, and transactional outbox — all wired by default.

### Semantic memory (vectors)
- **Status:** Not built-in
- **Pattern:** BYO Chroma, Pinecone, Weaviate, Qdrant, or any vector store
- **AISOTROPY COMPARISON:** aisotropy has in-process cosine brute-force (active) + Qdrant gRPC
  (CODE-ONLY, dormant behind `QDRANT_HOST`). Both require wiring for production vectors.

### Facts memory (journal)
- **Status:** Not built-in
- **Pattern:** No equivalent — would need SQLite/PostgreSQL + FTS5 manually wired
- **AISOTROPY COMPARISON:** aisotropy ships Engram-style SQLite+FTS5 facts memory out of the box.

### Cost governance
- **Status:** Partial — `UsageLimits` per-run
- **Pattern:** `UsageLimits(token_limit=...)` caps a single agent run
- **AISOTROPY COMPARISON:** aisotropy has **pre-spend quota gate** + **per-tenant cost meter**.
  `UsageLimits` is per-run only — no tenant aggregation, no pre-spend blocking.

### Observability
- **Status:** Not built-in
- **Pattern:** BYO OpenTelemetry — no native integration
- **AISOTROPY COMPARISON:** aisotropy ships OTel gen_ai semconv compliant tracing, `/costs` and
  `/health` endpoints, circuit-breaker metrics — all wired by default.

### Multi-tenant isolation
- **Status:** Convention only
- **Pattern:** Pass `tenant_id` through `RunContext[deps]` — developer responsibility
- **AISOTROPY COMPARISON:** aisotropy enforces isolation by construction — `context.Context`
  propagation + per-statement/key scoping + constitutional gate. No code path can issue an
  unscoped read.

## Summary

Pydantic AI v2 is the **best-typed DX** of the three frameworks — but it's fundamentally an
**agent orchestration library**, not a runtime. Every infrastructure concern (memory tiers,
cost governance, observability, multi-tenancy) is DIY. The structured output + retry path is
smooth, but that's a commodity feature now (collapsed as a differentiator per competitive-landscape).

Where aisotropy pays the full upfront cost to build **everything** as built-in, Pydantic AI
gives you the best experience for the **agent loop** and leaves the rest to you.
