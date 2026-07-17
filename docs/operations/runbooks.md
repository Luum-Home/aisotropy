# Aisotropy runbooks — the 3am playbooks

Symptom → diagnosis (which signal/endpoint) → mitigation. Every diagnosis step
names a **real** signal that exists today. Where a signal is missing, the
runbook says so and routes you to the fallback (usually the access-audit log).

## Operator quick reference

```bash
# --- Endpoints (default addr :8080; override AISOTROPY_ADDR) ---
curl -s localhost:8080/healthz                       # liveness (always 200 if up)
curl -s localhost:8080/health | jq                   # readiness + per-tier checks; 503 if degraded
curl -s -H "Authorization: Bearer $AISOTROPY_OPS_TOKEN" localhost:8080/costs | jq   # per-tenant cost meter
curl -s -H "Authorization: Bearer $AISOTROPY_OPS_TOKEN" localhost:8080/bitrate | jq # bits-per-token trend

# --- Observability stack ---
# Grafana localhost:3000 | Prometheus localhost:9090 | Alertmanager localhost:9093
# Derived metrics: curl -s localhost:8889/metrics | grep aisotropy_spanmetrics

# --- Access-audit log (ADR-0047): identity + request metadata per /v1 request ---
# File sink when AISOTROPY_AUDIT_LOG is set; else slog (component=access_audit).
# Fields: timestamp, tenant_id, user_id, method, route, status, request_id, latency_ms
tail -n 200 "$AISOTROPY_AUDIT_LOG" | jq -c 'select(.status >= 400)'
```

Relevant env: `OTEL_EXPORTER_OTLP_ENDPOINT` (traces on/off), `ANTHROPIC_API_KEY`
/ `OPENROUTER_API_KEY` / `OPENAI_API_KEY` (else keyless stub), `RQLITE_URL`,
`QDRANT_HOST`, `AISOTROPY_DATA_DIR`, `AISOTROPY_OPS_TOKEN`, `AISOTROPY_AUDIT_LOG`.

---

## high-error-rate — fleet error-budget burn firing

**Symptom:** `AisotropyErrorBudgetBurnCritical/Warning`. Overview dashboard error
ratio red.

**Diagnose:**
1. Confirm scope — one tenant or fleet-wide?
   `sum by (tenant_id) (rate(aisotropy_spanmetrics_calls_total{status_code="STATUS_CODE_ERROR"}[5m]))`
   in Prometheus. One tenant dominating → go to [tenant-reports-errors](#tenant-reports-errors).
2. Break down by model/provider:
   `sum by (gen_ai_system, gen_ai_request_model, status_code) (rate(aisotropy_spanmetrics_calls_total[5m]))`.
   All errors on one `gen_ai_system` → upstream provider incident.
3. Check the breaker: a provider failing trips `ErrCircuitOpen`
   (`internal/llm/breaker.go`) → requests fail fast. See [quota-breaker](#quota-breaker).
4. Check readiness: `curl -s :8080/health | jq` — a degraded tier can surface as
   hydration errors. See [tier-degraded](#tier-degraded).

**Mitigate:**
- Upstream provider down → the runtime already fails open on tracing and the
  breaker backs off; if a cheap-model fallback is configured (`DegradeAt`,
  `internal/llm/degrade.go`), confirm it is engaging. If a single provider is
  bad, fail over by pointing the runtime at another key/provider and restart.
- Bad deploy correlated with the burn → roll back the aisotropyd image.
- Record budget burned in the post-incident review (see `slo.md`).

## tenant-reports-errors — a single tenant is failing

**Symptom:** `AisotropyTenantErrorRateHigh{tenant_id=...}`, or a customer report.

**Diagnose:**
1. Isolate the tenant in Grafana Overview (`$tenant` variable) — error ratio +
   p95 for that tenant only.
2. Audit log for that tenant's recent failures:
   `grep '"tenant_id":"<T>"' "$AISOTROPY_AUDIT_LOG" | jq -c 'select(.status>=400)'`
   — note `route`, `status`, `latency_ms`, `request_id`.
3. `status` 429 → the tenant is being rate-limited/quota-throttled
   (`internal/memory/quota.go`); see [quota-breaker](#quota-breaker).
   `status` 5xx with fast `latency_ms` → likely breaker-open for the provider.
   `status` 5xx with slow `latency_ms` → generation/timeout; see
   [latency-spike](#latency-spike).
4. Errors only on tool-calling → a scope/tool config issue for that tenant
   (scope-gated tools) rather than an infra fault.

**Mitigate:**
- Legitimate quota exhaustion → raise the tenant's bucket (rate/burst) if the
  plan allows, or communicate the limit.
- Poison prompt / abusive loop → correlate `request_id`s; if one client is
  looping, throttle it. Cost impact: [cost-anomaly](#cost-anomaly).
- Never widen isolation to "fix" it — a cross-tenant symptom is a security event:
  [cross-tenant](#cross-tenant).

## latency-spike — p95/p99 over objective

**Symptom:** `AisotropyLatencyP95High` / `AisotropyLatencyP99High`.

**Diagnose:**
1. TTFT vs. total: the span carries `aisotropy.ttft_ms` and `aisotropy.duration_ms`.
   High TTFT → upstream model slow to start. High duration with normal TTFT →
   long generations. (If a trace store is wired, drill into slow spans.)
2. Is it all models or one? Break down `duration_milliseconds` by
   `gen_ai_request_model`.
3. Memory-tier latency: `aisotropy_health_tier` (per-tier probe latency) — a slow
   rqlite/Qdrant adds hydration latency before the LLM call.
4. Collector backpressure inflating apparent latency? Check
   `otelcol_receiver_refused_spans` (Reliability dashboard).

**Mitigate:**
- Upstream slowness → engage cheap/faster model fallback (`DegradeAt`) if
  configured; consider lowering max output tokens for the affected path.
- Slow tier → see [tier-degraded](#tier-degraded); scale Qdrant (the one tier
  meant to scale independently) or move facts to the in-process fallback
  temporarily by unsetting `RQLITE_URL` (accepts the durability trade-off).
- Client cancellations are normal and stop the upstream call immediately (ctx
  cancellation) — they should not count as errors.

## liveness-down — /healthz failing

**Symptom:** `AisotropyLivenessDown`. blackbox `probe_success` on `/healthz` = 0.

**Diagnose:**
1. Is the process up? `docker compose ps aisotropyd` / orchestrator status.
2. Logs: `docker compose logs --tail=200 aisotropyd` — look for panic, OOM, port
   bind failure (`AISOTROPY_ADDR`).
3. Network: can the observability net reach it? (`aisotropyd:8080` resolvable?)
   A green process with a red probe = a networking/DNS problem, not an app crash.

**Mitigate:**
- Crash/OOM → restart; if it recurs, capture logs and roll back the last change.
  Increase memory limits if BadgerDB working set grew.
- Bind failure → fix `AISOTROPY_ADDR`; ensure no port collision.

## tier-degraded — /health returns 503

**Symptom:** `AisotropyReadinessDegraded` and/or `AisotropyTierDegraded{tier=...}`.
Readiness stat DEGRADED.

**Diagnose:**
1. `curl -s :8080/health | jq` — the `checks[]` array names the failing tier and
   its `detail`. Probes are cheap, read-only, `_health`-tenant-scoped
   (`internal/health`), so a degraded verdict is a real dependency failure, not
   probe cost.
2. facts tier degraded → rqlite (`RQLITE_URL`) unreachable or no leader:
   `curl -s $RQLITE_URL/readyz`. semantic tier → Qdrant (`QDRANT_HOST`):
   `curl -s $QDRANT_HOST:6333/readyz` (or gRPC 6334 the client uses).
3. With the bare binary (no `RQLITE_URL`/`QDRANT_HOST`) tiers run in-process and
   should never be "networked-degraded" — a degrade there points at disk/`AISOTROPY_DATA_DIR`.

**Mitigate:**
- Restart/repair the failing service (rqlite leader election, Qdrant volume).
- Emergency fallback: unset the tier's env var and restart to drop to the
  in-process backend (loses networked durability/scale but restores readiness).
  Document the trade-off; re-enable when the service is healthy.
- Note: LLM reachability is deliberately NOT probed (would spend tokens), so a
  provider outage shows up as errors, not readiness — cross-check
  [high-error-rate](#high-error-rate).

## quota-breaker — rate-limit / quota / circuit-breaker tripped

**Signal gap (be honest):** there is **no metric** for quota rejections or
breaker state today (`internal/memory/quota.go`, `internal/llm/breaker.go` expose
none). Detect via HTTP status in the **audit log**.

**Symptom:** clients get 429 (quota/rate-limit) or fast 5xx (breaker open); no
dedicated alert fires.

**Diagnose:**
1. Rate-limit / quota (429):
   `jq -c 'select(.status==429)' "$AISOTROPY_AUDIT_LOG" | tail -50` — group by
   `tenant_id`. Token-bucket is per-tenant in BadgerDB; a single tenant flooding
   is expected throttling, fleet-wide 429 is a misconfigured limit.
2. Circuit breaker: fast 5xx clustered by `gen_ai_system` in the error breakdown,
   and `ErrCircuitOpen` in `docker compose logs aisotropyd`. The breaker opens
   after `threshold` failures and half-opens after `cooldown`.

**Mitigate:**
- Quota: adjust the tenant's rate/burst if the plan allows; otherwise the 429 is
  the system working as designed (reject/degrade **before** spending tokens — a
  constitutional gate). Communicate the limit.
- Breaker open: it is protecting you from a failing provider — do **not** disable
  it. Fix/replace the upstream (rotate key, switch provider), then it half-opens
  and recovers automatically. Confirm recovery on the Overview error ratio.
- Follow-up: add `aisotropy_quota_rejections_total` / `aisotropy_breaker_state`
  counters upstream so this becomes an alert, not log forensics (see `slo.md`
  gaps).

## cost-anomaly — spend spike

**Symptom:** `AisotropyTokenSpendSpike{tenant_id=...}` (always-on, token-based), or
a USD spike if the opt-in `/costs` scrape is enabled.

**Diagnose:**
1. Which tenant/model: Cost dashboard → output-tokens/s per tenant; table of
   cumulative tokens by `tenant_id, gen_ai_request_model`.
2. Authoritative snapshot:
   `curl -s -H "Authorization: Bearer $AISOTROPY_OPS_TOKEN" :8080/costs | jq 'sort_by(.CostUSD) | reverse'`
   (JSON keys are Go field names: `TenantID`, `Requests`, `InputTokens`,
   `OutputTokens`, `CostUSD`).
3. Runaway loop vs. real load: audit log for that tenant — a tight burst of
   `request_id`s at high rate suggests an agent loop; steady growth suggests real
   adoption.

**Mitigate:**
- Runaway/abuse → throttle the tenant (lower bucket), or block the offending
  client. The breaker/quota are your levers.
- Legitimate growth → confirm the tenant's plan covers it; consider the
  cheap-model fallback (`DegradeAt`) for cost-sensitive paths.
- Efficiency check: `/bitrate` shows bits-per-token memory lift — a low value
  means context is being spent without payoff.

## cross-tenant — suspected isolation issue

**This is a security event, not a perf issue.** Multi-tenant isolation is a
constitutional gate: `tenant_id` travels only via `context.Context`, and every
tier filters by tenant (Qdrant payload-filtered during the HNSW scan).

**Symptom:** a tenant sees another tenant's data, or a response references data it
should not have.

**Diagnose (use the audit log — ADR-0047):**
1. Pull every access for both tenants around the report window:
   `jq -c 'select(.tenant_id=="<A>" or .tenant_id=="<B>")' "$AISOTROPY_AUDIT_LOG"`
   — the log records `tenant_id`/`user_id` per `/v1` request from the validated
   path identity (ADR-0024 must-agree bind: path == verified principal).
2. Correlate by `request_id` to reconstruct the exact call.
3. Confirm identity source: a leak would mean `tenant_id` came from body/header
   instead of the path — check for any bypass of ctx propagation in recent
   changes to memory reads/writes.
4. Remember the audit log is retained but **not tamper-evident** (honest residual
   in ADR-0047) — treat it as evidence, corroborate with app logs.

**Mitigate:**
- Contain first: if a real leak, take the affected path out of service (feature-
  flag/roll back) before more requests run.
- Escalate as a security incident; preserve the audit log and app logs.
- Root-cause the isolation bug (a new read/write that didn't scope by tenant);
  this is a hard-gate regression, not a config tweak.

## guardrail-flood — spike in guardrail activity

**Signal gap:** guardrails (`internal/guardrails`) emit **slog**, not a counter —
there is no guardrail-hit metric or alert today.

**Symptom:** logs show many guardrail warnings; possible prompt-injection wave.

**Diagnose:**
1. `docker compose logs aisotropyd | grep -i guardrail` — distinguish:
   - `model scanner failed open (regex-only path stands)` → the ML scanner
     errored; regex guardrails still active but weaker. Check the scanner
     subprocess/model dir env.
   - hits/blocks on specific inputs → correlate with `tenant_id`/`request_id` in
     the audit log to find the source.
2. Concentration: one tenant/user flooding injection attempts vs. spread.

**Mitigate:**
- Scanner failed open → restore the multilingual model scanner (check its bin +
  model-dir env); regex path is a floor, not the intended posture.
- Active injection wave from one source → throttle/block that tenant/user via
  the quota lever; escalate if it looks targeted.
- Follow-up: expose a `aisotropy_guardrail_hits_total{kind}` counter so this is
  observable without log grep.

## no-traffic — live process, zero serving metrics

**Symptom:** `AisotropyNoServingTraffic` — `/healthz` green but no `gen_ai` spans
for 15m.

**Diagnose:**
1. Genuinely idle vs. broken pipeline? Real traffic in the audit log while
   metrics are empty ⇒ traces aren't reaching the collector.
2. Is tracing on? `OTEL_EXPORTER_OTLP_ENDPOINT` must be set on aisotropyd (unset
   ⇒ no-op tracer, no metrics — by design). Confirm the collector receiver:
   `docker compose -f deploy/observability/docker-compose.observability.yml logs otel-collector`.
3. Collector dropping? `rate(otelcol_receiver_refused_spans[5m])` > 0 ⇒ backpressure/memory limiter.

**Mitigate:**
- Set/fix `OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318` and restart
  aisotropyd (tracing fails open — safe to toggle).
- Collector overloaded → raise its memory limit or `batch` sizes; scale it.
- If truly idle, silence the alert for the maintenance window.
```
