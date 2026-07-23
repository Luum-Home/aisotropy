---
type: ADR
title: "ADR-0068 — Observability architecture: three signals, one context-metadata spine"
description: aisotropy today emits OTLP traces only — few spans (the boundary span, little else), slog logs with no trace correlation, and zero metrics. This ADR defines the target architecture; a single typed tag vocabulary carried in context.Context and auto-hydrated into every span, log line, and metric; two-stage trace-log correlation; transparent client decorators that turn every downstream call (rqlite, Qdrant, embed, LLM) into a child span; an slog fan-out to OTLP logs; an OTLP-push MeterProvider with RED instruments; and a masking layer that extends the existing redactingExporter with a structural-attribute allowlist. Implemented in layers, vocabulary first.
tags: [decisions, adr, observability, opentelemetry, traces, logs, metrics, slog, correlation, masking, sre]
timestamp: 2026-07-23T00:00:00Z
supersedes: []
revisits: []
---

# ADR-0068 — Observability: three signals, one context-metadata spine

## Status

**✅ Accepted (2026-07-23). Phased implementation** (layer order below). Complements ADR-0065
(local OTLP viewer). Product-scoped: everything lands in `internal/` behind the existing
env-gated seams; nothing changes the public HTTP contract.

## Context (code-truth: what exists today)

- **Traces**: `internal/llm/otel.go` builds a `TracerProvider` (otlptracehttp, env-gated,
  no-op when unset) and emits `gen_ai.*` spans with `tenant_id`/`user_id` on the LLM call —
  but little else: the request boundary and downstream tier calls (rqlite facts, Qdrant
  semantic, embed server, outbox) emit **no child spans**, so a trace shows one or two spans
  where a real request performs a dozen distinct operations. The arena's `observability`
  dimension measures exactly this gap against peers.
- **Logs**: `log/slog` JSON, but **no `trace_id`/`span_id` on log lines** — logs cannot be
  joined to traces in any backend, and there is no standard field vocabulary (call sites pick
  ad-hoc keys).
- **Metrics**: none. No MeterProvider, no RED counters, no latency histograms.
- **Masking**: `redactingExporter` strips prompt/response content from exported spans (the P0
  content-leak fix). It is exporter-level and trace-only; logs have no masking, and there is
  no explicit "never mask correlation fields" contract.

## Decision

One architectural spine, four layers. The spine: **a typed metadata vocabulary carried in
`context.Context`, hydrated into every signal automatically** — instrument once at the
boundary, and every span, log line, and metric downstream inherits the same tags. Built slim
over plain `context.Context` (no bespoke context-manager framework).

### Layer 1 — the typed tag vocabulary (`internal/o11y/keys`)

Compile-time constants, **snake_case throughout** (one deliberate convention, no mixed
dotted/flat drift), grouped in buckets:

| Bucket | Keys |
|---|---|
| `telemetry` | `trace_id`, `span_id`, `request_id`, `session_id`, `correlation_id` |
| `service` | `service_module`, `service_package`, `service_component`, `service_function` |
| `identity` | `tenant_id`, `user_id` (the constitutional pair — always present, never masked) |
| `execution` | `execution_job_id`, `execution_is_async` |
| (reserved) | `auth`, `device`, `locale` buckets — schema reserved, populated when the surface needs them |

Plus a `MetricName` enum (`{domain}_{outcome}_total` convention, `_total` suffix) and a
meter-per-layer scoping enum (`api`, `llm`, `memory`, `facts`, `semantic`, `outbox`).
OTel semantic conventions (`http.*`, `db.*`, `gen_ai.*`) are used verbatim where they exist;
the vocabulary above covers what they don't.

### Layer 2 — two-stage trace↔log correlation + boundary auto-attribution

1. **Boundary bridge (once per request)**: the Huma handler (where `tenant_id`/`user_id`
   already enter `ctx`) drains `span.SpanContext().TraceID()/SpanID()` into the ctx metadata
   and stamps `request_id`/`session_id`. One place, `internal/api`.
2. **Auto-hydration everywhere after**:
   - a `slog.Handler` wrapper adds the telemetry+identity buckets to **every log line** from
     ctx (`logger.InfoContext(ctx, …)` is enough — no per-call field plumbing);
   - the boundary span receives **all** ctx metadata as attributes (one loop, masked);
   - metrics get `service_module`/`service_component` as default labels from ctx.
   Error ergonomics: an `o11y.Error(ctx, err, msg, kv…)` helper auto-extracts `error` and,
   when the error implements `interface{ StackTrace() []uintptr }`, a `stack_trace` field.

### Layer 3 — transparent client decorators (1 span → N spans)

Zero call-site changes; each downstream dependency gets a `SpanKindClient` child span with
semconv attributes, duration, and ≥400/error status classification:

- **HTTP RoundTripper wrapper** for the rqlite HTTP client, the embed-server client, and any
  outbound HTTP (`http.method`, `http.url` (redacted), `net.peer.name`, `http.status_code`,
  `http.duration_ms`), injecting `traceparent` (W3C `TraceContext` + `Baggage` composite
  propagator — which also carries `tenant_id` across process boundaries where needed).
- **Store-call wrappers** for the facts (rqlite/SQLite) and semantic (Qdrant) tiers
  (`db.system`, `db.operation`, `db.statement` (masked), rows/hits counts). "Not found" is
  **not** an error status.
- **LLM call span** already exists (`gen_ai.*`); it becomes a child of the boundary span with
  the shared ctx metadata.
- **Async boundary**: the outbox→consolidation worker extracts/injects trace context via a
  `TextMapCarrier` over the outbox record's metadata, so consolidation work joins the
  originating request's trace.

### Layer 4 — logs over OTLP + metrics

- **Logs**: fan-out the slog handler (stdout JSON + `otelslog` bridge → OTLP logs) behind
  `OTEL_LOGS_ENABLED`; backend-side, logs join traces by `trace_id`. Fail-open like traces.
- **Metrics**: `MeterProvider` with **OTLP push** (`otlpmetrichttp`, same endpoint discipline
  as traces — not a Prometheus pull surface), RED instruments per layer: `*_ok_total`,
  `*_error_total` counters + latency histograms at the API boundary and each tier, labels
  kept low-cardinality (`service_module`, `service_component`, `operation`, `outcome`).
  Instrument-creation errors are logged and swallowed — telemetry never breaks a request.

### Masking contract (extends the existing `redactingExporter`)

- A single masking package serves both spans and logs: **key-rules + validated regex**
  (candidates validated before redaction to kill false positives), labeled replacement
  tokens, env-gated default-off for the regex layer.
- **Structural-attribute allowlist, by contract**: the `telemetry`, `service`, `identity`,
  and `execution` buckets are **never masked** — correlation must survive redaction.
  Content fields (prompt/response/`db.statement` values) are always maskable. The existing
  exporter-level content strip stays as the last-resort backstop.

### Testing kit

`tracetest.SpanRecorder` on a real provider (swap via `otel.SetTracerProvider` +
`t.Cleanup`), buffer-sink slog assertions on JSON fields, injectable fatal action, and a
deterministic in-memory metric reader — telemetry assertions become first-class unit tests.

## Consequences

- **SRE-ready troubleshooting**: any log line or metric pivots to its trace (and tenant) by
  construction; a request's trace shows every tier hop with duration and error class.
- **The arena observability gap closes structurally** (child spans per dependency), not by
  hand-adding spans one call site at a time.
- **Privacy strengthened**: masking becomes a contract with an explicit never-mask
  correlation allowlist, applied to logs too — content never leaves the process unmasked
  even before the exporter backstop.
- **Cost**: one new internal package (`internal/o11y`), decorators at client-construction
  sites, and two new env flags. No stack-walking per call (deliberately dropped — too
  costly); `service_*` tags are set explicitly at module boundaries instead.

### Implementation order

1. `internal/o11y/keys` vocabulary + ctx carry/read helpers (+ tests).
2. Boundary bridge + slog ctx-handler + error helper (correlation live end-to-end).
3. RoundTripper + store decorators + outbox carrier (the span fan-out).
4. `otelslog` fan-out + MeterProvider/RED + masking package unification.

Each layer lands independently verifiable (unit tests + a local Aspire-dashboard smoke per
ADR-0065) before the next.
