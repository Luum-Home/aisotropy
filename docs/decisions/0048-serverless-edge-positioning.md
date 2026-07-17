---
type: Decision
title: "ADR-0048 — Serverless & edge positioning: a tiered, honest fit statement"
description: Records where aisotropyd truthfully runs on serverless/edge platforms and where it does not, and the state-externalization principle that makes it portable. Container-serverless (Cloud Run, Lambda-container+Web-Adapter, Azure Container Apps, Fly) is a strong fit; stateless FaaS works with a reduced in-process-memory advantage; V8-isolate/WASM edges (Workers-isolate, Vercel Edge, Deno Deploy, Fastly) do NOT run the runtime and are fronted as a service. The facts/semantic tiers are env-selectable to networked backends; working memory is the one local-coupled tier.
tags: [decision, adr, serverless, edge, deployment, positioning, cloud-run, lambda, cloudflare, state-externalization]
timestamp: 2026-07-17T00:00:00Z
---

# ADR-0048 — Serverless & edge positioning: a tiered, honest fit statement

## Status

✅ **accepted (2026-07-17)** — companion to the deployment guide
`docs/deployment/serverless-edge.md`. Follows the code-truth discipline of ADR-0020
(honest positioning with revisit triggers) and the doc-truth gate: every fit claim
is grounded in a file/ADR or a cited 2026 platform contract, and the one measured
proof (the `$PORT` gap) was executed, not asserted.

## Context

The operator wants aisotropy to be able to say, truthfully, that it "works well in
functions / serverless / any cloud provider / edge functions." The naive version of
that claim is false in one direction and undersold in another, so it needs a
**tiered** statement rather than a yes/no.

Two code-grounded facts frame the decision:

1. **The footprint is serverless-grade and measured.** A single **12.4 MiB** static
   distroless Go container, **8.8 MiB** idle RSS, **262 ms** median cold start
   (`bench/footprint/results.txt`, `RUNS=5`; `Dockerfile`, `arenas/BENCHMARKS.md
   §Class 3`). This is a genuine strength on any platform that accepts a container.

2. **The moat is in-process, tiered memory** — which is in tension with serverless
   statelessness, but only partly. Of the three tiers:
   - **Working memory** (BadgerDB L1, µs) is **always in-process** — no networked
     variant exists, by design (`aisotropy/app.go` `newWorkingMemory`).
   - **Facts** is **env-selectable to networked rqlite** via `RQLITE_URL`
     (`internal/facts/factory.go`).
   - **Semantic** is **env-selectable to networked Qdrant** via `QDRANT_HOST`
     (`internal/semantic/factory.go`).

So the runtime's **durable** state can be externalized; only the **working-memory
summary buffer** couples it to a local instance. That single fact is what sorts the
platforms into tiers.

Three secondary contracts also bear on a public serverless endpoint:
- The server resolves its listen address through the exported
  `aisotropy.ResolveListenAddr` with precedence `WithAddr > AISOTROPY_ADDR > $PORT
  (as ":"+PORT) > ":8080"` (`aisotropy/app.go`, used by both `app.Run` and
  `cmd/aisotropyd/main.go` as the single source; test `aisotropy/listenaddr_test.go`).
  It therefore **honors the `$PORT` variable** Knative/Cloud Run/Lambda-Web-Adapter
  inject, and the `$PORT` bind is `:<port>` = `0.0.0.0` (non-loopback), so the
  ADR-0042 NoAuth guard still applies.
- Secure-by-default auth: on a non-loopback bind, aisotropyd **refuses to boot**
  without a JWT authenticator unless NoAuth is explicitly opted into for non-local
  use (ADR-0042); optional TLS enforcement via `AISOTROPY_REQUIRE_TLS` (ADR-0025).
- A **stateless, response-cached `/ask`** endpoint exists (`internal/api/ask.go`,
  ADR-0030) that composes well with an edge cache.

## Options considered

**Option A — Claim broad "serverless + edge" support (rejected).** Simple marketing
line, but false: the full Go HTTP runtime does **not** run inside a V8 isolate or a
WASM edge (`GOOS=wasip1` has no sockets → the chi/Huma server deadlocks;
`docs/research/wasm-vs-docker-sandbox-2026-07-17.md` §2c/§2e). Claiming Cloudflare
Workers / Vercel Edge / Fastly would be an overclaim and a strawman (it also
conflates Cloudflare Workers-isolate with Cloudflare Containers, which are different
products).

**Option B — Claim only "runs in a container, deploy it yourself" (rejected as
undersold).** True but leaves real, differentiated capability on the table: the
memory tiers *are* externalizable, so aisotropy is genuinely a strong
container-serverless citizen (scale-to-zero, sub-300 ms cold start) — not merely
"a container you can run."

**Option C — Build a stateless WASM/edge subset of the runtime (rejected).** Would
require re-architecting the memory tiers as network-only and abandoning the
in-process L1 that is the entire thesis. High cost, breaks the moat, and duplicates
what "aisotropy behind the edge" already achieves. Not shipped, not planned.

**Option D — A tiered honesty matrix + the state-externalization principle
(accepted).** State exactly where it fits great, where with caveats, and where it
does not run — and front the edge as a service in the last case.

## Decision

Adopt a **three-tier fit statement**, recorded in `docs/deployment/serverless-edge.md`:

- **Tier 1 — strong fit (say it confidently): container-serverless on any cloud.**
  Google Cloud Run / Knative, AWS Lambda (**container image + AWS Lambda Web
  Adapter**, which runs the HTTP server unmodified and keeps the same image portable
  to Fargate/EC2/local), Azure Container Apps, Fly.io Machines. Ship the existing
  distroless image; externalize facts (`RQLITE_URL`) and semantic (`QDRANT_HOST`);
  configure JWT auth; scale to zero. Honest caveat: the working-memory buffer is
  per-instance.

- **Tier 2 — works with caveats: stateless FaaS.** AWS Lambda zip /
  `provided.al2023` custom runtime, Cloud Functions. Cold start per invoke, **all**
  durable state externalized, and the in-process µs-memory advantage largely
  neutralized (every memory read becomes a network hop). It runs and isolates
  correctly, but competes here as "a small fast Go function with good memory
  plumbing," not as "an in-process cognitive runtime." Prefer the Tier-1
  container+LWA path over a handler rewrite on AWS.

- **Tier 3 — does NOT run the full runtime: V8-isolate / WASM edges.** Cloudflare
  **Workers** (isolate), Vercel Edge/Fluid, Deno Deploy, Fastly Compute. Recommend
  **aisotropy-as-a-service behind the edge**: the edge function calls aisotropyd over
  HTTP; the stateless response-cached `/ask` fronts an edge cache cleanly. A stateless
  WASM subset is **not shipped** (Option C) and would break the moat. Note the split:
  Cloudflare **Containers** (GA 2026) is a **Tier-1** path — it runs the image at the
  edge directly.

**The state-externalization principle (the load-bearing rule):** *the networked
memory-tier backends make the runtime serverless-portable; working memory is the one
tier that stays local-coupled by design.* Point `RQLITE_URL`/`QDRANT_HOST` at
managed services and the durable memory survives any instance recycle; the
working-memory summary buffer does not, and a follow-up turn on a fresh instance
rehydrates from the externalized tiers rather than the warm buffer. A networked
working-memory tier is deliberately **not** offered — it would move the µs L1 across
the network and dissolve the reason it is L1.

**The honest edge boundary:** the full Go runtime does not run inside a V8/WASM edge
(no sockets under `wasip1`; stateless per-request WASM is incompatible with the
in-process tiers). aisotropy fronts the edge; it does not live in it.

## Consequences

**Easier / better:**
- A defensible one-line claim the product can use without overclaiming (see the
  deployment guide's headline).
- A concrete, per-platform deployment recipe for Tier 1, with the auth/TLS/port
  contract spelled out.
- The Tier-3 "behind the edge" pattern turns a limitation into an architecture, and
  `/ask` + edge-cache is a real, shippable composition.

**Resolved (shipped):**
1. **`$PORT` honored — RESOLVED.** A gap surfaced during this work (a native build
   run twice: `PORT=8080` bound `:8080` and served `/healthz`/`/health` 200, but
   `PORT=8090` was ignored and still bound `:8080`) is now **fixed and shipped**.
   `aisotropy/app.go` exports `ResolveListenAddr(optAddr)` with precedence `WithAddr
   > AISOTROPY_ADDR > $PORT (as ":"+PORT) > ":8080"`, used as the single source by
   both `app.Run` and `cmd/aisotropyd/main.go` (test `aisotropy/listenaddr_test.go`,
   `TestResolveListenAddr`, green). aisotropyd now binds the platform-injected
   `$PORT` with no extra config; the `$PORT` bind stays non-loopback, so the ADR-0042
   NoAuth guard is unchanged (a JWT authenticator or the explicit NoAuth override is
   still required on a public endpoint).

**Harder / residual cost — three deployment-config items, no code change:**
2. **Readiness probe.** Wire the platform startup/readiness probe to `/health`
   (real dependency probe), keep `/healthz` for liveness — deployment-config
   recommendation, no code change.
3. **Cold-start tuning.** 262 ms median / 501 ms max; use `min-instances=1` for
   latency-tight or conversational workloads — a cost/latency dial.
4. **Scale-to-zero vs local working memory.** Inherent to the L1 design; mitigations
   are accept-and-rehydrate, keep one warm instance, or (Fly only) pin
   `AISOTROPY_DATA_DIR` to a Machine volume (encrypted, ADR-0044).

**Revisit triggers** (per ADR-0020 discipline): a networked
working-memory requirement appears from a genuinely stateless deployment (would force
re-examining the "L1 stays local" invariant — expected answer: still no, front the
edge instead); or `GOOS=wasip3` ships sockets and mainline Go gains a viable edge
target (would reopen the Tier-3 boundary, tracked against
`docs/research/wasm-vs-docker-sandbox-2026-07-17.md`).

## References

`docs/deployment/serverless-edge.md` (the operational guide this ADR ratifies),
`bench/footprint/results.txt`, `arenas/BENCHMARKS.md`, `Dockerfile`,
`aisotropy/app.go`, `cmd/aisotropyd/main.go`, `internal/facts/factory.go`,
`internal/semantic/factory.go`, `internal/api/ask.go`,
`docs/decisions/0025-security-hardening-authn-ops.md`,
`docs/decisions/0030-jwks-rotation-image-signal-stateless-ask.md`,
`docs/decisions/0042-noauth-explicit-optin.md`,
`docs/decisions/0044-working-memory-at-rest-encryption.md`,
`docs/research/wasm-vs-docker-sandbox-2026-07-17.md`. Platform contracts cited inline
in the deployment guide.
