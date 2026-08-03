---
type: Reference
title: "Aisotropy SLO catalog & error-budget policy"
description: >
  Operator-facing reliability targets for aisotropyd and the single source of truth the Prometheus rules encode.
tags: [operations, slo, error-budget, reliability, prometheus]
timestamp: 2026-08-03T00:00:00Z
---

# Aisotropy SLO catalog & error-budget policy

Operator-facing reliability targets for `aisotropyd`. These are the single
source of truth the Prometheus rules encode
(`deploy/observability/prometheus/rules/`). Change a target here **and** in the
rules together.

> Scope honesty: the availability/latency SLIs are measured on the **LLM serving
> path** via `gen_ai` spans (`internal/llm/otel.go`), derived into metrics by the
> collector's `spanmetrics` connector. They do **not** yet cover requests
> rejected before the model call (quota 429, breaker open, guardrail block) —
> those are tracked via the access-audit log (ADR-0047). See `runbooks.md`.

## SLI definitions

| # | SLI | Definition (what "good" means) | Measured from |
|---|---|---|---|
| 1 | Availability | Share of LLM calls whose span status is not `ERROR` | `aisotropy_spanmetrics_calls_total{status_code}` |
| 2 | Latency p95 | 95th pct of end-to-end LLM call duration (TTFT + generation) | `aisotropy_spanmetrics_duration_milliseconds_bucket` |
| 3 | Latency p99 | 99th pct of the same | same |
| 4 | Readiness | `GET /health` returns 200 (all memory tiers OK) | blackbox probe on `/health` |
| 5 | Liveness | `GET /healthz` returns 200 | blackbox probe on `/healthz` |

Cost is governed as a **budget signal**, not an SLO (below).

## Targets (objectives)

| SLI | Objective | Window | Rationale |
|---|---|---|---|
| Availability | **99.5%** | 30d rolling | Early multi-tenant runtime; the model provider is a shared upstream failure domain, so 99.5% is honest, not aspirational 99.99%. |
| Latency p95 | **< 3000 ms** | 30d rolling | Span includes model generation time; p95 target reflects a streamed answer, not just TTFT. |
| Latency p99 | **< 8000 ms** | 30d rolling | Tail budget for long generations / slow upstream. |
| Readiness | **99.9%** | 30d rolling | A tier probe (rqlite/Qdrant) failing gates traffic; the target is stricter than availability because it is cheap to keep green. |
| Liveness | **99.95%** | 30d rolling | Process-up; failures are crashes/OOM/deploys. |

> Each "nine" costs roughly 10x. 99.5% availability = **~3.6h/month** of error
> budget; 99.9% readiness = **~43min/month**. Do not tighten a target without a
> plan to pay for it.

## Error-budget policy

Budget = `1 - SLO`, over the 30-day rolling window.

| Availability budget consumed | Posture |
|---|---|
| < 50% | **Ship freely.** Feature work proceeds; take reliability risks deliberately. |
| 50–90% | **Caution.** New rollouts go canary-first; no risky migrations during peak. |
| 90–100% | **Slow down.** Prioritize reliability fixes over features; require review for changes touching the LLM path, memory tiers, breaker, or quota. |
| Exhausted (budget = 0) | **Freeze.** Only reliability/security fixes merge until the budget recovers. Post-incident review required for the burn. |

Burn-rate alerts (multi-window, Google SRE method) page **before** the budget is
gone — see the fast/slow pairs below.

### Burn-rate alert thresholds (encoded in `aisotropy-alerts.yml`)

With availability SLO 99.5% → `(1 - SLO) = 0.005`:

| Severity | Burn factor | Error-ratio threshold | Windows (long & short) | Meaning |
|---|---|---|---|---|
| Critical (page) | 14.4x | 7.2% | 1h **and** 5m | Burns ~2% of the 30d budget per hour. |
| Warning (ticket) | 6x | 3.0% | 6h **and** 30m | Burns ~5% of the 30d budget per 6h. |

The two-window `and` gate suppresses flapping: the long window proves it is
sustained, the short window proves it is still happening now.

Latency has its own static-threshold alerts (p95 > 3s / p99 > 8s for 10m); they
are symptom alerts, not burn-rate alerts.

## What to measure (and where it comes from)

| Question | Metric / endpoint | Panel |
|---|---|---|
| Are we serving? | `aisotropy:llm_requests:fleet:rate5m` | Overview |
| Are we erroring? | `aisotropy:llm_error_ratio:fleet:5m` | Overview |
| Is it slow? | `aisotropy:llm_latency_p95_ms:fleet:5m` / `p99` | Overview |
| Which tenant is hurting? | `aisotropy:llm_error_ratio:5m{tenant_id}` | Overview |
| How much are we spending? | `aisotropy:output_tokens:rate5m`; `/costs` (opt-in USD) | Cost |
| Can we serve (deps)? | `probe_success{.../health}`, `aisotropy_health_tier` | Reliability |
| Is the pipeline healthy? | `otelcol_receiver_refused_spans`, no-traffic alert | Reliability |
| Who accessed which tenant? | access-audit log (ADR-0047) | runbook (log query) |

## Review cadence

- **Weekly:** error-budget consumed vs. remaining; top-3 error-ratio tenants;
  p99 trend.
- **Per incident:** did an SLO breach? Record the budget burned in the
  post-incident review.
- **Quarterly:** re-baseline targets against observed reality — tighten only
  where the budget was consistently underused, loosen only with justification.

## Known gaps to close (roadmap for the SLO surface)

1. **Full-request SLI** (Huma handler level), so pre-LLM rejections count toward
   availability — needs an HTTP server metric or an audit-log-derived metric.
2. **Native counters** for breaker trips / quota rejections / guardrail hits, to
   make those first-class SLIs instead of log forensics.
3. **Authoritative cost SLO** once `/costs` is scraped by default (currently
   opt-in behind the ops token).
