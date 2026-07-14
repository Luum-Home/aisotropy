---
type: How-To
title: Operations runbook — running aisotropyd in production
description: Operator-facing runbook for the aisotropyd binary — the single-process state caveat (per-replica quota buckets and cost meter), Kubernetes probe mapping for /healthz and /health, what AISOTROPY_DATA_DIR does and does not persist, how the runtime degrades (429 pre-spend, circuit breaker, embedder teardown-on-fault, dormant sleep cycle), and the ops-critical env vars. Every claim carries a code pointer.
tags: [deployment, runbook, operations, kubernetes, probes, persistence, quota, degradation]
timestamp: 2026-07-14T00:00:00Z
---

# Operations runbook — running aisotropyd in production

Status (2026-07-14): written against the code as built — every behavior claim below
carries a pointer to the file or package that implements it. The authoritative env-var
table and the full "what actually boots" ledger live in
[runtime-as-built.md](../architecture/runtime-as-built.md); this runbook covers the
operational consequences.

## 1. Single-process state: do not scale horizontally (yet)

Two enforcement/observability mechanisms are **per-process**, held inside the embedded
working-memory BadgerDB or in plain process memory. Running N replicas behind a load
balancer silently multiplies quotas and fragments cost visibility.

| State | Where it lives | Multi-replica consequence |
|---|---|---|
| Quota token-bucket (per tenant) | BadgerDB under the `quota:` key prefix, embedded in each process (`internal/memory/quota.go`) | Each replica holds its own bucket. N replicas ≈ N× the effective per-tenant rate/burst. The package doc is explicit: "Shared quota across replicas would move this state to the networked tier — out of scope here." |
| Cost meter (`GET /costs`) | In-memory map, no persistence (`internal/costmeter`) | Each replica reports only the requests it personally served; totals reset on restart. The package doc says it plainly: "not synchronized across replicas … not the billing system of record." |

Safe patterns until a shared store lands:

- **Run a single replica** (the measured footprint makes this viable: ~31 MB binary,
  ~30 MB idle RSS, ~85 ms startup — see [distroless.md](distroless.md)).
- **Shard tenants across single-replica instances**: `AISOTROPY_DEDICATED_TENANTS`
  (`aisotropy/app.go`, ADR-0007) routes a tenant's facts/semantic tiers to dedicated
  resources, but note it does **not** shard the rate limiter — isolation of quota state
  comes from each shard being its own process, not from the routing policy.
- If you must run N replicas of the same shard, dividing the configured rate by N is
  only an approximation (per-tenant traffic is rarely balanced) and `/costs` must be
  summed across replicas by your observability layer. Treat both as degraded modes.

## 2. Kubernetes probe mapping

The binary exposes two health endpoints, both outside the OpenAPI contract
(`aisotropy/app.go`):

- `GET /healthz` — **liveness**. Static `{"status":"ok"}` with HTTP 200; proves the
  process is up and serving (`aisotropy/app.go`, the `/healthz` route).
- `GET /health` — **readiness**. Runs the facts and semantic probes in parallel, each
  bounded by a **2 s per-probe timeout** (`readinessProbeTimeout` in
  `aisotropy/app.go`; enforcement in `internal/health`). Returns HTTP 200 when every
  check is `ok`, **HTTP 503** when any check is `degraded`, so a load balancer can gate
  on the status code alone (`internal/health/health.go`, `Handler`). Probes are
  read-only, scoped to the reserved `_health` tenant, and **never spend LLM tokens**
  (`internal/health` package doc).

With the in-process default backends the readiness probes hit local stores (nearly
always green); once `RQLITE_URL` / `QDRANT_HOST` are set, the same probes become real
network readiness checks with no code change (see
[docker-compose.md](docker-compose.md)).

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: aisotropyd
spec:
  replicas: 1          # see section 1 — quota and cost state are per-process
  selector:
    matchLabels:
      app: aisotropyd
  template:
    metadata:
      labels:
        app: aisotropyd
    spec:
      containers:
        - name: aisotropyd
          image: aisotropyd:distroless
          ports:
            - containerPort: 8080
          livenessProbe:
            httpGet:
              path: /healthz
              port: 8080
            initialDelaySeconds: 2
            periodSeconds: 10
          readinessProbe:
            httpGet:
              path: /health
              port: 8080
            initialDelaySeconds: 2
            periodSeconds: 10
            timeoutSeconds: 5   # > the 2s internal per-probe timeout, so the
                                # endpoint answers (with 503) before kubelet times out
          volumeMounts:
            - name: data
              mountPath: /data
          env:
            - name: AISOTROPY_DATA_DIR
              value: /data
      volumes:
        - name: data
          persistentVolumeClaim:
            claimName: aisotropyd-data
```

A degraded dependency flips readiness (503 → pod removed from Service endpoints) but
not liveness — the process keeps running and recovers on its own when the dependency
returns.

## 3. Data persistence: what `AISOTROPY_DATA_DIR` does (and does not) cover

Backend selection happens at boot in `aisotropy/app.go`
(`newWorkingMemory` / `openFactsStore` / `openSemanticStore` / `openBitrateStore`):

| Tier | `AISOTROPY_DATA_DIR` unset (default) | Set | Networked alternative |
|---|---|---|---|
| Working memory (chat turns, summaries, **quota buckets**, **outbox queue**) | In-memory BadgerDB — lost on restart | On-disk BadgerDB at `$AISOTROPY_DATA_DIR` (`internal/memory`) | none (always embedded) |
| Facts (FTS5 journal) | SQLite `:memory:` — lost on restart | `$AISOTROPY_DATA_DIR/facts.db` (per-resource `<name>.db` files with dedicated-tenant routing) | rqlite via `RQLITE_URL` (`internal/facts/factory.go`) |
| Semantic (vectors) | In-memory cosine index — lost on restart | **Still in-memory** — `AISOTROPY_DATA_DIR` does NOT persist vectors (`openSemanticStore` builds `semantic.NewInMemoryStore` whenever `QDRANT_HOST` is unset) | Qdrant via `QDRANT_HOST` (`internal/semantic/factory.go`) |
| Bitrate (Bet 6, only when `AISOTROPY_BITRATE_BIN` is set) | SQLite `:memory:` | `$AISOTROPY_DATA_DIR/bitrate.db` | none |

Operational consequences:

- **Running without `AISOTROPY_DATA_DIR` means a restart loses everything**: chat
  history, summary buffers, distilled facts, vectors, quota buckets, and any
  consolidation outbox backlog that had not drained.
- **Vectors are only durable with Qdrant.** Even with a data dir, semantic memory is
  rebuilt empty on restart; re-ingest knowledge (`/v1/.../knowledge`) or run Qdrant.
- **Cold-boot outbox recovery**: with a data dir, events left in the outbox by a crash
  are drained on the next boot before the steady-state worker starts
  (`wm.RecoverOutbox` in `aisotropy/app.go`).

Backup / restore:

- The binary exposes **no online backup endpoint or API** (no backup code path exists
  in `internal/` or `aisotropy/`). The supported approach is **stop-copy-start**: stop
  the process (SIGTERM triggers graceful shutdown that closes every store —
  `aisotropy/app.go`), copy `$AISOTROPY_DATA_DIR` (BadgerDB dir + `*.db` SQLite
  files), restart. Restore is the reverse.
- When rqlite / Qdrant are wired, that state lives on those services and is backed up
  with their own mechanisms (rqlite snapshots, Qdrant snapshots/collections) — the
  aisotropyd data dir then only holds working memory (and bitrate, if enabled).

## 4. Degradation behavior

What the runtime does when things run out or break:

- **Quota exhaustion — reject before spending tokens.** The token bucket is checked
  before any LLM call (constitutional gate). The non-streaming `chat` route returns
  **HTTP 429** (`huma.Error429TooManyRequests`, `internal/api/chat.go`). On the SSE
  `chat/stream` route the HTTP response is already open, so the client receives a
  stream **error event** (`"rate limit exceeded for tenant …"`) instead of a 429
  status (`internal/api/chat_stream.go`). Either way, no tokens are spent.
- **Quota pressure — cheap-model degradation (opt-in).** With `AISOTROPY_CHEAP_MODEL`
  set, a tenant near its limit is routed to the cheaper model instead of hitting the
  hard reject (`internal/api/chat_stream.go`, `llm.DegradeAt`; client built by
  `llm.CheapClientFromEnv`, `internal/llm/registry.go`). Without it, there is no
  intermediate step: allowed → primary model, exhausted → reject.
- **Circuit breaker open — fail fast, self-heal.** After **5 consecutive** LLM
  start failures the breaker opens for a **30 s** cooldown
  (`circuitFailureThreshold` / `circuitCooldown` in `aisotropy/app.go`); calls fail
  immediately with `ErrCircuitOpen` instead of piling onto a struggling provider
  (`internal/llm/breaker.go`). After the cooldown one half-open trial call either
  closes the breaker or re-opens it. No operator action needed beyond watching logs;
  sustained open state means the upstream provider (or its credentials) is the problem.
- **Embedder daemon fault — teardown-on-fault, auto-respawn.** When the local warm
  embedder daemon is active (`AISOTROPY_EMBED_BIN` + `AISOTROPY_EMBED_MODEL_DIR`,
  `AISOTROPY_EMBED_MODE` unset or `daemon`), any I/O error, daemon crash, or mid-call
  ctx cancellation kills the process and clears it; the failing call returns an error
  and the **next** `Embed` respawns a fresh daemon (~one cold start), guaranteeing the
  pipe is never half-drained (`internal/semantic/daemon_embedder.go`, "Failure
  model"). On the request path a failed semantic search degrades hydration to "no
  semantic context" rather than failing the chat (`internal/api/chat_stream.go`).
- **Sleep-cycle binary absent — dormant, not broken.** With `AISOTROPY_SLEEPCYCLE_BIN`
  unset the DataFusion consolidation worker is simply not scheduled and the boot log
  says so (`"sleep-cycle worker disabled"`, `aisotropy/app.go`). The pure-Go
  turn-vector pruning (TTL/cap on `KindTurn` vectors) runs regardless
  (`aisotropy/app.go`, ADR-0017 Phase 3), so vector growth stays bounded either way.

## 5. Ops-critical environment variables

The authoritative, complete table is in
[runtime-as-built.md](../architecture/runtime-as-built.md) → "Environment variables
(as consumed by the code)". The ops-critical subset:

| Variable | Effect | Code pointer |
|---|---|---|
| `AISOTROPY_ADDR` | HTTP listen address (default `:8080`) | `aisotropy/app.go` |
| `AISOTROPY_DATA_DIR` | On-disk BadgerDB + default SQLite file locations; unset = everything in-memory (section 3) | `aisotropy/app.go` |
| `RQLITE_URL` | Networked rqlite facts store instead of local SQLite | `internal/facts/factory.go` |
| `QDRANT_HOST` (+ `QDRANT_PORT` / `QDRANT_API_KEY` / `QDRANT_USE_TLS` / `QDRANT_COLLECTION`) | Qdrant semantic store instead of the in-memory index — the only durable vector option | `internal/semantic/factory.go`, `aisotropy/app.go` |
| `AISOTROPY_SLEEPCYCLE_BIN` | Enables the hourly DataFusion sleep-cycle worker; unset = dormant | `aisotropy/app.go` |
| `OPENROUTER_API_KEY` / `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` | LLM provider credentials. The runtime boots with the single default client from `llm.FromEnv()`, chosen by precedence `openrouter > openai > anthropic > stub` — the keyless stub is always available, so the binary boots (and answers with canned output) with zero credentials | `internal/llm/registry.go` (`FromEnv` / `RegistryFromEnv`) |
| `AISOTROPY_CHEAP_MODEL` | Enables quota-pressure cheap-model degradation (section 4) | `internal/llm/registry.go`, `CheapClientFromEnv` |
| `AISOTROPY_RATE_PER_SEC` | Per-tenant token-bucket refill rate (requests/sec, float); default `10`. Ignored when the builder sets `WithRateLimit` | `aisotropy/app.go` (`ratePerSecFromEnv`) |
| `AISOTROPY_BURST` | Per-tenant token-bucket capacity (integer); default `30`. Ignored when the builder sets `WithRateLimit` | `aisotropy/app.go` (`burstFromEnv`) |

Rate-limit precedence (2026-07-14): the per-tenant token-bucket rate/burst is resolved
in this order — an explicit builder `aisotropy.WithRateLimit(perSec, burst)` wins;
otherwise `AISOTROPY_RATE_PER_SEC` / `AISOTROPY_BURST` are read from the environment;
otherwise the compiled-in defaults `10` req/s and `30` burst apply (`ratePerSecFromEnv`
/ `burstFromEnv`, gated on `a.rateLimitSet`, in `aisotropy/app.go`). Each env var falls
back to its default (with a `slog.Warn`) when unset, unparsable, or non-positive
(`aisotropy/app.go` `ratePerSecFromEnv` / `burstFromEnv`; covered by
`aisotropy/app_env_test.go`). Because the bucket is per-process (section 1), an
override applies **per replica** — N replicas still multiply the effective per-tenant
rate. The builder option is documented in
[build-your-own-agent](../guides/build-your-own-agent.md).
