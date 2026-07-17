---
type: Concept
title: Serverless & edge deployment — a tiered honesty matrix
description: Where aisotropyd runs well, where it runs with caveats, and where it does NOT run — grounded in the code (footprint, listen contract, state model) and in each platform's own 2026 runtime contract. The runtime is a small static Go container with env-selectable networked memory tiers, so it is a strong fit for container-serverless on any cloud; a workable fit for stateless FaaS with all state externalized; and NOT a fit for V8-isolate / WASM edges, which it fronts as a service instead.
tags: [deployment, serverless, edge, cloud-run, knative, lambda, azure-container-apps, fly, cloudflare, vercel, fastly, faas, wasm, cold-start, scale-to-zero]
timestamp: 2026-07-17T00:00:00Z
---

# Serverless & edge deployment — a tiered honesty matrix

> **What this document lets aisotropy claim, truthfully.** aisotropyd is a single
> **12.4 MiB static distroless Go container** with an **8.8 MiB idle RSS** and a
> **262 ms median cold start** (`bench/footprint/results.txt`, measured, `RUNS=5`).
> That footprint is a genuinely strong serverless story. The tension is the moat:
> the tiered-memory advantage is **in-process by design** (BadgerDB working memory
> on local disk, µs latency), and serverless is ephemeral. The reconciliation —
> proven in code below — is that the **facts and semantic tiers are env-selectable
> to networked backends** (`RQLITE_URL`, `QDRANT_HOST`), so the runtime's state can
> be externalized and it becomes portable across serverless platforms. **One tier
> stays local: BadgerDB working memory.** This guide states exactly where that
> matters and where it does not.

## The one-line headline

> **aisotropy ships as a small, fast static container that runs on any
> container-serverless platform — Google Cloud Run, AWS Lambda (container +
> Web Adapter), Azure Container Apps, Fly.io — with its memory tiers pointed at
> networked backends; at the V8/WASM edge it runs *behind* the edge as a service,
> not inside it.**

Do **not** shorten this to "runs on serverless and edge." It does not run *inside*
a V8-isolate or WASM edge (Cloudflare Workers-isolate, Vercel Edge, Deno Deploy,
Fastly Compute) — see Tier 3. Overclaiming there is the one thing this document
exists to prevent.

---

## The two facts that decide every tier

### Fact 1 — the footprint is serverless-grade (measured)

| Metric | aisotropy | Source |
|---|---|---|
| Image (distroless static) | **12.4 MiB** | `bench/footprint/results.txt` |
| Idle RSS | **8.8 MiB** | same |
| Cold start (median / min / max, `RUNS=5`) | **262 / 215 / 501 ms** | same |
| Packaging | `CGO_ENABLED=0` static binary on `distroless/static-debian12:nonroot` | `Dockerfile` |

A sub-300 ms cold start and a 12 MiB image are competitive with, and often better
than, the Python/TypeScript agent runtimes in the same benchmark class (langgraph
61 MiB / 583 ms; mastra 126 MiB / 514 ms — `arenas/BENCHMARKS.md §Class 3`). This
is the part of the story that is unambiguously true everywhere.

### Fact 2 — the state model is the tension, and it is externalizable (code-truth)

aisotropy has three memory tiers with different portability:

| Tier | Default backend (local) | Networked backend (env-selectable) | Externalizable? | Source |
|---|---|---|---|---|
| **Working memory** (L1, live chat + summary buffer) | BadgerDB, in-mem or on-disk (`AISOTROPY_DATA_DIR`) | **none** — always in-process | **No** — stays local | `aisotropy/app.go` `newWorkingMemory`; `.claude/rules/architecture.md` |
| **Facts** (rules/tasks journal, FTS5) | embedded SQLite | **rqlite** via `RQLITE_URL` | **Yes** | `internal/facts/factory.go` |
| **Semantic** (vectors, RAG) | in-process cosine | **Qdrant** via `QDRANT_HOST` | **Yes** | `internal/semantic/factory.go` |

`internal/facts/factory.go` selects the networked `RqliteStore` the moment
`RQLITE_URL` is set; `internal/semantic/factory.go` selects the gRPC `QdrantStore`
the moment `QDRANT_HOST` is set. Both fall back to an in-process default when unset.
**Working memory has no networked variant** — it is BadgerDB, embedded, by design
(the µs-latency L1 of the "memory hierarchy by access temperature" model). This is
the single tier that couples the runtime to a local disk, and every caveat below
traces back to it.

**The state-externalization principle:** point `RQLITE_URL` and `QDRANT_HOST` at
managed/networked services and aisotropy's durable memory survives any
instance recycle, cold start, or scale-to-zero. What does **not** survive is the
**working-memory summary buffer** for an in-flight conversation when the instance
that held it is reclaimed. On a stateless platform, a follow-up turn that lands on
a fresh instance rebuilds working memory from the externalized tiers (facts +
semantic RAG) rather than from the live buffer — correct, but with a warm-context
gap. This is the honest tradeoff, not a bug.

### Fact 3 — the listen & auth contract (matters for a public endpoint)

- **Listen:** the server resolves its address through the exported
  `aisotropy.ResolveListenAddr` with precedence `WithAddr > AISOTROPY_ADDR > $PORT
  (as ":"+PORT) > ":8080"` (`aisotropy/app.go`; the single source used by both
  `app.Run` and `cmd/aisotropyd/main.go`; test `aisotropy/listenaddr_test.go`). So
  it **honors the `$PORT` variable** that Knative/Cloud Run/Lambda-Web-Adapter
  inject, and the default `:8080` bind is `0.0.0.0:8080` — all interfaces, which
  satisfies the "listen on 0.0.0.0, not 127.0.0.1" rule Cloud Run and others require.
  The `$PORT` bind is `:<port>` = non-loopback, so a JWT authenticator (or the
  explicit NoAuth override) is required — consistent with ADR-0042 below.
- **Auth (secure by default):** on a **non-loopback** bind, aisotropyd **refuses to
  boot** unless either a JWT authenticator is configured (`AISOTROPY_JWT_SECRET` /
  `AISOTROPY_JWT_PUBLIC_KEY[_FILE]` / `AISOTROPY_JWT_JWKS_URL`) **or** the operator
  explicitly sets `AISOTROPY_ALLOW_NOAUTH=1` **and** `AISOTROPY_ALLOW_NOAUTH_NONLOCAL=1`
  (ADR-0042, `cmd/aisotropyd/main.go` `resolveAuthBoot`). For any public serverless
  URL, configure a JWT authenticator — a public endpoint that trusts the path
  `tenant_id` with no credential is an isolation hole. Optionally set
  `AISOTROPY_REQUIRE_TLS=1` when the platform does **not** terminate TLS upstream
  (ADR-0025).

---

## The tiered honesty matrix

| Tier | Platforms | Runs the full runtime? | Confidence | The honest caveat |
|---|---|---|---|---|
| **1 — Strong fit** | Cloud Run / Knative, AWS Lambda (container + Web Adapter), Azure Container Apps, Fly.io Machines | **Yes** | Say it confidently | Working memory is per-instance; externalize facts+semantic. Honor-`$PORT` gap (default 8080 works today). |
| **2 — Works with caveats** | AWS Lambda (zip / `provided.al2023` custom runtime), Cloud Functions | **Yes, per-invoke** | Say it with the tradeoff | Cold start per invocation; **all** durable state externalized; the in-process µs-memory advantage is largely neutralized. |
| **3 — Does NOT run the full runtime** | Cloudflare Workers (V8 isolate), Vercel (Edge/Fluid), Deno Deploy, Fastly Compute | **No** | Do not claim it | Front aisotropyd **behind** the edge as a service. A stateless WASM subset is not shipped (and would break the moat). |

> **Cloudflare is in two rows on purpose.** Cloudflare **Workers** (V8 isolates) is
> Tier 3 — it cannot run a Go server binary. Cloudflare **Containers** (GA in 2026)
> is Tier 1 — it runs standard Docker images at the edge. These are different
> products; conflating them is the classic strawman this document refuses to make.

---

## Tier 1 — Strong fit: container-serverless on any cloud

The recipe is the same everywhere: ship the existing distroless image, externalize
the facts and semantic tiers, configure auth, let the platform scale to zero.

### Shared prerequisites (all Tier-1 platforms)

```bash
# Durable memory — point the two externalizable tiers at managed/networked services
RQLITE_URL=http://<rqlite-host>:4001         # facts tier  -> networked (internal/facts/factory.go)
QDRANT_HOST=<qdrant-host>                     # semantic tier -> gRPC     (internal/semantic/factory.go)
QDRANT_PORT=6334                              # default; QDRANT_API_KEY / QDRANT_USE_TLS as needed

# Auth (required on a public endpoint; ADR-0042/0025)
AISOTROPY_JWT_SECRET=<hs256-secret>           # or AISOTROPY_JWT_PUBLIC_KEY_FILE / AISOTROPY_JWT_JWKS_URL
# AISOTROPY_REQUIRE_TLS=1                      # only if the platform does NOT terminate TLS upstream

# LLM provider
ANTHROPIC_API_KEY=<key>                       # unset -> the keyless stub client (won't answer for real)

# Leave working memory in-process (do NOT set AISOTROPY_DATA_DIR on an ephemeral FS —
# it would write BadgerDB to a tmpfs that vanishes and costs RAM). In-mem default is correct here.
```

Health probes (both outside the OpenAPI contract, `aisotropy/app.go`):
`GET /healthz` (liveness, always 200 `{"status":"ok"}`) and `GET /health`
(readiness — runs cheap `_health`-tenant probes against facts + semantic, 200 when
all respond, 503 when any is degraded — so once rqlite/Qdrant are wired the same
probe becomes a real dependency readiness check).

### Google Cloud Run / Knative

Cloud Run runs **any container image / arbitrary binary**; it injects a `PORT` env
var (default **8080**, configurable) and requires the container to listen on
**`0.0.0.0:$PORT`, not `127.0.0.1`**
([container contract](https://docs.cloud.google.com/run/docs/container-contract)).

- aisotropy **honors `$PORT`** via `ResolveListenAddr` (see [`$PORT` support
  (shipped)](#port-honored)), and it binds all interfaces (`0.0.0.0`), so it
  satisfies the contract at **any** port Cloud Run assigns — no need to pin the
  container port to 8080.
- The default `:8080` also matches Cloud Run's default `PORT`, so it works with or
  without an explicit port set in the service config.

```bash
gcloud run deploy aisotropyd \
  --image=<registry>/aisotropyd@sha256:<digest> \
  --port=8080 \                         # optional — aisotropy honors Cloud Run's injected $PORT at any value
  --min-instances=0 --max-instances=N \ # scale to zero
  --set-env-vars=RQLITE_URL=...,QDRANT_HOST=...,AISOTROPY_JWT_SECRET=...,ANTHROPIC_API_KEY=... \
  --allow-unauthenticated               # platform-level; aisotropy still enforces its own JWT
```

Notes: Cloud Run's in-instance filesystem is **in-memory and ephemeral** (counts
against the memory limit), so never point `AISOTROPY_DATA_DIR` at it. TLS is
terminated by Cloud Run upstream, so leave `AISOTROPY_REQUIRE_TLS` **off** (the
request arrives as plain HTTP with `r.TLS == nil`). Scale-to-zero cold start is the
262 ms class plus the platform's own container-scheduling latency.

### AWS Lambda (container image + Lambda Web Adapter) — Tier 1

Lambda runs the runtime **unmodified** by adding the **AWS Lambda Web Adapter**
(LWA) as a Lambda Extension: LWA boots first, sends a readiness `GET` to
`http://127.0.0.1:8080/`, then translates Lambda invocation events ⇄ HTTP against
your server. The **same image runs on Lambda, Fargate, EC2, and locally**
([aws-lambda-web-adapter](https://github.com/awslabs/aws-lambda-web-adapter)).

```dockerfile
# Add one line to the existing distroless image (or a Lambda base image):
COPY --from=public.ecr.aws/awsguru/aws-lambda-adapter:0.9.1 /lambda-adapter /opt/extensions/lambda-adapter
# LWA defaults to app on 127.0.0.1:8080 — but aisotropy binds 0.0.0.0:8080 by default,
# which also serves 127.0.0.1, so LWA's readiness probe succeeds.
```

Port note: LWA probes `127.0.0.1:8080` by default, which aisotropy's `0.0.0.0:8080`
bind covers out of the box. If you bind a different port (via `AISOTROPY_ADDR` or the
injected `$PORT`, both honored by `ResolveListenAddr`), set `AWS_LWA_PORT` to the same
value so LWA's readiness probe targets the bound port. Lambda's `/tmp` is ephemeral and
frozen between invocations; working memory persists only across **warm** reuse of
one execution environment, not across cold starts. 15-minute max timeout bounds any
long SSE stream. Externalize facts+semantic exactly as above.

### Azure Container Apps

Runs any container image; you declare `targetPort` (set it to **8080**), and the
**Consumption** workload profile scales to zero on an HTTP scale rule (CPU/memory
rules cannot reach zero, and a scale rule is required or the app cannot restart from
zero) ([scaling](https://learn.microsoft.com/en-us/azure/container-apps/scale-app)).

```yaml
# containerApp (excerpt)
properties:
  configuration:
    ingress: { external: true, targetPort: 8080 }   # match aisotropy's :8080
  template:
    scale: { minReplicas: 0, maxReplicas: N, rules: [ { name: http, http: { metadata: { concurrentRequests: "50" } } } ] }
    containers:
      - image: <registry>/aisotropyd@sha256:<digest>
        env: [ { name: RQLITE_URL, value: ... }, { name: QDRANT_HOST, value: ... }, { name: AISOTROPY_JWT_SECRET, secretRef: ... } ]
```

### Fly.io Machines

Fly turns the Docker image into a **Firecracker micro-VM**; scale-to-zero is
**application-driven** — the process exits when idle and Fly stops the Machine,
restarting it on the next request ([Fly Machines](https://fly.io/blog/fly-machines/)).
Set `internal_port = 8080` in `fly.toml` to match aisotropy's bind. Fly gives you a
real (if small) ephemeral disk per Machine, and a Fly **volume** can host
`AISOTROPY_DATA_DIR` if you want on-disk (encrypted, ADR-0044) working memory that
survives a restart on the same Machine — the one Tier-1 spot where local working
memory can be made durable, at the cost of pinning to a Machine.

---

## Tier 2 — Works with caveats: stateless FaaS

**Platforms:** AWS Lambda as a **zip / `provided.al2023` custom runtime** (the Go
binary rewritten to the `aws-lambda-go` handler instead of running as an HTTP
server), Google Cloud Functions.

This runs, but it changes the shape of the product:

- **Cold start per invocation.** Every invoke may pay the ~262 ms class (plus
  platform overhead) unless the environment is warm. `provided.al2023` is a <40 MiB
  OS-only base with a fast Go start, so aisotropy's small binary is a good citizen
  here ([provided.al2023](https://aws.amazon.com/blogs/compute/introducing-the-amazon-linux-2023-runtime-for-aws-lambda/)).
- **All durable state externalized, always.** There is no persistent local disk and
  no reliable warm reuse to lean on: `RQLITE_URL` and `QDRANT_HOST` are **mandatory**,
  and the BadgerDB working-memory summary buffer is effectively **per-invocation**.
- **The in-process µs-memory advantage is largely neutralized.** The whole thesis of
  the tiered memory is that L1 working memory is local and µs-fast; a per-invocation
  FaaS turns every memory read into a network hop to rqlite/Qdrant. aisotropy still
  *works* and still *isolates by tenant*, but on this tier it competes as "a small
  fast Go function with good memory plumbing," not as "an in-process cognitive
  runtime." Be honest about which claim you are making on Tier 2.

**Recommendation:** prefer the Tier-1 **container + Lambda Web Adapter** path over
the Tier-2 handler-rewrite path on AWS — LWA keeps the HTTP server (SSE streaming,
the full route surface) intact and the same image portable, where the zip/handler
path forfeits streaming semantics and forks the entry point.

---

## Tier 3 — Does NOT run the full runtime: V8-isolate / WASM edges

**Platforms and why each cannot host aisotropyd:**

| Platform | Runtime model | Why the Go runtime does not run |
|---|---|---|
| **Cloudflare Workers** | V8 isolates, JS/WASM only, stateless, no persistent TCP, single-digit MB | Cannot execute a Go server binary; isolates are not containers ([how Workers works](https://developers.cloudflare.com/workers/reference/how-workers-works/)) |
| **Vercel (Edge / Fluid)** | Edge deprecated 2025, folded into Fluid; Fluid runs Node.js/Python (no first-class Go); Go only via compile-to-WASM | No native Go binary runtime ([Fluid compute](https://vercel.com/docs/fluid-compute), [Wasm](https://vercel.com/docs/functions/runtimes/wasm)) |
| **Deno Deploy** | V8, JS/TS/WASM only | Cannot run a Go binary ([Deno Deploy](https://deno.com/blog/fastest-git-deploys-to-the-edge)) |
| **Fastly Compute** | **WASM-only** (WASI per request) | Go-to-WASM compiles, but the aisotropy **HTTP server** cannot: `GOOS=wasip1` has **no sockets**, so a chi/Huma `net.Listen` deadlocks, and the in-process BadgerDB tiers are incompatible with a stateless per-request WASM model ([Fastly Compute](https://docs.fastly.com/products/compute); see `docs/research/wasm-vs-docker-sandbox-2026-07-17.md` §2c, §2e) |

**The honest boundary** (reused from `docs/research/wasm-vs-docker-sandbox-2026-07-17.md`):
a full Go HTTP server does not run under `GOOS=wasip1` (no sockets → deadlock), and
forcing the runtime into a stateless per-request WASM model **breaks the
tiered-memory moat** (BadgerDB is embedded, not a network service). A stateless WASM
subset of aisotropy is **not shipped**, and building one would trade away the exact
in-process advantage the product is built on. Do not claim edge-WASM support.

### The recommended pattern: aisotropy *behind* the edge

Run aisotropyd as a Tier-1 container service and let the edge function call it over
HTTP. The edge keeps what it is good at (global POP termination, caching, auth at
the door); aisotropy keeps what it is good at (stateful, isolated, in-process
memory).

```
Client → Cloudflare Worker / Vercel Edge / Fastly Compute  (V8/WASM, global, cached)
             │  fetch() over HTTPS
             ▼
         aisotropyd on Cloud Run / Lambda-LWA / ACA / Fly  (Tier 1, stateful)
             │
             ▼  RQLITE_URL / QDRANT_HOST  (networked memory tiers)
```

**`/ask` fronts an edge cache well.** aisotropy ships a **stateless,
response-cached** `POST .../ask` endpoint (`internal/api/ask.go`, ADR-0030): it
consults a per-`(tenant,user)` response cache and returns `{"cached": true}` on a
hit without an LLM call. Because a hit is a pure function of the request, an edge
cache (Cloudflare Cache API, Vercel edge cache, Fastly) can cache the `/ask`
response by request key and serve the common questions from the POP entirely — the
edge and the stateless endpoint compose cleanly. The stateful `/chat` and
`/chat/stream` routes stay on the origin (they mutate working memory and stream).

For Cloudflare specifically, the **native** path is **Cloudflare Containers**
(Tier 1) — run the aisotropyd image at the edge directly, with a thin Worker in
front for routing/cache ([Cloudflare Containers](https://developers.cloudflare.com/containers/)).

---

## `$PORT` support (shipped) and remaining deployment-config items

### `$PORT` honored — shipped {#port-honored}

aisotropyd resolves its listen address through the exported
`aisotropy.ResolveListenAddr(optAddr)` with precedence:

```
explicit WithAddr(...)  >  AISOTROPY_ADDR  >  $PORT (as ":"+PORT)  >  :8080
```

Both `app.Run` and `cmd/aisotropyd/main.go` use this single source
(`aisotropy/app.go`; test `aisotropy/listenaddr_test.go`, `TestResolveListenAddr`).
So aisotropyd binds the port injected by Knative/Cloud Run/Lambda-Web-Adapter with
no extra config, at **any** value the platform assigns — not just 8080. `AISOTROPY_ADDR`
remains the explicit override when set (it wins over `$PORT`), so no existing
deployment changes behavior.

Security note: a `$PORT`-derived bind is `:<port>` = `0.0.0.0` (non-loopback), so the
ADR-0042 NoAuth non-local guard still fires — a JWT authenticator (or the explicit
`AISOTROPY_ALLOW_NOAUTH=1` + `AISOTROPY_ALLOW_NOAUTH_NONLOCAL=1` override) is required
on a public endpoint. Honoring `$PORT` does not widen the insecure surface.

### Remaining deployment-config items (no code change)

### Item — readiness semantics for scale-to-zero

`GET /health` already runs real facts+semantic probes (200/503). For Cloud Run and
ACA, wire the platform's **startup/readiness probe to `/health`** (not `/healthz`)
so an instance is not sent traffic before its externalized tiers are reachable.
No code change needed — a deployment-config recommendation. (`/healthz` remains the
liveness probe.)

### Item — cold-start tuning

262 ms median is already good, but the max was 501 ms (`RUNS=5`). For latency-tight
Tier-1 use, prefer `min-instances=1` (Cloud Run) / `minReplicas=1` (ACA) to keep one
warm instance and pay scale-to-zero only off-peak — a cost/latency dial, documented,
not a code change.

### Item — scale-to-zero vs local working memory

The BadgerDB working-memory buffer for an in-flight conversation is lost when its
instance is reclaimed at scale-to-zero. This is **inherent** to the in-process L1
design (not a fixable bug). Mitigations, in order of preference: (a) accept it —
facts+semantic rehydrate the next turn correctly, just without the warm buffer;
(b) keep `min-instances=1` for conversational workloads; (c) on Fly, pin working
memory to a Machine volume (`AISOTROPY_DATA_DIR` on a Fly volume, encrypted per
ADR-0044). A networked working-memory tier is deliberately **not** offered — it
would move the µs L1 across the network and dissolve the reason it is L1.

---

## What to say, and what not to say

**Say (true):**
- "Runs as a small, fast static container on any container-serverless platform —
  Cloud Run, AWS Lambda (container + Web Adapter), Azure Container Apps, Fly — with
  its facts and semantic memory pointed at networked backends."
- "12 MiB image, sub-300 ms cold start, scales to zero."
- "Fronts a V8/WASM edge as a service; its stateless `/ask` endpoint caches cleanly
  at the edge."

**Do not say (overclaim):**
- "Runs on edge functions" / "runs on Cloudflare Workers / Vercel Edge / Fastly" —
  it runs *behind* those, not *inside* them.
- "Fully stateless / serverless-native" — working memory is a local in-process tier;
  on stateless FaaS the in-process memory advantage is reduced.
- "Fully stateless" of the whole runtime — the working-memory L1 is in-process by
  design; `$PORT` and the externalized tiers make it *portable*, not *stateless*.

---

## References

**Code / ADRs (aisotropy):** `bench/footprint/results.txt`, `arenas/BENCHMARKS.md`,
`Dockerfile`, `aisotropy/app.go` (`Run`, `startRuntime`, `newWorkingMemory`),
`aisotropy/app.go` (`ResolveListenAddr`), `aisotropy/listenaddr_test.go`,
`cmd/aisotropyd/main.go` (`resolveAuthBoot`), `internal/facts/factory.go`,
`internal/semantic/factory.go`, `internal/api/ask.go`,
`docs/decisions/0042-noauth-explicit-optin.md`,
`docs/decisions/0025-security-hardening-authn-ops.md`,
`docs/decisions/0044-working-memory-at-rest-encryption.md`,
`docs/decisions/0048-serverless-edge-positioning.md`,
`docs/research/wasm-vs-docker-sandbox-2026-07-17.md`.

**Platform docs (2026, cited inline):**
[Cloud Run container contract](https://docs.cloud.google.com/run/docs/container-contract) ·
[Knative container contract](https://docs.cloud.google.com/kubernetes-engine/enterprise/knative-serving/docs/reference/container-contract) ·
[AWS Lambda Web Adapter](https://github.com/awslabs/aws-lambda-web-adapter) ·
[Lambda Go container images](https://docs.aws.amazon.com/lambda/latest/dg/go-image.html) ·
[provided.al2023 runtime](https://aws.amazon.com/blogs/compute/introducing-the-amazon-linux-2023-runtime-for-aws-lambda/) ·
[Azure Container Apps scaling](https://learn.microsoft.com/en-us/azure/container-apps/scale-app) ·
[Azure Container Apps target port](https://learn.microsoft.com/en-us/azure/container-apps/troubleshoot-target-port-settings) ·
[Fly Machines](https://fly.io/blog/fly-machines/) ·
[Cloudflare Containers](https://developers.cloudflare.com/containers/) ·
[Cloudflare Workers model](https://developers.cloudflare.com/workers/reference/how-workers-works/) ·
[Vercel Fluid compute](https://vercel.com/docs/fluid-compute) ·
[Vercel Wasm](https://vercel.com/docs/functions/runtimes/wasm) ·
[Deno Deploy](https://deno.com/blog/fastest-git-deploys-to-the-edge) ·
[Fastly Compute](https://docs.fastly.com/products/compute).
