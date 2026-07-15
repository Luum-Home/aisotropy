---
type: How-To
title: Operations runbook — running aisotropyd in production
description: Operator-facing runbook for the aisotropyd binary — the single-process state caveat (per-replica quota buckets and cost meter), Kubernetes probe mapping for /healthz and /health, what AISOTROPY_DATA_DIR does and does not persist, how the runtime degrades (429 pre-spend, circuit breaker, embedder teardown-on-fault, dormant sleep cycle), and the ops-critical env vars. Every claim carries a code pointer.
tags: [deployment, runbook, operations, kubernetes, probes, persistence, quota, degradation]
timestamp: 2026-07-14T00:00:00Z
---

# Operations runbook — running aisotropyd in production

Status (updated 2026-07-15, ADR-0029): written against the code as built — every behavior
claim below carries a pointer to the file or package that implements it. The 2026-07-15 pass
adds the RS256/JWKS auth modes (§6) and the output-safety / model-routing knobs (§7). The
authoritative env-var table and the full "what actually boots" ledger live in
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

## 6. Security & authentication environment variables (ADR-0024 / ADR-0025 / ADR-0029)

The shipped binary is **securable by config** — no fork, no library embedding. The
defaults are chosen so that an unconfigured deployment is loud (a `NoAuth` warning) or
safe (ops endpoints closed), never silently insecure. For any non-local deployment, set
**one** JWT mode (`AISOTROPY_JWT_SECRET` for HS256, or `AISOTROPY_JWT_PUBLIC_KEY[_FILE]` /
`AISOTROPY_JWT_JWKS_URL` for RS256) **and** `AISOTROPY_OPS_TOKEN`, and put TLS in front (or
enable `AISOTROPY_REQUIRE_TLS`).

**Exactly one JWT mode may be set.** `AuthenticatorFromEnv` counts the configured mode
families and **refuses to boot** if more than one is set (HS256 vs RS256-static vs
RS256-JWKS). The **algorithm-confusion defence** is that the *configured* mode fixes the
accepted `alg` — a token whose header `alg` differs is rejected before any signature check,
so an RS256-configured verifier can never be steered onto the HMAC path (`internal/auth/jwt.go`).

| Variable | Effect | Default when unset | Code pointer |
|---|---|---|---|
| `AISOTROPY_JWT_SECRET` | Enables **HS256** bearer-JWT authentication on every `/v1` route (the must-agree bind: 401 missing/invalid, 403 tenant/user mismatch). Verified JWT scopes gate tools. **Must be ≥ 32 bytes** — a shorter secret is a fatal config fault: the binary **refuses to boot** (the error never contains the secret). | `NoAuth()` — path `tenant_id`/`user_id` trusted verbatim, logged as a **startup warning** (only when no RS256 mode is set either). The secret value is never logged. | `aisotropy/authenv.go` (`AuthenticatorFromEnv`, `auth.ValidateHS256Secret`); wired at `cmd/aisotropyd/main.go` |
| `AISOTROPY_JWT_PUBLIC_KEY` / `AISOTROPY_JWT_PUBLIC_KEY_FILE` | Enables **RS256** verification against a **static RSA public key** (ADR-0029) — for tokens minted by an external IdP. `PUBLIC_KEY` is a PEM literal (PKIX or PKCS#1), `PUBLIC_KEY_FILE` a path to the same; the two are **mutually exclusive**. Pins the verifier to RS256 (HS256 tokens rejected). | Off. A parse/read failure is a fatal boot fault whose error **never contains key bytes**. | `aisotropy/authenv.go`, `internal/auth/jwt_rs256.go` (`ParseRSAPublicKeyPEM`, `WithRSAPublicKey`) |
| `AISOTROPY_JWT_JWKS_URL` | Enables **RS256** verification against a **JWKS endpoint** (ADR-0029): the key set is fetched (`net/http`), parsed **stdlib-only** (`math/big`, no external JWK library), TTL-cached (5 min), and the token `kid` matched to a key (bounded refetch on unknown `kid`); response bounded to 1 MiB. **Fails CLOSED** — a fetch failure with no usable cached key rejects the request, never falls open. | Off. | `aisotropy/authenv.go`, `internal/auth/jwt_rs256.go` (`WithJWKS`, `jwksSource`) |
| `AISOTROPY_OPS_TOKEN` | Gates the operator endpoints `GET /costs` and `GET /bitrate` (all-tenant aggregates) behind a shared bearer token, compared constant-time (`crypto/subtle`). Present → the request must carry `Authorization: Bearer <token>` (401 otherwise). The token is never logged. | **Endpoints closed**: every request to `/costs` and `/bitrate` gets **503** until the token is set (default-closed — no cross-tenant leak). | `internal/api/ops_gate.go` (`OpsAuthGate`, `OpsTokenFromEnv`); wired at `aisotropy/app.go` |
| `AISOTROPY_JWT_AUDIENCE` | When set, requires the JWT `aud` claim (string or array, RFC 7519) to contain this value — closes token-confusion / cross-audience replay. **Optional.** | `aud` is not checked (existing tokens keep verifying). | `internal/auth/jwt.go` (`WithAudience`); re-export `aisotropy.WithJWTAudience` |
| `AISOTROPY_JWT_ISSUER` | When set, requires the JWT `iss` claim to equal this value exactly. **Optional.** | `iss` is not checked. | `internal/auth/jwt.go` (`WithIssuer`); re-export `aisotropy.WithJWTIssuer` |
| `AISOTROPY_JWT_MAX_TTL` | When set (a Go duration, e.g. `1h`), rejects a token whose lifetime `(exp - iat)` — or `(exp - now)` when `iat` is absent — exceeds the cap; bounds effectively immortal tokens. A **malformed** value is a fatal config fault (the binary refuses to boot). **Optional.** | Lifetime bounded only by the required `exp` claim. | `internal/auth/jwt.go` (`WithMaxTTL`); re-export `aisotropy.WithJWTMaxTTL`; parsed in `aisotropy/authenv.go` |
| `AISOTROPY_REQUIRE_TLS` | Truthy (per `strconv.ParseBool`) → reject any request that did not arrive over TLS (`r.TLS == nil`) with **HTTP 400**, so a bearer token (the `/v1` JWT or the ops token) can never be sent in plaintext. **Enable only when aisotropyd terminates TLS itself.** | **Off** — plaintext accepted, for the common behind-a-proxy topology where TLS is terminated upstream and plain HTTP is forwarded. | `aisotropy/app.go` (`requireTLSFromEnv`, the pre-route `Use` middleware) |

Notes independent of these levers, so the picture is honest:

- **`exp` is always required** and `nbf`/`iat` are always validated when present (a
  token with no expiry, or a far-future `iat`, is rejected) — this holds regardless of
  the optional `AISOTROPY_JWT_*` hardening above (`internal/auth/jwt.go`).
- **Startup posture is logged, secrets are not.** `cmd/aisotropyd/main.go` logs the auth
  mode (`jwt-hs256`, `jwt-rs256`, `jwt-rs256-jwks`, or the `noauth` warning), whether the ops
  token is set, and whether TLS enforcement is on — presence only, never a value (no HMAC
  secret, RSA key, or JWKS URL).
- **Library users get default-deny.** Building `aisotropy.App` with **no**
  `WithAuthenticator` rejects every `/v1` request with 401; the shipped binary avoids
  that by always passing `WithAuthenticator(AuthenticatorFromEnv())` (which is `NoAuth`
  when the secret is unset). Full rationale: [ADR-0024](../decisions/0024-authenticated-identity-seam.md),
  [ADR-0025](../decisions/0025-security-hardening-authn-ops.md); attack surface: [threat model](../architecture/threat-model.md).

### Secure-deployment checklist

- [ ] **One** JWT mode configured: `AISOTROPY_JWT_SECRET` (HS256, **≥ 32 bytes** — the binary
  refuses to boot on a weaker one) **or** `AISOTROPY_JWT_PUBLIC_KEY[_FILE]` / `AISOTROPY_JWT_JWKS_URL`
  (RS256, for an external IdP). Setting more than one refuses to boot; setting none means the
  insecure `NoAuth` mode with a warning.
- [ ] For the **JWKS** mode, confirm the endpoint is reachable from the pod and served over
  TLS — a fetch failure with no cached key **fails closed** (requests are rejected), which is
  safe but will 401 everyone until the endpoint recovers.
- [ ] `AISOTROPY_OPS_TOKEN` set to a random token if `/costs` / `/bitrate` are exposed
  (unset means those endpoints return 503 — also safe, just unavailable).
- [ ] TLS terminated in front of aisotropyd (proxy/load balancer), or
  `AISOTROPY_REQUIRE_TLS=true` when the binary terminates TLS itself — bearer credentials
  must not travel in plaintext.
- [ ] Consider `AISOTROPY_JWT_AUDIENCE` / `AISOTROPY_JWT_ISSUER` (they apply to **any** JWT
  mode) when the token is shared with other services, and `AISOTROPY_JWT_MAX_TTL` to bound
  token lifetime.

## 7. Output-safety and model-routing knobs (ADR-0028 / ADR-0029 / ADR-0030)

Guardrails run on every chat request — input redaction + injection/output **detection are
ALWAYS on** — but **blocking is opt-in and default-off** (annotate + log). The model-selection
gateway is likewise a pass-through until configured. The operationally-relevant knobs, all
default-off:

| Variable / signal | Effect | Operational note | Code pointer |
|---|---|---|---|
| `AISOTROPY_GUARDRAILS_BLOCK_INJECTION` | Truthy → a suspicious **input** injection verdict **rejects** the request (400 on `chat`/`chat/agent`, SSE `error` on `chat/stream`) instead of annotating. | Heuristic detector — expect false positives. Default off: redaction + detection still run, nothing is blocked. | `aisotropy/app.go` (`guardrailsBlockFromEnv`), `internal/api/guard.go` |
| `AISOTROPY_GUARDRAILS_BLOCK_OUTPUT` | Truthy → a flagged (toxicity/leak) reply is replaced with a safe refusal on **both** chat routes. On `chat/stream` this forces the response to be **BUFFERED** (no live token flush) so it can be inspected before any byte is sent. | **Latency trade-off (ADR-0029):** buffering sacrifices time-to-first-token on `chat/stream` — the client waits for the full reply. Non-streaming `chat` has no such cost. Default off → `chat/stream` streams live and only annotates. | `aisotropy/app.go`, `internal/api/guard.go`, `internal/api/chat_stream.go` |
| `AISOTROPY_GUARDRAILS_LLM_JUDGE` | Truthy (and an LLM client set) → attaches an **LLM-as-a-judge** Safety check to the output guardrail (flags a reply scoring `< 0.5` safety). | **Adds a model call per response** (cost + latency). Driven by the **raw** LLM client, not the circuit-broken chat client, so a flaky judge can't trip the chat breaker. Its error posture tracks `BLOCK_OUTPUT` (fail-open when annotating, fail-closed when hard-blocking). Construction failure → falls back to built-in heuristics. Default off. | `aisotropy/app.go`, `internal/api/eval_output_check.go`, `internal/eval` |
| `AISOTROPY_COST_BUDGET_USD` | A positive USD budget enables gateway cost-budget shaping — an over-budget tenant is routed to the cheap target. | **Requires `AISOTROPY_CHEAP_MODEL`** too, else skipped (pass-through). An explicit per-request `model_preference` overrides this rule, as does an image-bearing turn (the `ImageRule` is chained ahead of the cost rule). | `aisotropy/app.go` (`costBudgetFromEnv`, `buildModelGateway`) |
| `AISOTROPY_VISION_MODEL` | The gateway target **name** an image-bearing turn is routed to via the `ImageRule` (ADR-0030): a `chat`/`chat/stream` request carrying `images` sets `HasImage` and is routed here, ahead of cost shaping. | Must name a **registered** client (this runtime: `primary` or `cheap`); an unregistered name resolves safely to the default. **Routing signal only** — the text-only clients do **not** send image bytes to the model yet (multimodal is a follow-on). Unset → images route to `primary` (unchanged). | `aisotropy/app.go` (`visionModelFromEnv`, `buildModelGateway`), `internal/gateway/policies.go` (`ImageRule`) |
| `model_preference` (request body field, not env) | An **optional** `ChatRequest` field naming a gateway target (e.g. `"cheap"`) the caller wants for that turn (ADR-0029). Wins over cost-budget routing. | A routing **preference, not authorization** — an unknown name falls back to the default, and quota-pressure degradation still overrides it. Additive/`omitempty`, so existing clients are unaffected. | `internal/api/chat.go` (`ChatRequest.ModelPreference`), `internal/api/guard.go` (`selectClient`) |
| `images` (request body field, not env) | An **optional** `ChatRequest` `[]string` presence signal that the turn carries image content (ADR-0030). Non-empty → routes to `AISOTROPY_VISION_MODEL` via the `ImageRule`. | **Routing signal only** — image bytes are neither embedded, persisted, nor forwarded to the model; only their presence is counted. Additive/`omitempty`, so existing clients are unaffected. | `internal/api/chat.go` (`ChatRequest.Images`), `internal/api/guard.go` (`selectClient`) |

The complete env-var table (all levers, not just the ops-critical subset) remains in
[runtime-as-built.md](../architecture/runtime-as-built.md) → "Environment variables (as
consumed by the code)".

### The stateless `/ask` route and its response cache (ADR-0030)

`POST /v1/tenant/{tenant_id}/user/{user_id}/ask` is a **stateless**, response-cached sibling of
`/chat`: no working-memory hydration, no `session_id`, no consolidation outbox — its only context
is tenant-scoped semantic RAG. On a cache **HIT** it returns the stored answer with **no LLM call**
(`AskOutput.cached = true`); on a **MISS** it builds RAG context, makes one LLM call, runs the same
output guardrail, caches the answer, and returns it. Operationally:

- **The cache is in-process and ephemeral.** `buildResponseCache` holds an exact-match LRU
  (4096 entries, 1 h TTL) plus, when an embedder is configured, a semantic near-match layer (cosine
  threshold 0.95, 1 h TTL, 512 entries per `(tenant, user)`). Nothing is persisted — a restart empties
  it (like every in-process tier, section 1), and it is **per-replica**, so N replicas hold N independent
  caches (no shared hit rate). There is no env var to size it today; the bounds are compiled-in.
- **Isolation is by construction.** Both cache layers key/partition by `(tenant, user)`, so a
  cross-tenant hit is structurally impossible — not a post-filter.
- **Same security posture as `/chat`.** `/ask` is a `/v1` route registered after the authn
  middleware, so it inherits the must-agree bind (401 missing/invalid, 403 tenant/user mismatch) and
  the input/output guardrails; the redacted prompt (never the raw one) is what becomes the cache key.
