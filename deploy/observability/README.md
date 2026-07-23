# Aisotropy observability stack (day-2 operability)

Operator-run, **optional** telemetry layer for a running `aisotropyd`. It turns
the signals the runtime already emits into dashboards, SLOs, and alerts. It
changes **no runtime code** — everything here is as-code config.

## What the runtime emits (the ground truth)

| Signal | Source | Reaches this stack via |
|---|---|---|
| `gen_ai` spans (`chat`, `chat.tools`) with `tenant_id`, `user_id`, `gen_ai.system`, `gen_ai.request.model`, real `gen_ai.usage.*_tokens`, `ttft_ms`, `duration_ms`, span status | `internal/llm/otel.go` | OTLP → collector → **spanmetrics** (RED) + **sum** (tokens) connectors → Prometheus |
| Liveness `GET /healthz` (200 while up) | `cmd/aisotropyd` | blackbox-exporter probe |
| Readiness `GET /health` (200 ok / 503 degraded, per-tier `checks[]`) | `internal/health` | blackbox (overall) + json-exporter (per-tier) |
| Per-tenant cost/tokens `GET /costs` (ops-token gated) | `internal/costmeter` | json-exporter (**opt-in**, needs `AISOTROPY_OPS_TOKEN`) |
| Access-audit log (tenant/user/method/route/status/latency) | `internal/audit`, ADR-0047 | file/slog — **not scraped**; used in runbooks |

**Key fact:** aisotropyd exposes **no Prometheus `/metrics` endpoint**. Request
rate / latency / errors are **derived from traces** by the collector. If
`OTEL_EXPORTER_OTLP_ENDPOINT` is unset, the tracer is a no-op and this stack
shows no serving metrics.

## Wiring `OTEL_EXPORTER_OTLP_ENDPOINT`

Point the runtime at the collector **before it starts**:

```bash
# root stack (aisotropyd + memory tiers)
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318 docker compose up -d

# then the observability stack (joins the same docker network)
docker compose -f deploy/observability/docker-compose.observability.yml up -d
```

`otlptracehttp` reads the standard OTel env vars itself; `:4318` = OTLP/HTTP,
`:4317` = OTLP/gRPC (`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`). Tracing **fails
open**: a bad endpoint never blocks serving traffic (`otel.go` `NewTracerProvider`).

UIs after `up`: Grafana http://localhost:3000 (admin/admin), Prometheus
http://localhost:9090, Alertmanager http://localhost:9093.

Verify the pipeline:

```bash
curl -s localhost:8889/metrics | grep aisotropy_spanmetrics   # derived RED metrics
curl -s 'localhost:9090/api/v1/rules' | jq '.data.groups[].name'  # rules loaded
```

## Licensing (honest distinction — license gate)

The repo license gate (`.claude/rules/cos/license-policy.md`) **blocks AGPL for a
code dependency**, and the sibling constitutional gate
(`.claude/rules/constitutional-gates.md`) requires "an explicit ADR" for any
copyleft exception. Grafana core is **AGPL-3.0**. That block does **not** apply
here, and the distinction is deliberate — formalized as the required exception
ADR in [ADR-0067 — Grafana AGPL-3.0 exception for the local observability
stack](../../docs/decisions/0067-grafana-agpl-exception-observability-stack.md):

- Grafana is **not an aisotropy dependency**. It is not imported, linked,
  vendored, or shipped with the runtime. No aisotropy artifact contains Grafana
  code.
- It is a **separate, operator-run service**, unmodified, consumed only over its
  HTTP UI/API — exactly the `license-policy.md` **Exceptions** shape (separate
  service, unmodified, documented, public API).
- It is **optional**: the runtime and its differentiation run without it.

Every other service in the stack is **Apache-2.0** (OTel Collector, Prometheus,
Alertmanager, blackbox-exporter, json-exporter).

**Permissive swap:** to avoid AGPL entirely, replace Grafana with **Perses**
(Apache-2.0) for dashboards — the Prometheus datasource and PromQL are identical;
the dashboard JSON is re-authored in Perses' schema. Prometheus itself is already
Apache-2.0, so no metrics-store swap is needed (VictoriaMetrics, also Apache-2.0,
is a drop-in if you want it).

## Residual gaps (do not overclaim)

1. **RED metrics cover the LLM call, not the full HTTP request.** A request
   rejected *before* the model call (quota 429, breaker open, guardrail block)
   may open no `gen_ai` span and is invisible in spanmetrics. Detect via the
   access-audit log — see `docs/operations/runbooks.md`.
2. **No native counters for circuit-breaker trips, rate-limit/quota rejections,
   or guardrail hits.** `internal/llm/breaker.go` and `internal/memory/quota.go`
   expose no metric; guardrails only `slog`. Alerts for these are intentionally
   absent (a placeholder is documented in `aisotropy-alerts.yml`). Ship the
   audit log / slog to **Loki** for log-derived panels, or add counters
   upstream. **Loki is AGPL-3.0** (same family as Grafana below) and is **not
   implemented** — this overlay is a suggestion, not shipped config. If it is
   ever built, it is covered by the Grafana exception in
   [ADR-0067](../../docs/decisions/0067-grafana-agpl-exception-observability-stack.md)
   under the same five conditions (separate service, unmodified, API-only,
   documented risk, operator-approved) — verify those conditions against the
   actual Loki wiring before shipping it; no second ADR is required if they hold.
3. **USD cost is opt-in.** Real token *counts* are always available; USD *cost*
   requires the `/costs` scrape with an ops token (json_exporter can't expand env
   vars — render the config first). Token spend is the always-on proxy.
4. **Traces are metricized, not stored.** The collector derives metrics and logs
   spans to stdout (`debug`). Add a Tempo/OTLP trace-store exporter for span
   drill-down.
5. **Per-tier readiness status is a label, not a number.** json_exporter can't
   turn `ok|degraded` into a numeric value, so tier verdict rides as a Prometheus
   label; the numeric series is probe latency.

## Files

```
docker-compose.observability.yml   the stack
.env.example                       local-only secrets template
otel-collector/config.yaml         OTLP in -> spanmetrics/sum -> Prometheus out
prometheus/prometheus.yml          scrape jobs (collector, blackbox, json)
prometheus/rules/                  SLO recording + alerting rules
alertmanager/alertmanager.yml      routing (null receiver by default)
blackbox/config.yml                http_2xx probe module
json-exporter/config.yml           /health per-tier + /costs (opt-in)
grafana/provisioning/              datasource + dashboard providers
grafana/dashboards/                Overview, Cost, Reliability (JSON)
```
