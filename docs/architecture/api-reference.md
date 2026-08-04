---
type: Reference
title: API reference — every endpoint the runtime serves
description: The first-class endpoint reference for aisotropyd — method, path, request/response/error contract, and streaming/SSE semantics for each route, plus which routes are inside the auto-generated OpenAPI 3.1 contract and which are operational side-channels. The payload shapes live in the data model; this documents the routes that carry them.
tags: [architecture, reference, api, endpoints, openapi, sse, http]
timestamp: 2026-07-14T00:00:00Z
---

# API reference — every endpoint the runtime serves

The OpenAPI 3.1 schema is **auto-generated from the Go structs** and served live at `/openapi.json`
— it is the machine source of truth. This node is the human-facing companion: it lists every route,
its contract, and its error/streaming semantics. Payload shapes are in the [data model](data-model.md);
which tiers back each route (real vs dormant) is in the [reality ledger](reality-ledger.md).

All `/v1/...` routes take `tenant_id` and `user_id` as **path params** (`^[a-zA-Z0-9_-]{1,64}$`,
Huma rejects with **422** before the handler runs), and `session_id` / `scopes` in the body.
Identity **is authenticated in-process** (ADR-0024/0025): a fail-closed Huma middleware runs in front
of every `/v1` route — a configured `Authenticator` with a must-agree bind (verified principal ==
path `tenant_id`/`user_id`; 401 on missing/invalid credential, 403 on mismatch), or a **default-deny**
gate (401 on every `/v1` request) when none is registered. The shipped `aisotropyd` binary opts into
`NoAuth()` (path identity trusted, loudly warned) unless `AISOTROPY_JWT_SECRET` (HS256) or an RS256/JWKS
mode is set. See the [threat model](threat-model.md).

## Operational endpoints (outside the OpenAPI contract)

| Method · Path | Purpose | Response |
|---|---|---|
| `GET /healthz` | Liveness only | `200 {"status":"ok"}` — no dependency checks |
| `GET /health` | Readiness | Parallel, read-only, `_health`-scoped probes of the facts + semantic tiers (no LLM spend). `200 {"status":"ok",checks:[…]}` when all respond, **`503 degraded`** when any fails |
| `GET /costs` | Per-tenant cost view | JSON array of `costMeter.Snapshot()` — per-tenant token totals + estimated USD, sorted by tenant. In-process, non-durable (not a billing system of record) — but the same meter drives the cost-budget model shaping, so "non-durable" has teeth beyond this view; see below |
| `GET /bitrate` | Per-tenant bits-per-token ops view (Bet 6) | JSON array of per-tenant bpt summaries. **Conditionally registered**: only when `AISOTROPY_BITRATE_BIN` is set; otherwise the route does not exist (404) |
| `GET /openapi.json` · `/openapi.yaml` · `GET /docs` | The contract itself + Stoplight docs UI | auto-generated |

### Operational response schemas (as built, 2026-07-14)

These four routes are plain `chi` handlers registered in [`aisotropy/app.go`](../../aisotropy/app.go)
— **outside** the Huma/OpenAPI contract, so `/openapi.json` does not describe them. Their exact
shapes, verified against the Go structs:

**`GET /healthz`** — always `200` with the literal body (no struct, hardcoded in `app.go`):

```json
{"status":"ok"}
```

**`GET /health`** — `health.Report` (`internal/health/health.go`), `200` when every probe passes,
`503` when any is degraded. Two probes are wired (`facts`, `semantic`), each a read-only search
scoped to the reserved `_health` tenant, bounded by a 2s per-probe timeout, run in parallel.
`status` is `"ok"` or `"degraded"` (both top-level and per-check); `detail` is omitted when the
check passes (`omitempty`):

```json
{
  "status": "ok",
  "checks": [
    { "name": "facts",    "status": "ok", "latency_ms": 1 },
    { "name": "semantic", "status": "ok", "latency_ms": 0 }
  ]
}
```

**`GET /costs`** — `200` with a JSON array of `costmeter.TenantStats`
(`internal/costmeter/costmeter.go`), sorted by tenant id ascending; `[]` when no usage has been
recorded. ⚠️ The struct has **no `json` tags**, so the fields serialize under their Go names
(PascalCase), unlike every other route:

```json
[
  {
    "TenantID": "acme",
    "Requests": 12,
    "InputTokens": 5321,
    "OutputTokens": 987,
    "ReasoningTokens": 140,
    "CostUSD": 0.030766,
    "UnmeteredTurns": 1
  }
]
```

Every count is an `int64`; `CostUSD` is a float. Field notes, in the order they appear:

- **`OutputTokens`** is the provider's complete completion count. **`ReasoningTokens`** is a
  *breakdown inside it*, not an addition — surfaced separately so a tenant's spend on hidden
  reasoning is visible, and deliberately **not priced a second time** (see
  [ADR-0061](../decisions/0061-reasoning-model-support.md); the arithmetic lives in one place,
  `internal/api/turn_cost.go`). Adding `OutputTokens + ReasoningTokens` to get a total is the exact
  mistake that over-charged reasoning turns by 22.7% before 2026-08-04.
- **`UnmeteredTurns`** (added 2026-08-04) is how many of `Requests` were served **without** any
  provider usage block reaching this process — a stream cancelled, errored, or disconnected before
  its terminal usage frame. Those turns still cost money upstream, so they are counted as requests
  and tallied here, contributing **zero** tokens and **zero** cost rather than being estimated: a
  fabricated number inside a cost meter is worse than an absent one, because nothing downstream can
  later tell the two apart. `Requests - UnmeteredTurns` is the metered population, and the ratio is
  the accounting-fidelity signal — a rising ratio means the USD figure is drifting below reality.

Per-process and in-memory only: totals reset on restart and are not aggregated across replicas. That
caveat is usually read as "the numbers are approximate", and for this endpoint it is — but the same
meter also feeds `gateway.CostAwarePolicy`, which is **not** observational: it decides when to shape
a tenant onto the cheap model. So the per-tenant **cost budget** inherits both limits, resetting on
every restart and multiplying by replica count. See the
[threat model](threat-model.md)'s gap ledger, item 3.

**`GET /bitrate`** — `200` with a JSON array of `bitrate.TenantSummary`
(`internal/bitrate/bitrate.go`), one aggregate per tenant (`[]` when no measurements exist);
`500 {"error":"bitrate summary failed"}` if the store read fails. Only registered when
`AISOTROPY_BITRATE_BIN` is set (the durable bpt store is gated on the scorer binary):

```json
[
  {
    "tenant_id": "acme",
    "latest_lift": 0.42,
    "latest_bpt_with": 3.1,
    "trend_slope": -0.05,
    "model_id": "DeepESP/gpt2-spanish@pinned",
    "n_measurements": 8
  }
]
```

`latest_lift` is `BptWithout − BptWith` for the newest measurement (positive = hydrated memory made
the tenant's own text less surprising); `trend_slope` is the linear trend over the tenant's whole
lift series; `n_measurements` an integer count.

## Contract endpoints (in the OpenAPI schema)

### `POST /v1/tenant/{tenant_id}/user/{user_id}/chat` — structured chat

Non-streaming, structured-output. Runs the reflection loop (Gollem default, homegrown fallback) and
**fails closed**: a persistent schema violation after 3 attempts returns **`502`**, never
unstructured text.

- **Body**: `ChatRequest` · **200**: `ChatOutput` (`reply`, `tenant_id`, `stub`).
- **Path**: consolidates memory via the outbox (like the streaming route).
- **Errors**: `422` (contract), `429`-equivalent rejection when the per-tenant rate bucket is empty
  (before any LLM call), `502` (reflection fail-closed).

### `POST /v1/tenant/{tenant_id}/user/{user_id}/chat/stream` — SSE streaming chat

The full hot path: identity → rate-limit gate → sequential memory hydration (working + facts FTS5 +
semantic RAG) → scope-gated tool fragment → `AppendWithOutbox` → (optional cheap-model degradation
at ≥95% quota) → circuit-breakered + OTel-traced LLM stream.

- **Body**: `ChatRequest` · **Response**: `text/event-stream`.
- **SSE events**: `token{ text }` (repeated) → `done{ tenant_id }` on a clean finish (only then is
  the assistant turn persisted and cost recorded), or `error{ message }` on rate-limit denial,
  mid-stream failure, or hydration failure.
- **Cancellation**: client disconnect / `ctx` expiry stops the upstream LLM call **immediately** and
  does **not** persist the partial reply — the spend-cutoff mechanism.

### `POST /v1/tenant/{tenant_id}/user/{user_id}/chat/agent` — agentic tool use

A bounded tool loop over the caller's **scope-permitted** tools. Native provider tool-calling when
the client implements `ToolCaller` (OpenAI-compatible/OpenRouter, ADR-0012), else the homegrown
structured-output `agentloop`. A tool outside the scopes is never offered nor executable.

- **Body**: `ChatRequest` · **200**: `AgentOutput` (`tenant_id`, `steps[]`).
- **Errors**: `422`; `503` when agent mode is not configured (nil client/registry/executor);
  `500` on an agent-loop failure (e.g. provider error). A non-converging run within `maxSteps`
  returns `200` with partial steps (graceful).
- ⚠️ **Gaps**: this route does **not** rate-limit, meter cost, hydrate, or persist memory
  ([threat model](threat-model.md) T4).

### Knowledge ingestion (ADR-0018/0019)

| Method · Path | Body / Path | Response |
|---|---|---|
| `POST /v1/tenant/{tenant_id}/user/{user_id}/knowledge` | `KnowledgeRequest{ documents[] }` | `{ chunks_ingested }` — chunk, embed (BYO embedder), store as user-scoped `KindDocument` |
| `DELETE /v1/tenant/{tenant_id}/user/{user_id}/knowledge/{doc_id}` | path `doc_id` (`^[a-zA-Z0-9_.-]{1,128}$`) | `{ deleted }` |
| `POST /v1/tenant/{tenant_id}/knowledge` | tenant-wide (no `user_id`) | `{ chunks_ingested }` — tenant-shared `KindDocument`, retrievable by every user in the tenant |
| `DELETE /v1/tenant/{tenant_id}/knowledge/{doc_id}` | tenant-wide | `{ deleted }` |

Ingesting the same `id` idempotently replaces its chunks. Requires a real embedder
(`AISOTROPY_EMBED_URL`) to rank semantically; the default `HashEmbedder` wires but does not embed
meaningfully ([reality ledger](reality-ledger.md)).

## Error contract summary

| Code | Meaning |
|---|---|
| `422` | Contract validation failed (bad path param, missing/oversized body field) — Huma, before the handler |
| `429`-equiv | Per-tenant rate bucket empty — surfaced as an SSE `error` on the streaming route, before any token is spent |
| `500` | Agent-loop / internal failure |
| `502` | Reflection fail-closed — no schema-valid structured reply after retries (`/chat` only) |
| `503` | Agent mode not configured, or `/health` reports a degraded dependency |

## References

- [data model](data-model.md) — the request/response/storage shapes.
- [runtime as built](runtime-as-built.md) — the step-by-step handler execution.
- [reality ledger](reality-ledger.md) — which routes are backed by live vs in-process-fallback tiers.
- Source: `internal/api/*.go`; wiring in [`aisotropy/app.go`](../../aisotropy/app.go).
