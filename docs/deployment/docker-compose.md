---
type: Guide
title: Reference deployment — docker-compose (the tiered-memory moat, live)
description: The docker compose reference stack that boots aisotropyd with its networked memory tiers ACTIVE (rqlite facts + Qdrant semantic), instead of the in-process fallbacks — ADR-0016 lever #3. Includes the sleep-cycle opt-in overlay and how to verify the moat is live.
tags: [deployment, docker, compose, qdrant, rqlite, sleep-cycle, memory-tiers, adr-0016]
timestamp: 2026-07-09T00:00:00Z
---

# Reference deployment — docker-compose

The bare `aisotropyd` binary boots with **in-process fallbacks** for the facts and
semantic tiers (SQLite+FTS5 in-process, in-memory cosine) — convenient for `go run`,
but not the deployment the project's differentiation is measured against. The
[competitive analysis](../research/competitive-landscape.md) calls the
**temperature-tiered memory** (working → facts/FTS5 → semantic vectors → sleep-cycle)
the perishable part of the moat; this compose stack is where that moat actually
**runs**: the same runtime code, but the facts and semantic tiers are real networked
services. This is [ADR-0016](../decisions/0016-dual-surface-sealed-runtime-composition-sdk.md)
lever #3.

## What it runs

| Service | Role | Image | License |
|---|---|---|---|
| `qdrant` | semantic memory (vectors, HNSW, per-tenant payload filter) | `qdrant/qdrant:v1.12.4` | Apache-2.0 |
| `rqlite` | facts memory (SQLite + FTS5 over Raft) | `rqlite/rqlite:8.36.3` | MIT |
| `aisotropyd` | the runtime, wired to both | built from `Dockerfile` | MIT |

The runtime reads its tier selection from env (unchanged contract): `RQLITE_URL`
points the facts tier at rqlite; `QDRANT_HOST`/`QDRANT_PORT` point the semantic tier
at Qdrant. With those set, `internal/facts` and `internal/semantic` use the networked
backends instead of their in-process defaults — no code change (see
[services-config](../../.claude/rules/services-config.md)).

## Quick start

```bash
# From the repo root. Set an LLM key first (optional — unset uses the keyless stub).
export OPENROUTER_API_KEY=sk-or-...        # or OPENAI_API_KEY / ANTHROPIC_API_KEY

docker compose up --build                  # boots qdrant + rqlite + aisotropyd
```

## Verify the moat is live

`GET /health` runs cheap, read-only readiness probes against the facts and semantic
tiers. With the fallbacks they hit the in-process stores; **here they become real
network readiness checks** against rqlite and Qdrant — with no code change. A green
`/health` is the proof the moat is wired end to end:

```bash
curl -s localhost:8080/healthz | jq        # liveness  -> {"status":"ok"}
curl -s localhost:8080/health  | jq        # readiness -> {"status":"ok","checks":[
                                           #   {"name":"facts","ok":true},       <- rqlite
                                           #   {"name":"semantic","ok":true}]}   <- Qdrant
```

If a tier is down, `/health` returns HTTP 503 and names the degraded check — the
same probe path that becomes a production readiness gate once these services are
deployed for real.

## Persistence

- **rqlite** (facts) and **Qdrant** (semantic) are volume-backed (`rqlite-data`,
  `qdrant-data`), so the persistent moat survives `docker compose down` (use
  `down -v` to wipe).
- **Working memory** (BadgerDB) runs in-memory here by design — it is the "RAM"
  tier, and the runtime's distroless image runs as a non-root user with no writable
  data dir. To also persist it, mount a writable volume and set `AISOTROPY_DATA_DIR`
  to it (the volume must be writable by UID 65532); it is intentionally left out of
  the default stack to keep the image non-root and shell-free.

## Startup ordering

`rqlite` has a `/readyz` healthcheck, so `aisotropyd` waits for it via
`depends_on: service_healthy`. Qdrant's image is distroless (no in-container tool
for a healthcheck), so it is gated by `service_started` plus `aisotropyd`'s
`restart: unless-stopped`: if Qdrant's gRPC port is not accepting connections at the
first boot, `aisotropyd` exits and Docker restarts it until Qdrant is ready. Expect
one or two restart lines in the log on a cold start; this is normal.

## Sleep-cycle (opt-in overlay)

The offline DataFusion consolidation ([ADR-0005](../decisions/0005-datafusion-go-rust-boundary.md))
is a **Rust subprocess** the runtime spawns, so it must live inside the aisotropyd
image. It is opt-in because it adds a full Rust/DataFusion build:

```bash
docker compose -f docker-compose.yml -f docker-compose.sleepcycle.yml up --build
```

The overlay rebuilds `aisotropyd` from [`deploy/Dockerfile.sleepcycle`](../../deploy/Dockerfile.sleepcycle)
— a multi-stage build that compiles `crates/sleep-cycle`, bundles the binary, and
sets `AISOTROPY_SLEEPCYCLE_BIN`. The base image switches from distroless/static to
`debian:bookworm-slim` because the Rust binary is dynamically linked against glibc.
Keep the root `Dockerfile` for production (minimal, distroless); this variant is for
the reference/dev stack. Expect the first build to take several minutes.

## Not included (honest scope)

- No reverse proxy / TLS termination — front with your own ingress for anything
  beyond local use.
- No Qdrant/rqlite clustering — both run single-node; scale Qdrant independently
  when needed (the only tier meant to, per the stack's golden rule).
- The stack is unauthenticated on localhost; do not expose the ports publicly
  without an auth layer.

## References

- [ADR-0016 — Dual surface](../decisions/0016-dual-surface-sealed-runtime-composition-sdk.md) (lever #3)
- [Distroless deployment](distroless.md) — the production single-binary image
- [Services configuration](../../.claude/rules/services-config.md) — the env contract
- [Competitive landscape](../research/competitive-landscape.md) — why the tiered moat matters
</content>
