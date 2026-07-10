---
type: Reference
title: API reference — every endpoint the runtime serves
description: The first-class endpoint reference for aisotropyd — method, path, request/response/error contract, and streaming/SSE semantics for each route, plus which routes are inside the auto-generated OpenAPI 3.1 contract and which are operational side-channels. The payload shapes live in the data model; this documents the routes that carry them.
tags: [architecture, reference, api, endpoints, openapi, sse, http]
timestamp: 2026-07-10T00:00:00Z
---

# API reference — every endpoint the runtime serves

The OpenAPI 3.1 schema is **auto-generated from the Go structs** and served live at `/openapi.json`
— it is the machine source of truth. This node is the human-facing companion: it lists every route,
its contract, and its error/streaming semantics. Payload shapes are in the [data model](data-model.md);
which tiers back each route (real vs dormant) is in the [reality ledger](reality-ledger.md).

All `/v1/...` routes take `tenant_id` and `user_id` as **path params** (`^[a-zA-Z0-9_-]{1,64}$`,
Huma rejects with **422** before the handler runs), and `session_id` / `scopes` in the body. There
is **no authentication in-process** today — see the [threat model](threat-model.md).

## Operational endpoints (outside the OpenAPI contract)

| Method · Path | Purpose | Response |
|---|---|---|
| `GET /healthz` | Liveness only | `200 {"status":"ok"}` — no dependency checks |
| `GET /health` | Readiness | Parallel, read-only, `_health`-scoped probes of the facts + semantic tiers (no LLM spend). `200 {"status":"ok",checks:[…]}` when all respond, **`503 degraded`** when any fails |
| `GET /costs` | Per-tenant cost view | JSON array of `costMeter.Snapshot()` — per-tenant token totals + estimated USD, sorted by tenant. In-process, non-durable (not a billing system of record) |
| `GET /openapi.json` · `/openapi.yaml` · `GET /docs` | The contract itself + Stoplight docs UI | auto-generated |

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
