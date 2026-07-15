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
- **Status:** Partial — per-call usage is built-in; aggregation, dollars, and the gate are DIY
- **Mechanism (RUN-VERIFIED, `scripts/cost_obs_demo.ts`, `npm run cost-obs`, exit 0):**
  - Per-call token usage: **[BUILT-IN]** — `agent.generate()` returns
    `result.usage = {inputTokens, outputTokens, totalTokens}` on every call (verified across 4 turns).
  - Per-tenant aggregation: **[DIY]** — the framework returns per-call usage only; the tenant
    ledger (`Map<tenantId, totals>`) is hand-rolled in the demo.
  - Dollar cost: **[DIY]** — Mastra returns tokens, not `$`; the pricing table + math are ours.
  - Pre-spend budget gate: **[DIY / NOT built-in]** — Mastra has **no** pre-spend/usage-limit
    check (no equivalent of Pydantic AI `UsageLimits.check_before_request`); usage is known only
    *after* the call. `grep` over `@mastra/core` for `usageLimit|checkBeforeRequest|budget` = 0 hits.
    The demo implements the gate as a ledger check *before* `generate()` and **proves the model is
    not invoked on a rejected turn** via a call-counter on the fake model (rejected turn Δ=0 calls,
    CONFIRMED).
- **AISOTROPY COMPARISON:** aisotropy has a **built-in** pre-spend quota gate + per-tenant cost
  meter — rejects or degrades *before* the LLM call. Mastra gives you the raw usage number and
  leaves the tenant ledger, the dollar conversion, and the reject-before-spend gate to you.

### Observability
- **Status:** Built-in (native Mastra AI tracing) — tenant tagging is DIY; hosted UI is PAID
- **Mechanism (RUN-VERIFIED, `scripts/cost_obs_demo.ts`, exit 0):**
  - Native tracing/spans: **[BUILT-IN]** — `@mastra/observability` `Observability({...})` on
    `new Mastra()` emits `agent_run`, `model_generation`, `model_step`, `model_inference`,
    `model_chunk` spans automatically (25 spans / 5 traces captured in one run).
  - In-memory span capture: **[BUILT-IN]** — `TestExporter` collects + queries spans fully
    offline. A real OTLP/console exporter (`OtelExporter`/`ConsoleExporter`) is a one-line swap.
  - gen_ai usage on spans: **[BUILT-IN]** — the `model_generation` span carries
    `usage.inputTokens/outputTokens` (GenAI semconv), verified in the captured span.
  - tenant_id on spans: **[DIY]** — the metadata mechanism is built-in, but you must pass
    `tracingOptions.metadata: { tenantId }` yourself; it is not auto-derived (25/25 spans carried
    `metadata.tenantId` only because the demo stamps it).
  - Hosted trace UI: **[PAID]** — Mastra Cloud / Langfuse / Braintrust exporters are optional paid
    backends; not required for the offline in-memory path above.
- **AISOTROPY COMPARISON:** aisotropy ships OTel gen_ai-semconv tracing plus `/costs` and `/health`
  endpoints by default, and stamps tenant onto spans from the `context.Context` boundary (no
  per-call opt-in). Mastra's tracing is genuinely built-in and semconv-aligned, but tenant tagging
  is a per-call responsibility, and the cost/health surface is DIY.

### Multi-tenant isolation
- **Status:** Convention (closest of the three)
- **Pattern:** `resourceId`/`threadId` with ownership validation
- **AISOTROPY COMPARISON:** Mastra's scoping is server-authoritative (harder to get wrong than
  Pydantic AI or LangGraph), but still convention-based. Bug mastra#11856 (`listMessages`
  ignoring `resourceId`) proves the leak is real. aisotropy's `Scope{}` value object with
  unexported fields makes leaks unrepresentable.

## Summary

Mastra is the **most batteries-included** of the three — memory, vectors, tools, streaming,
structured output, and (run-verified) **native OTel-semconv observability** all built-in. But it's
still a **framework**, not a runtime. Facts memory and cost governance are DIY; observability is
built-in but tenant tagging is a per-call opt-in and the `/costs`/`/health` surface is DIY. The
isolation model (`resourceId`/`threadId`) is
the closest any competitor comes to construction, but mastra#11856 proves it's still convention.

**vs. aisotropy:** Mastra wins on TS DX and built-in memory tiers; aisotropy wins on isolation-by-
construction, facts memory, cost governance, and sovereignty. The trade is: best TS experience
with partial built-ins vs. complete Go runtime with everything wired.
