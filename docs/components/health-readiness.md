---
type: Component
title: "Health — liveness vs. readiness"
description: The GET /healthz liveness probe and the GET /health readiness endpoint — parallel, timeout-bounded, _health-tenant-scoped probes of the facts and semantic tiers, 200/503 semantics, and why the LLM is deliberately never probed.
tags: [components, health, readiness, liveness, observability, operations]
timestamp: 2026-07-14T00:00:00Z
---

# Health — liveness vs. readiness

Two endpoints, two questions. Status (2026-07-14): **REAL** — both are wired unconditionally
in [`aisotropy/app.go`](../../aisotropy/app.go), outside the Huma/OpenAPI contract.

| Endpoint | Question | Backing | Response |
|---|---|---|---|
| `GET /healthz` | Is the process up? (liveness) | A static handler in `app.go` | Always `{"status":"ok"}`, HTTP 200 |
| `GET /health` | Can the process serve requests? (readiness) | [`internal/health`](../../internal/health/health.go) `Checker` running real dependency probes | `{"status":"ok\|degraded","checks":[...]}` — HTTP 200 when every probe passes, **503** when any is degraded |

The 200/503 split is deliberate: a load balancer or orchestrator can gate traffic on the
status code alone, without parsing the body. Each check in the body reports
`{name, status, detail?, latency_ms}`.

## The probe model (`internal/health`)

[`internal/health/health.go`](../../internal/health/health.go) is a small generic runner: a
`Checker` holds named `Probe`s and a per-probe timeout.

- **Parallel + bounded**: `Run` executes every probe in its own goroutine, each under its own
  `context.WithTimeout` derived from the request context, so one slow or hung dependency
  cannot make the readiness endpoint itself hang. `New` defaults a non-positive timeout to
  **2s**; `app.go` passes exactly that (`readinessProbeTimeout = 2 * time.Second`).
- **Cheap, read-only, side-effect-free**: a probe must never write and must honor ctx
  cancellation.
- **Aggregate verdict**: the report is `ok` only if *every* probe succeeded; any error or
  timeout makes the whole report `degraded`. Check order is stable (matches probe order).

## The wired probes (`_health` tenant)

`app.go` registers two probes, both scoped to the reserved identity
`healthProbeTenant = "_health"` (tenant *and* user), so a probe never reads or writes real
tenant data:

- **`facts`** — a `Search(ctx, scope, "health", 1)` against the facts store: one cheap FTS5
  query under the `_health` scope.
- **`semantic`** — a `Search(ctx, scope, healthProbeVector(dim), 1)` against the semantic
  store: a unit vector of the active embedder's dimension (`[1, 0, 0, ...]`), so the probe is
  valid for whatever dimension is configured.

With the default in-process backends these probes hit the local stores; once rqlite / Qdrant
are configured (`RQLITE_URL` / `QDRANT_HOST`), the **same probes become real network readiness
checks with no code change**.

## Why the LLM is deliberately NOT probed

Actively calling the model on every readiness poll would spend tokens — violating the
"reject/degrade before spending tokens" constitutional gate
(`.claude/rules/constitutional-gates.md`). The package doc in `internal/health` states the
constraint explicitly: no probe may spend LLM tokens; LLM health is a static mode (real vs.
stub client), never an active token-spending call. Readiness therefore covers the memory
tiers, not model reachability.

## Code map

| Path | What it is |
|---|---|
| [`internal/health/health.go`](../../internal/health/health.go) | `Probe`, `Check`, `Report`, `Checker` (parallel run + timeout), `Handler` (200/503 JSON) |
| [`aisotropy/app.go`](../../aisotropy/app.go) | `/healthz` liveness handler; `/health` wiring with the `facts` and `semantic` probes, `healthProbeTenant = "_health"`, `readinessProbeTimeout = 2s`, `healthProbeVector` |

## Gotchas

- **`/healthz` says nothing about dependencies.** Point liveness restarts at `/healthz` and
  traffic gating at `/health`; using `/healthz` for both hides degraded tiers.
- **A degraded report still returns quickly.** The per-probe timeout bounds the worst case at
  ~2s, not the sum of probes (they run in parallel).
- **The `_health` scope is real data-plane traffic.** The probes exercise the same
  tenant-scoped `Search` paths as production reads — which is the point — so a tier that
  rejects the `_health` scope would (correctly) report degraded.

## References

- [Services configuration](../../.claude/rules/services-config.md) — operational summary of
  both endpoints.
- [rqlite — Facts Memory](rqlite-facts-memory.md) · [Qdrant — Semantic Memory](qdrant-semantic-memory.md)
  — the tiers the readiness probes exercise.
- [Runtime as built](../architecture/runtime-as-built.md) — where these endpoints sit in the
  boot sequence.
