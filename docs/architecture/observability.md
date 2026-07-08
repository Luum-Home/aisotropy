---
type: Concept
title: Observability
description: OTel Semantic Conventions for LLMs applied to Aisotropy — one Span per interaction, cost per tenant, and the North Star metric (bits/token) as a derived attribute.
tags: [architecture, observability, otel, tracing, cost, north-star]
timestamp: 2026-07-08T00:00:00Z
---

# Observability

> "Observable by design" isn't a feature bolted on afterward: it's step 3 of the [stack's golden
> rule](overview.md) carried into traces. If something can't be measured in tokens, latency, and
> cost per `tenant_id`, it's a design bug — the same anti-magic spirit as the
> [thesis](../vision/thesis.md).

## The standard: OTel Semantic Conventions for LLMs

Aisotropy doesn't invent its own telemetry schema. It adopts
**[OpenTelemetry](https://opentelemetry.io/) (OTel)** and, within it, the
**GenAI/LLM Semantic Conventions** — the standardized attribute vocabulary already understood by
the AI observability tools in the ecosystem. The SDK is Go's official one
(`go.opentelemetry.io/otel`), consistent with the fact that the entire hot path runs in-process in
the same Go binary ([stack overview](overview.md)).

**Base rule:** one **Span per agent interaction** — a complete call
Huma → Gollem → LLM → response is the minimum trace unit, with nested sub-spans for each costly
step of the [request lifecycle](overview.md#lifecycle-of-a-request).

## LLM call attributes

Every Span wrapping a model invocation carries, at minimum, the standard GenAI Semantic
Conventions attributes:

| OTel Attribute | Content | Why it matters |
|---|---|---|
| `gen_ai.system` | Model provider (`openai`, `anthropic`, etc.) | Allows comparing cost/latency across providers without changing the trace schema. |
| `gen_ai.request.model` | Requested model | Audit trail: what was *attempted* (relevant if the circuit breaker degrades the model). |
| `gen_ai.response.model` | Model that actually responded | May differ from the requested one if there was a fallback — full visibility, zero magic. |
| `gen_ai.usage.input_tokens` | Input tokens consumed | Direct cost input. |
| `gen_ai.usage.output_tokens` | Output tokens generated | **Vital**: together with the input one, allows computing exact **cost per Tenant**, per request and aggregated over time. |

These last four attributes are the foundation of any per-customer cost dashboard — without them,
"how much does Tenant X cost us" is an estimate; with them, it's a query.

## Custom domain attributes

On top of the GenAI standard, Aisotropy adds the attributes that make the trace *actionable* for
a multi-tenant framework:

- **Identity and isolation:** `tenant_id`, `user_id`, `product_id` on **every** Span of the
  request (not just the LLM call one) — the same `ctx` that isolates memory queries
  ([multi-tenancy](multi-tenancy.md)) also travels to the trace. This is what allows filtering
  "all traces for Tenant X" without downstream joins.
- **`agent.optimization.*` — self-improvement auditability:** when the
  [sleep cycle](memory-tiers.md#4-batch-consolidation--the-sleep-cycle) detects friction and
  **mutates** a user's primitive (rewrites a description, adds a counter-example), it emits its
  own Span with:
  - `agent.optimization.user_id` — whose primitive was mutated.
  - `agent.optimization.primitive` — which tool changed.
  - `agent.optimization.reason` — why (e.g. "N reflection-loop retries in 7 days").

  Without this Span, [primitive self-improvement](memory-tiers.md#primitive-self-improvement-behavioral-memory)
  would be exactly the kind of "automated and opaque decision" the [anti-magic thesis](../vision/thesis.md)
  forbids. The Span turns it into an auditable event: what changed, for whom, and why, with a
  timestamp and full backward traceability.

## One Span per memory layer

Since all of [tiered memory](memory-tiers.md) hydrates **in-process** (BadgerDB, rqlite, and
Qdrant are queried from the same Go binary, in parallel, step 3 of the
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
From there, routing goes to tools that natively understand AI traces:

- **[Langfuse](https://langfuse.com/)** or **[Arize Phoenix](https://phoenix.arize.com/)** — read
  GenAI Semantic Conventions natively: cost/tenant dashboards, model comparison, and conversation
  replay without transforming the schema.
- **Existing Jaeger/Grafana stack** — if the organization already runs general-purpose
  observability, the same OTel Spans are ingested there with no separate AI-only pipeline.

The choice isn't exclusive: the Collector can fan out to both (an AI backend for product, the
generic stack for infra) without duplicating instrumentation in the code.

## Cross-reference with the compression thesis: bits/token/tenant lives here

The project's **North Star** metric —
[bits-per-token-per-tenant](../research/bet-06-bits-per-token-metric.md) (`bpt`) — isn't a
separate measurement system: it's **one more OTel attribute**, derived, attached to the same
`gen_ai.completion` Span. It's cheap to compute because it comes from the **logprobs the model
already returns** to generate the response — there's no extra inference pass.

```
Span: gen_ai.completion
 ├─ gen_ai.usage.input_tokens
 ├─ gen_ai.usage.output_tokens
 └─ aisotropy.bpt            ← derived from logprobs, at no additional cost
```

Tracking `aisotropy.bpt` as a time series per tenant is, literally, seeing in production whether
"the agent learned the user" — the central promise of the
[compression thesis](../vision/compression-thesis.md#north-star-bits-per-token-per-tenant) —
stops being a narrative and becomes a curve that goes down (or doesn't) on a dashboard.

## Cross-reference with granular control

The same per-token cost data this layer exposes (`gen_ai.usage.*` aggregated by `tenant_id`) is
the **input to the circuit breaker** described in
[granular control](granular-control.md): the decision to degrade to a cheaper model or return
`429` when a tenant exhausts its quota is made over the same series observability exports. These
aren't two systems — the trace *is* the source of truth for the cost the quota control consumes.

## References

- [Architecture overview](overview.md) — where Spans fit in the request lifecycle.
- [Request lifecycle](request-lifecycle.md) — step by step, which component generates each Span.
- [Tiered memory](memory-tiers.md) — the three hydrations instrumented separately.
- [Bet 6 — Bits-per-token-per-tenant](../research/bet-06-bits-per-token-metric.md) — the North Star metric.
- [Granular control](granular-control.md) — consumer of per-token cost (cost axis, circuit breaker).
- [Multi-tenancy](multi-tenancy.md) — the `ctx`-based isolation that also travels to the trace.
