---
type: Feature matrix
title: "Mastra — features vs aisotropy"
description: Coverage map of Mastra capabilities compared to aisotropy's built-ins.
tags: [arenas, mastra, features, comparison]
timestamp: 2026-07-15T00:00:00Z
---

# Mastra — Features

## Built-in capabilities

### Structured output (FAIL-CLOSED)
- **Status:** Built-in
- **Mechanism:** AI-SDK v5 native structured output with Zod schemas
- **Retry:** Automatic self-correction via AI-SDK's `output` parameter
- **AISOTROPY COMPARISON:** Equivalent to aisotropy's Gollem reflection loop. Both are fail-closed.
  Mastra's is AI-SDK-native; aisotropy's is homegrown but equally effective.

### Tool calling
- **Status:** Built-in
- **Mechanism:** Native tool definitions with TypeScript type inference
- **Integration:** First-class in the agent constructor
- **AISOTROPY COMPARISON:** Equivalent to aisotropy's `WithTypedTool[T]`. Both are type-safe.
  Mastra uses TS decorators; aisotropy uses Go generics.

### Streaming
- **Status:** Built-in
- **Mechanism:** AI-SDK streaming with SSE support
- **Cancellation:** Node.js `AbortController` propagates to LLM
- **AISOTROPY COMPARISON:** Equivalent to aisotropy's Huma SSE + `ctx` cancellation. Both stream
  deltas; Mastra adds AI-SDK v5's richer streaming primitives.

### Working memory
- **Status:** Built-in
- **Mechanism:** `MemoryStorage` with `resourceId`/`threadId` scoping
- **Features:** Message history, memory processors (trimming before context window)
- **AISOTROPY COMPARISON:** Mastra's is thread-scoped with memory processors; aisotropy's is
  temperature-tiered (BadgerDB → SQLite+FTS5 → Qdrant → DataFusion). Both are built-in,
  but aisotropy's hierarchy is deeper.

### Semantic memory (vectors)
- **Status:** Built-in
- **Mechanism:** Vector recall with embedding integration
- **Features:** Automatic embedding, similarity search, memory processors
- **AISOTROPY COMPARISON:** Both have built-in vector memory. Mastra uses AI-SDK embeddings;
  aisotropy uses in-process cosine brute-force (active) + Qdrant gRPC (dormant).

## DIY / missing capabilities

### Facts memory (journal)
- **Status:** Not built-in
- **Pattern:** No equivalent — would need PostgreSQL/SQLite with FTS5 manually wired
- **AISOTROPY COMPARISON:** aisotropy ships Engram-style SQLite+FTS5 facts journal out of the box.

### Cost governance
- **Status:** Not built-in
- **Pattern:** BYO token counting / budget management
- **AISOTROPY COMPARISON:** aisotropy has pre-spend quota gate + per-tenant cost meter — rejects
  or degrades before the LLM call.

### Observability
- **Status:** Not built-in
- **Pattern:** BYO OpenTelemetry — no native integration
- **AISOTROPY COMPARISON:** aisotropy ships OTel gen_ai semconv compliant tracing, `/costs` and
  `/health` endpoints by default.

### Multi-tenant isolation
- **Status:** Convention (closest of the three)
- **Pattern:** `resourceId`/`threadId` with ownership validation
- **AISOTROPY COMPARISON:** Mastra's scoping is server-authoritative (harder to get wrong than
  Pydantic AI or LangGraph), but still convention-based. Bug mastra#11856 (`listMessages`
  ignoring `resourceId`) proves the leak is real. aisotropy's `Scope{}` value object with
  unexported fields makes leaks unrepresentable.

## Summary

Mastra is the **most batteries-included** of the three — memory, vectors, tools, streaming,
structured output all built-in. But it's still a **framework**, not a runtime. Facts memory,
cost governance, and observability are DIY. The isolation model (`resourceId`/`threadId`) is
the closest any competitor comes to construction, but mastra#11856 proves it's still convention.

**vs. aisotropy:** Mastra wins on TS DX and built-in memory tiers; aisotropy wins on isolation-by-
construction, facts memory, cost governance, and sovereignty. The trade is: best TS experience
with partial built-ins vs. complete Go runtime with everything wired.
