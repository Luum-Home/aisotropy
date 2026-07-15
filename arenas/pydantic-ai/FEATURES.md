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
- **Status:** Partial — REAL per-run pre-spend gate; per-tenant is DIY
- **RUN-VERIFIED** (`scripts/cost_obs_demo.py`, pydantic-ai 2.10.0, offline `TestModel`):
  - **Per-call usage visibility — [BUILT-IN].** `result.usage` returns a `RunUsage`
    (`input_tokens`/`output_tokens`/`total_tokens`). Note: in v2.x `usage` is a **property**,
    not the older `result.usage()` method call.
  - **Pre-spend budget gate — [BUILT-IN], and it genuinely fires *before* the model.**
    `UsageLimits(input_tokens_limit=N, count_tokens_before_request=True)` makes the framework
    call `model.count_tokens()` (cheap) then `check_before_request()` raises
    `UsageLimitExceeded` **before** `model.request()`. The demo proves it with a call-counter:
    on the rejected turn `generation calls == 0` (only the pre-count ran). Verified message:
    `The next request would exceed the input_tokens_limit of 2 (input_tokens=14)`.
    (`count_tokens_before_request` needs a model implementing `count_tokens()` — Google/Bedrock
    in prod; our offline fake implements it. `TestModel`/`FunctionModel` do NOT.)
  - **Per-tenant aggregation & per-tenant budget — [DIY].** `UsageLimits` is scoped to a single
    **run**, not a tenant. The demo sums `result.usage` into a `TenantLedger` and derives each
    turn's limit from `budget - already_spent[tenant]` — all app code. The framework has no
    tenant concept.
- **AISOTROPY COMPARISON:** aisotropy wires the **pre-spend quota gate + per-tenant cost meter**
  by default (token-bucket in BadgerDB, `/costs` endpoint). Pydantic AI gives you the pre-spend
  *primitive* (a real, pre-generation raise) but leaves per-tenant accounting to you.

### Observability
- **Status:** Built-in native OTel tracing (opt-in one-liner); hosted dashboard is paid; tenant tagging is DIY
- **RUN-VERIFIED** (`scripts/cost_obs_demo.py`, logfire 4.37.0 + opentelemetry-sdk, offline):
  - **Native OTel gen_ai tracing — [BUILT-IN].** `logfire.configure(send_to_logfire=False)` +
    `logfire.instrument_pydantic_ai()` emits standard OTel spans captured here with an
    `InMemorySpanExporter`. Observed spans: `invoke_agent <agent>` and `chat <model>` carrying
    `gen_ai.operation.name`, `gen_ai.request.model`, `gen_ai.usage.input_tokens` /
    `output_tokens`, `gen_ai.aggregated_usage.*`, plus input/output messages and
    `gen_ai.conversation.id`. Runs fully offline — no data leaves the process.
  - **Hosted trace dashboard — [PAID].** Sending those same spans to the **Logfire SaaS**
    dashboard is the paid path (same story as LangSmith). The OTel export itself is OSS/free —
    you can point it at your own collector.
  - **`tenant_id` on spans — [DIY].** The framework's spans do **not** carry `tenant_id`
    (verified: `tenant_id on framework spans == False`). It appears only on the wrapping
    `logfire.span("tenant.turn", tenant_id=...)` we open ourselves.
- **AISOTROPY COMPARISON:** aisotropy ships OTel gen_ai semconv tracing plus `/costs`, `/health`,
  and circuit-breaker metrics wired by default, with tenant identity propagated via
  `context.Context`. Pydantic AI's tracing is comparable in span quality but is opt-in and
  tenant-unaware — the tenant dimension is your responsibility.

### Multi-tenant isolation
- **Status:** Convention only
- **Pattern:** Pass `tenant_id` through `RunContext[deps]` — developer responsibility
- **AISOTROPY COMPARISON:** aisotropy enforces isolation by construction — `context.Context`
  propagation + per-statement/key scoping + constitutional gate. No code path can issue an
  unscoped read.

## Summary

Pydantic AI v2 is the **best-typed DX** of the three frameworks — but it's fundamentally an
**agent orchestration library**, not a runtime. Most infrastructure concerns (memory tiers,
multi-tenancy) are DIY. Two are more nuanced than "DIY", and the run-verified demo shows it:
**cost** ships a *real pre-spend gate* (`UsageLimits.check_before_request` +
`count_tokens_before_request` raise `UsageLimitExceeded` before the model call — proven with a
call-counter), and **observability** ships *native OTel gen_ai tracing* one `instrument_pydantic_ai()`
call away. What's still DIY is the **per-tenant dimension** on both: per-tenant cost aggregation,
per-tenant budgets, and `tenant_id` on spans are all your code — the framework has no tenant concept.
The structured output + retry path is smooth, but that's a commodity feature now (collapsed as a
differentiator per competitive-landscape).

Where aisotropy pays the full upfront cost to build **everything** as built-in, Pydantic AI
gives you the best experience for the **agent loop** and leaves the rest to you.
