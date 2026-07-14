---
type: Concept
title: Observability
description: OTel Semantic Conventions for LLMs applied to Aisotropy — one Span per LLM call, per-tenant cost accounting, and the North Star metric (bits/token) as an out-of-band span event.
tags: [architecture, observability, otel, tracing, cost, north-star]
timestamp: 2026-07-08T00:00:00Z
---

# Observability

> "Observable by design" isn't a feature bolted on afterward: it's step 3 of the [stack's golden
> rule](overview.md) carried into traces. If something can't be measured in tokens, latency, and
> cost per `tenant_id`, it's a design bug — the same anti-magic spirit as the
> [thesis](../vision/thesis.md).

## Implementation status (2026-07-14)

This document mixes what is wired today with the aspirational target. Grounded against the code:

- **REAL** — OTel is a real dependency (`go.opentelemetry.io/otel` v1.44.0 + the OTLP/HTTP trace
  exporter). `internal/llm.WithOTel` decorates the streaming client and emits one span per
  `StreamChat` (span name `chat`) and per native tool call (`chat.tools`), carrying
  `gen_ai.system`, `gen_ai.request.model`, real `gen_ai.usage.input_tokens`/`output_tokens`
  (only when the provider reports usage), plus `tenant_id`/`user_id` when the request `ctx`
  carries them (`internal/llm/otel.go`). Export is **no-op by default**; spans are batched to an
  OTLP endpoint only when `OTEL_EXPORTER_OTLP_ENDPOINT` / `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`
  is set (`llm.NewTracerProvider`). Per-tenant cost is aggregated in-process by
  `internal/costmeter` and served via `GET /costs`. The sleep-cycle worker emits a
  `sleepcycle.run` span with an `agent.optimization.consolidation` event
  (`internal/sleepcycle/worker.go`); the bitrate worker emits a `bitrate.run` span with an
  `agent.compression.bpt` event, surfaced via `GET /bitrate` (`internal/bitrate/coordinator.go`,
  `aisotropy/app.go`).
- **ASPIRATIONAL** — `gen_ai.response.model`, `product_id` on spans, the per-memory-tier
  `hydration.*` sub-spans, and treating `bpt` as a derived serving-span attribute are NOT wired.
  See the dated notes in the sections below.

## The standard: OTel Semantic Conventions for LLMs

Aisotropy doesn't invent its own telemetry schema. It adopts
**[OpenTelemetry](https://opentelemetry.io/) (OTel)** and, within it, the
**GenAI/LLM Semantic Conventions** — the standardized attribute vocabulary already understood by
the AI observability tools in the ecosystem. The SDK is Go's official one
(`go.opentelemetry.io/otel`), consistent with the fact that the entire hot path runs in-process in
the same Go binary ([stack overview](overview.md)).

**Base rule:** one **Span per LLM call** — the streaming `chat` span (and `chat.tools` for a
native tool call) is the minimum trace unit today. Nested sub-spans for each costly step of the
[request lifecycle](overview.md#lifecycle-of-a-request) — e.g. the per-memory-tier hydration
spans below — are the target, not yet emitted (2026-07-14).

## LLM call attributes

Every Span wrapping a model invocation carries, at minimum, the standard GenAI Semantic
Conventions attributes:

| OTel Attribute | Content | Why it matters |
|---|---|---|
| `gen_ai.system` | Model provider (`openai`, `anthropic`, etc.) | Allows comparing cost/latency across providers without changing the trace schema. |
| `gen_ai.request.model` | Requested model | Audit trail: what was *attempted* (relevant if the circuit breaker degrades the model). |
| `gen_ai.response.model` *(aspirational, 2026-07-14 — not yet emitted)* | Model that actually responded | Would differ from the requested one on a fallback. The serving span today sets `gen_ai.request.model` only; the responding model is not yet recorded. |
| `gen_ai.usage.input_tokens` | Input tokens consumed | Direct cost input. |
| `gen_ai.usage.output_tokens` | Output tokens generated | **Vital**: together with the input one, allows computing exact **cost per Tenant**, per request and aggregated over time. |

These last four attributes are the foundation of any per-customer cost dashboard — without them,
"how much does Tenant X cost us" is an estimate; with them, it's a query.

*As built (2026-07-14):* the serving span is named `chat` (or `chat.tools`), and also carries
`gen_ai.operation.name`, a deliberately non-token `aisotropy.stream.delta_count`, and
`aisotropy.ttft_ms` / `aisotropy.duration_ms` (`internal/llm/otel.go`). The per-tenant cost
totals themselves are aggregated in-process by `internal/costmeter` and exposed at `GET /costs`,
not re-derived from exported traces.

## Custom domain attributes

On top of the GenAI standard, Aisotropy adds the attributes that make the trace *actionable* for
a multi-tenant framework:

- **Identity and isolation:** `tenant_id` and `user_id` on the LLM-call spans (`chat`,
  `chat.tools`) when the request `ctx` carries them — the same `ctx` that isolates memory queries
  ([multi-tenancy](multi-tenancy.md)) also travels to the trace, which is what allows filtering
  "all traces for Tenant X" without downstream joins. `product_id` is **not yet threaded** onto
  spans — no per-request ctx carrier exists for it today (`internal/llm/otel.go`, noted as a
  follow-up) — and the "on **every** Span" target awaits the hydration sub-spans below
  (2026-07-14).
- **`agent.optimization.*` — self-improvement auditability:** as built (2026-07-14), the
  [sleep cycle](memory-tiers.md#4-batch-consolidation--the-sleep-cycle) worker emits a
  `sleepcycle.run` span and, on a successful consolidation pass, an
  `agent.optimization.consolidation` span event carrying `rows_in`, `rows_out`, and `dropped`
  (`internal/sleepcycle/worker.go`) — the batch dedup/consolidation is the auditable mutation
  today. The finer-grained per-primitive attributes below are **aspirational**: they belong to
  [primitive self-improvement](memory-tiers.md#primitive-self-improvement-behavioral-memory),
  which exists as a seam but is not yet adopted, so no span emits them today:
  - `agent.optimization.user_id` — whose primitive was mutated.
  - `agent.optimization.primitive` — which tool changed.
  - `agent.optimization.reason` — why (e.g. "N reflection-loop retries in 7 days").

  The intent stands: without an auditable event, self-improvement would be exactly the kind of
  "automated and opaque decision" the [anti-magic thesis](../vision/thesis.md) forbids — what
  changed, for whom, and why, with a timestamp and full backward traceability.

## One Span per memory layer

> **Aspirational (2026-07-14):** the per-tier `hydration.*` sub-spans below are **not yet
> emitted** — no `hydration.badgerdb` / `hydration.rqlite` / `hydration.qdrant` span exists in
> the code. Memory hydration also runs **sequentially** today, not the parallel goroutines drawn
> here (see [runtime-as-built](runtime-as-built.md)). This section describes the target.

Since all of [tiered memory](memory-tiers.md) hydrates **in-process** (BadgerDB, rqlite, and
Qdrant are queried from the same Go binary — sequentially today, parallel goroutines are the
target — step 3 of the
[request lifecycle](overview.md#lifecycle-of-a-request)), instrumenting each hydration is
straightforward — there's no process/network boundary obscuring the measurement:

```
Span: request (tenant_id, user_id, product_id)
 ├─ Span: hydration.badgerdb   (duration, hit/miss)
 ├─ Span: hydration.rqlite     (duration, number of facts retrieved)
 ├─ Span: hydration.qdrant     (duration, number of vectors, top-k score)
 └─ Span: gen_ai.completion    (gen_ai.system, tokens in/out, model)
```

With this, a question that in a stack with external memory (Redis + a vector DB in another
process) would require correlating logs across several services is answered in seconds: **how
much of the request's total latency did each memory tier consume?** If `hydration.qdrant` starts
dominating a tenant's p99, it's a signal that their semantic knowledge volume has grown and may
need quantization tuning or a dedicated shard.

## Export

Spans go out through the standard **OTel Collector** — without coupling to a proprietary backend,
another direct application of [sovereignty](../vision/principles.md#derived-operating-principles).
Export is **off by default (2026-07-14)**: `llm.NewTracerProvider` returns a no-op provider
unless `OTEL_EXPORTER_OTLP_ENDPOINT` / `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` is set, in which case
spans are batched over OTLP/HTTP to that endpoint. From there, routing goes to tools that
natively understand AI traces:

- **[Langfuse](https://langfuse.com/)** or **[Arize Phoenix](https://phoenix.arize.com/)** — read
  GenAI Semantic Conventions natively: cost/tenant dashboards, model comparison, and conversation
  replay without transforming the schema.
- **Existing Jaeger/Grafana stack** — if the organization already runs general-purpose
  observability, the same OTel Spans are ingested there with no separate AI-only pipeline.

The choice isn't exclusive: the Collector can fan out to both (an AI backend for product, the
generic stack for infra) without duplicating instrumentation in the code.

## Cross-reference with the compression thesis: bits/token/tenant

The project's **North Star** metric —
[bits-per-token-per-tenant](../research/bet-06-bits-per-token-metric.md) (`bpt`) — is **not** a
derived attribute on the serving span, and it is **not** free from the serving call's logprobs.
No serving provider exposes token logprobs, so `bpt` cannot be computed on the `chat` span
(`internal/llm/otel.go` states this explicitly). Instead, as built (2026-07-14), `bpt` is
measured **out of band** by an offline reference-model scorer (`internal/bitrate`), invoked as a
subprocess like the sleep cycle, and persisted to a tenant-scoped bpt store. It surfaces two
ways:

- an `agent.compression.bpt` **span event** on the `bitrate.run` span, carrying `tenant_id`,
  `user_id`, `bpt_with`, `bpt_without`, `lift`, and `model_id`
  (`internal/bitrate/coordinator.go`);
- the `GET /bitrate` view (`aisotropy/app.go`).

```
Span: bitrate.run
 └─ event: agent.compression.bpt   (tenant_id, user_id, bpt_with, bpt_without, lift, model_id)
```

Tracking `bpt` (specifically its `lift`) as a per-tenant time series is, literally, seeing in
production whether "the agent learned the user" — the central promise of the
[compression thesis](../vision/compression-thesis.md#north-star-bits-per-token-per-tenant) —
stops being a narrative and becomes a curve on a dashboard. What changes versus the earlier
framing is *where* the number comes from: an out-of-band scorer, not a zero-cost enrichment of
the serving span.

## Cross-reference with granular control

The same per-token usage this layer records (`gen_ai.usage.*` on the `chat` span, aggregated by
`tenant_id`) feeds the cost view in [granular control](granular-control.md). One correction to
the earlier framing (2026-07-14): the per-tenant cost aggregate is held **in-process by
`internal/costmeter`** and read via `GET /costs` — it is not re-derived from exported traces, and
the circuit breaker itself trips on a **provider-failure threshold**, not on cost
(`llm.WithCircuitBreaker`). The token usage on the span and the cost totals in the meter share
the same source (the real provider `Usage`), so they agree — but the meter, not the trace
pipeline, is the source of truth the cost view consumes.

## References

- [Architecture overview](overview.md) — where Spans fit in the request lifecycle.
- [Request lifecycle](request-lifecycle.md) — step by step, which component generates each Span.
- [Tiered memory](memory-tiers.md) — the three hydrations instrumented separately.
- [Bet 6 — Bits-per-token-per-tenant](../research/bet-06-bits-per-token-metric.md) — the North Star metric.
- [Granular control](granular-control.md) — consumer of per-token cost (cost axis, circuit breaker).
- [Multi-tenancy](multi-tenancy.md) — the `ctx`-based isolation that also travels to the trace.
