---
type: Concept
title: Runtime as built — what aisotropyd actually wires and runs
description: A code-grounded description of the aisotropyd runtime as it is actually assembled in cmd/aisotropyd/main.go — the HTTP surface, the streaming request lifecycle, the async outbox/sleep-cycle side, the memory tiers, the Phase 7 granular-control machinery, and an honest ledger of what is not yet wired.
tags: [architecture, as-built, runtime, request-lifecycle, outbox, quota, circuit-breaker, cost-meter, phase-7]
timestamp: 2026-07-09T00:00:00Z
---

# Runtime as built — what aisotropyd actually wires and runs

This document describes the aisotropyd runtime **as the code assembles it today**, verified by
reading the source rather than the design docs. Where the running code and the aspirational
architecture docs disagree, this document follows the **code** and flags the gap. Its companions
describe the *intended* design: [overview](overview.md), [request lifecycle](request-lifecycle.md),
[tiered memory](memory-tiers.md), [consistency](consistency.md), [multi-tenancy](multi-tenancy.md),
[granular control](granular-control.md), [observability](observability.md). This is the honest
"here is what boots" counterpart.

Everything below is anchored in [`cmd/aisotropyd/main.go`](../../cmd/aisotropyd/main.go), which is
the single wiring point: it constructs every tier and decorator and mounts every route. There is no
other composition root.

## 1. The HTTP surface (every route actually registered)

The binary builds a `chi` router (`middleware.RequestID`, `middleware.Recoverer`), mounts a Huma v2
API on it via the `humachi` adapter (`huma.DefaultConfig("Aisotropy", "0.1.0")`), and registers the
following — nothing else:

| Route | Where registered | In OpenAPI contract? | What it does |
|---|---|---|---|
| `GET /healthz` | `main.go` (raw chi handler) | No | Liveness only. Writes `{"status":"ok"}` with HTTP 200. No dependency checks. |
| `GET /costs` | `main.go` (raw chi handler) | No | JSON-encodes `costMeter.Snapshot()` — the per-tenant token/cost aggregate (see [`internal/costmeter`](../../internal/costmeter/costmeter.go)). Sorted by tenant id. |
| `GET /openapi.json`, `/openapi.yaml`, `GET /docs` | Huma `DefaultConfig` | n/a (they *are* the contract) | Auto-generated OpenAPI 3.1 schema + Stoplight docs UI. The Go struct is the single source of truth. |
| `POST /v1/tenant/{tenant_id}/user/{user_id}/chat` | `api.Register` ([`chat.go`](../../internal/api/chat.go)) | Yes (`operationID: chat`) | Non-streaming, structured-output chat. Runs the reflection loop and **fails closed** with 502 on a persistent schema violation. |
| `POST /v1/tenant/{tenant_id}/user/{user_id}/chat/stream` | `api.RegisterStream` ([`chat_stream.go`](../../internal/api/chat_stream.go)) | Yes (`operationID: chat-stream`) | SSE streaming chat, token by token. The full memory + outbox + cost path. Emits `token` / `done` / `error` events. |
| `POST /v1/tenant/{tenant_id}/user/{user_id}/chat/agent` | `api.RegisterAgent` ([`chat_agent.go`](../../internal/api/chat_agent.go)) | Yes (`operationID: chat-agent`) | Bounded structured-output tool loop over scope-permitted tools. Returns the ordered list of tool decisions + results. |

`tenant_id` and `user_id` are **path params** on all three `/v1/...` routes, charset-constrained at
the contract level (`pattern:"^[a-zA-Z0-9_-]{1,64}$"`, Huma rejects with 422 before the handler
runs). `session_id` and `scopes` travel in the request body (`ChatRequest`): a session id carries no
authorization meaning, and scopes are the JWT-derived permissions stand-in.

### Contract shapes

- **`ChatRequest`** (body): `prompt` (maxLength 2000), `product_id`, `session_id`, `scopes[]`.
- **`ChatOutput`** (non-streaming): `reply`, `tenant_id` (isolation echo), `stub` (true when the
  resolved LLM client is the credential-less stub).
- **SSE events** (streaming): `token{text}`, `done{tenant_id}`, `error{message}`.
- **`AgentOutput`**: `tenant_id`, `steps[]` where each step is `{tool, args, result}`.

## 2. The streaming request lifecycle, step by step as coded

This is the `chat/stream` handler in [`chat_stream.go`](../../internal/api/chat_stream.go),
in exact execution order:

1. **Identity validation.** `withIdentity(ctx, tenant, user, session)` validates all three
   components through `memory.Key.Validate()` (non-empty, none of the reserved chars `:`, `\x00`,
   `\n` — see [`internal/memory/key.go`](../../internal/memory/key.go)) and injects tenant/user into
   `ctx`. It then also calls `llm.WithTenant` / `llm.WithUser` so the OTel decorator can attribute
   spans without `internal/llm` importing `internal/api`. A `facts.Scope` is built via
   `facts.NewScope(tenant, user)`, which routes identity through
   [`internal/tenancy`](../../internal/tenancy/scope.go) (ADR-0011). On any failure the handler
   emits an SSE `error` event and returns.
2. **Rate-limit gate (429-equivalent).** If a `*memory.Limiter` is present, `limiter.Allow(ctx,
   tenant)` takes one token from the per-tenant bucket **before any LLM call**. A denied request
   emits an SSE `error` ("rate limit exceeded…") and returns — no tokens spent. (See
   [`internal/memory/quota.go`](../../internal/memory/quota.go).)
3. **System-prompt hydration.** `hydrateSystemPrompt` assembles the system prompt *sequentially*:
   - base prompt from `product_id`;
   - working-memory `Summary(ctx, key)` (condensed buffer) and `Recent(ctx, key, 12)` (last 12
     active messages);
   - facts-memory `store.Search(ctx, scope, prompt, 4)` — FTS5 top-4 engrams ranked
     Relevance × Importance × Recency. **A facts failure is non-fatal** (logged, proceeds without
     facts); a working-memory failure *is* fatal (SSE error).
4. **Scope-gated tool fragment.** If a `*tools.Registry` is present, `PromptFragment(scopes)` appends
   only the tools the caller's scopes permit. A tool the scopes do not grant is never mentioned —
   governance by absence of information ([`internal/tools/tools.go`](../../internal/tools/tools.go)).
5. **`AppendWithOutbox` (atomic message + consolidation intent).** The user turn and a pending
   consolidation event are written in **one BadgerDB transaction**
   ([`internal/memory/outbox.go`](../../internal/memory/outbox.go)). The event id is a stable
   sha256-derived `outboxRef(key, prompt)` so re-sending the identical prompt consolidates onto the
   same fact/vector. This happens *before* the LLM call, because the user's message already
   happened.
6. **Quota-pressure model degradation.** If a cheap client *and* a limiter are both configured,
   `limiter.Pressure(ctx, tenant)` peeks the bucket and `llm.DegradeAt(pressure, 0.95, primary,
   cheap)` decides whether to route this turn to the cheaper model instead of a hard rejection
   ([`internal/llm/degrade.go`](../../internal/llm/degrade.go),
   [`internal/memory/quota_pressure.go`](../../internal/memory/quota_pressure.go)). Absent a cheap
   client, the primary is always used.
7. **LLM stream (circuit-breakered + OTel).** `active.StreamChat(ctx, system, prompt)` opens a token
   channel. The client is `WithOTel(WithCircuitBreaker(primary))` — one `gen_ai` span per call, and
   fast-fail once the breaker is open (see steps below).
8. **Drain loop.** For each token:
   - `ctx.Done()` (client disconnect / expiry) → stop immediately; the partial reply is **not**
     persisted. Token spend is cut by the producer observing the same `ctx.Done()`.
   - `tok.Err != nil` → emit SSE `error`, stop; partial reply not persisted.
   - `tok.Done` → **persist the assistant turn** via `wm.Append` (only a clean, complete stream is
     stored), **record cost** via `meter.Record` from `tok.Usage` (real provider token counts, if
     supplied), then emit `done`.
   - otherwise → accumulate text and forward the `token` event.

The non-streaming `chat` handler ([`chat.go`](../../internal/api/chat.go)) is similar but:
- it uses the reflection loop `llm.ReflectAuto[chatStructuredReply]` (Gollem backend by default with
  a homegrown `Reflect[T]` fallback, ADR-0008) and **fails closed with HTTP 502** if no valid
  structured reply is produced after `chatMaxReflectionRetries = 3` attempts;
- it persists via plain `wm.Append` (user turn before the call, assistant turn only on success) —
  **it does not enqueue an outbox event**, so non-streaming turns never reach the facts/semantic
  consolidation pipeline;
- it uses the breaker-wrapped client **without** the OTel decorator (per ADR-0010 the reflection
  path is meant to use Gollem's own `ext/otel`, not yet adopted).

## 3. The async / offline side

### Outbox worker (at-least-once drain)

At boot, `main.go` builds a `consolidation.Sink` ([`internal/consolidation/sink.go`](../../internal/consolidation/sink.go))
over the facts store + `HeuristicExtractor` and the semantic store + `HashEmbedder`, then:

1. **Cold-boot reconciliation:** `wm.RecoverOutbox(ctx, sink)` drains every event orphaned by a
   previous shutdown *before* the server starts serving — same drain path, no special recovery mode.
2. **Steady state:** `go wm.RunOutboxWorker(workerCtx, sink, 1s)` drains pending events back-to-back
   when present, polling every second when empty, backing off exponentially (200ms → 30s) on store
   errors ([`internal/memory/outbox.go`](../../internal/memory/outbox.go)).

For each drained event `Sink.Consolidate` runs the two sinks **sequentially** (facts, then
semantic): the `Extractor` distils the event's `Content` (the user prompt) into facts and
`Facts.Save` upserts them by content-derived id; the `Embedder` embeds the content and
`Semantic.Upsert` writes a point keyed on a deterministic UUIDv5 (`semantic.PointID`). An event key
is deleted **only after both sinks succeed** — at-least-once, idempotent on redelivery. Both nil
tiers are skipped (facts-only or semantic-only deployments are allowed).

### Sleep-cycle worker (DataFusion, Rust subprocess)

Only wired when `AISOTROPY_SLEEPCYCLE_BIN` is set. `main.go` builds a `sleepcycle.Coordinator`
(facts store + `sleepcycle.Worker{BinaryPath, Tracer}`) and runs `RunScheduler(workerCtx, 1h, …)`.
Each pass ([`internal/sleepcycle/coordinator.go`](../../internal/sleepcycle/coordinator.go)):
`ExportAll` (a reviewed cross-tenant read) → write NDJSON to a scratch dir → invoke the Rust binary
as a subprocess (`--input`/`--output`, ADR-0005) → parse the JSON `Summary` from stdout → `Delete`
each dropped duplicate id **within its own (tenant,user) scope**. A Rust panic surfaces as a
non-zero exit, retried with backoff up to 3 attempts; a failure is logged, never fatal, and never on
the request path. The Rust worker now **exists** at [`crates/sleep-cycle`](../../crates/sleep-cycle)
(DataFusion dedup over exported facts, ADR-0005), but this path is still **dormant by default**: it
runs only when the binary is built and `AISOTROPY_SLEEPCYCLE_BIN` points at it.

## 4. The memory tiers as built

| Tier | Package | Default (zero-config) | Networked / production | Filled by | Read by |
|---|---|---|---|---|---|
| **Working** (L1) | [`internal/memory`](../../internal/memory/working_memory.go) | In-memory BadgerDB (no persistence) | On-disk BadgerDB at `AISOTROPY_DATA_DIR` | `Append` / `AppendWithOutbox` on every chat turn | `Summary` + `Recent` during hydration; also holds the quota buckets and the outbox queue |
| **Facts** | [`internal/facts`](../../internal/facts/facts.go) | Ephemeral SQLite `:memory:` (FTS5) | File SQLite (`AISOTROPY_FACTS_DB` or `AISOTROPY_DATA_DIR/facts.db`) or **rqlite** (`RQLITE_URL`, ADR-0004) | Outbox worker → `Sink` → `HeuristicExtractor` → `Save` (UPSERT) | `Search` (FTS5 top-4) during hydration; `ExportAll`/`Delete` by the sleep cycle |
| **Semantic** | [`internal/semantic`](../../internal/semantic/semantic.go) | In-memory brute-force cosine index | **Qdrant** (`QDRANT_HOST` + `QDRANT_PORT`/`QDRANT_API_KEY`/`QDRANT_USE_TLS`/`QDRANT_COLLECTION`, ADR-0001) | Outbox worker → `Sink` → `HashEmbedder` → `Upsert` (UUIDv5) | **Not read on the request path today** — written only (see §6) |

The embedder is the deterministic `HashEmbedder` placeholder and the working-memory summarizer is
`LLMSummarizer` (real LLM condensation via the same chat client, or `NoopSummarizer` in tests).
Every operation on every tier is scoped by `(tenant, user[, session])` — the multi-tenant isolation
gate lives in each key/statement, never in an intermediate layer.

## 5. Phase 7 granular control as built

All five knobs are constructed in `main.go` and threaded through the handlers:

- **Rate limiter** — `wm.NewLimiter(AISOTROPY_RATE_PER_SEC=10, AISOTROPY_BURST=30)`: a per-tenant
  token bucket persisted in the working-memory BadgerDB, serialized per tenant by an in-process
  mutex (BadgerDB runs with `DetectConflicts=false`). Checked on `chat` and `chat/stream` **before**
  the LLM call. (The `chat/agent` route does **not** rate-limit — no limiter is passed to
  `RegisterAgent`.)
- **Circuit breaker** — `llm.WithCircuitBreaker(client, 5, 30s)`: 5 consecutive `StreamChat` start
  failures open the breaker; calls then fail fast with `ErrCircuitOpen` for a 30s cooldown, then a
  half-open trial. Wraps both the primary and the cheap client. Shared by streaming, non-streaming,
  and agent paths.
- **Scope-gated tools** — `tools.NewRegistry(search_docs [no scope], read_financials
  [read:financial-data], write_tasks [write:tasks])`. Only scope-permitted tools enter the prompt;
  the agent loop re-checks the model's chosen tool name against the permitted set post-parse, so a
  hallucinated/injected tool name can never execute
  ([`internal/agentloop/agentloop.go`](../../internal/agentloop/agentloop.go)).
- **Model degradation** — `llm.CheapClientFromEnv()` (built from `AISOTROPY_CHEAP_MODEL`) wrapped in
  the same breaker + OTel. Consumed only by the streaming path at pressure ≥ 0.95. `nil` (no
  degradation) when the env var is unset.
- **Cost meter** — `costmeter.New()`: an in-process, per-tenant aggregate of real provider token
  usage (input/output tokens, request count, estimated USD at $0.15/$0.60 per 1M in/out). Recorded
  on each streamed `done`, exposed at `GET /costs`. Per-process and non-durable — a live
  observability aggregate, not a billing system of record.

## 6. Honest "not yet wired / limitations"

- **Semantic (Qdrant) RAG on the streaming path: now read AND written.** The consolidation sink
  writes vectors, and `hydrateSystemPrompt` now embeds the prompt and runs a scoped semantic
  search (top `semanticWindow`), injecting the results — closing the earlier write-only gap. It
  is wired on the **streaming** path only (the non-streaming `chat` passes a nil semantic store).
  Hydration is still **sequential** (working memory → facts → semantic), not the goroutine-parallel
  hydration the aspirational [request lifecycle](request-lifecycle.md) describes.
- **Both chat routes consolidate; only `chat/agent` does not.** `chat/stream` and `chat` both call
  `AppendWithOutbox` (the non-streaming route since commit `85caf46`), so both feed the outbox →
  facts/semantic pipeline. `chat/agent` is the only route that neither hydrates nor consolidates
  memory. (Historical note: non-streaming `chat` previously used plain `wm.Append` and never
  consolidated — that gap is now closed.)
- **The routing seam is not adopted.** [`internal/routing`](../../internal/routing/router.go)
  (ADR-0007 Shared/Dedicated tenant tiering) exists but **nothing outside the package imports it** —
  the facts and semantic stores still use fixed resource names. It is a designed seam awaiting
  adoption.
- **Primitive self-improvement is fully un-wired.** [`internal/primitives`](../../internal/primitives/primitives.go)
  (friction-driven per-user rewriting) is referenced only by a comment in `main.go`; no friction
  source feeds it and the sleep cycle never calls `primitives.Consolidate`. The sleep cycle only
  does **facts dedup**, not primitive rewriting — contrary to the package's own docstring.
- **Model degradation needs `AISOTROPY_CHEAP_MODEL`.** Without it `CheapClientFromEnv()` returns
  `nil` and every near-quota tenant gets the primary model (the degradation branch is skipped).
- **The agent loop is structured-output, not native function-calling.** `internal/agentloop` drives
  "tool use" by asking the model for a JSON `Decision` over the plain `StreamChat` interface — there
  is no native Gollem/provider function-calling. The wired `Executor` is `demoToolExecutor`, which
  only echoes the invocation; real tool implementations replace it. The agent route also does not
  hydrate memory, persist turns, rate-limit, or meter cost.
- **Rate limiting and the cost meter are per-instance.** Both live in-process; there is no
  cross-replica shared quota or accounting. A multi-replica deployment would need to move this state
  to a networked tier.
- **Reflection/fail-closed applies only to non-streaming chat.** The `chat/stream` path forwards raw
  tokens with no schema enforcement; the structured-output reflection loop and its 502 fail-closed
  behavior are the `chat` route's alone.
- **Stale wiring note in `working_memory.go`.** Its type doc claims `ChatInput` "currently carries
  only `TenantID`" and that wiring memory into chat needs a contract change first — but the contract
  already carries `tenant_id`/`user_id` path params + `session_id` body, and the streaming path is
  fully wired. The comment predates the current wiring.

## Environment variables (as consumed by the code)

| Variable | Consumed in | Effect |
|---|---|---|
| `AISOTROPY_ADDR` | `main.go` | HTTP listen address (default `:8080`) |
| `ANTHROPIC_API_KEY` | `llm.FromEnv` | Selects the real Anthropic client; unset → canned stub |
| `AISOTROPY_DATA_DIR` | working memory + facts | On-disk BadgerDB dir; also default `facts.db` location |
| `AISOTROPY_FACTS_DB` | `facts.OpenFromEnv` | Explicit SQLite file path |
| `RQLITE_URL` | `facts.OpenFromEnv` | Networked rqlite facts store |
| `QDRANT_HOST` (+ `QDRANT_PORT`/`QDRANT_API_KEY`/`QDRANT_USE_TLS`/`QDRANT_COLLECTION`) | `semantic.OpenFromEnv` | Qdrant semantic store |
| `AISOTROPY_CHEAP_MODEL` | `llm.CheapClientFromEnv` | Enables quota-pressure degradation |
| `AISOTROPY_SLEEPCYCLE_BIN` | `main.go` | Enables the DataFusion sleep-cycle worker |
| `AISOTROPY_RATE_PER_SEC` / `AISOTROPY_BURST` | `wm.NewLimiter` | Token-bucket rate + burst (defaults 10 / 30) |
| `AISOTROPY_REFLECTION_BACKEND` | `llm.ReflectionBackendFromEnv` | `own` forces the homegrown loop; anything else → Gollem |
| `OTEL_EXPORTER_OTLP_ENDPOINT` / `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | `llm.NewTracerProvider` | Enables OTLP/HTTP trace export; unset → no-op tracer |

## References

- Wiring: [`cmd/aisotropyd/main.go`](../../cmd/aisotropyd/main.go)
- Handlers: [`internal/api/chat.go`](../../internal/api/chat.go),
  [`chat_stream.go`](../../internal/api/chat_stream.go),
  [`chat_agent.go`](../../internal/api/chat_agent.go)
- Design counterparts: [overview](overview.md) · [request lifecycle](request-lifecycle.md) ·
  [tiered memory](memory-tiers.md) · [consistency](consistency.md) ·
  [multi-tenancy](multi-tenancy.md) · [granular control](granular-control.md) ·
  [observability](observability.md)
