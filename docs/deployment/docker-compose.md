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

## Tier authentication (local-only defaults)

The tiers are **not** open ports, even locally:

- **Qdrant** requires an API key — `QDRANT__SERVICE__API_KEY` on the service, and
  the same value on the aisotropyd client via `QDRANT_API_KEY`.
- **rqlite** requires HTTP auth — `rqlited` runs with `-auth` against
  [`deploy/rqlite/users.json`](../../deploy/rqlite/users.json) (the `aisotropy`
  user has full perms; the anonymous `*` user has only `ready`/`status` so the
  `/readyz` healthcheck works). aisotropyd embeds the credentials in `RQLITE_URL`
  (`http://aisotropy:<pw>@rqlite:4001`).

The shipped values are **LOCAL-ONLY placeholders** (`*-CHANGE-ME`). Override them
with a `.env` file (or shell env) for anything reachable — never reuse production
credentials here:

```bash
# .env (git-ignored) — local overrides
QDRANT_API_KEY=your-strong-key            # drives both the Qdrant service + client
RQLITE_PASSWORD=your-strong-password      # ALSO update deploy/rqlite/users.json to match
```

The Qdrant key flows to both sides from one `.env` var. The rqlite password is
paired with the users file, so change **both** together (the k8s/Helm surfaces
generate the file from the password, so they do not have this pairing).

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
                                           #   {"name":"facts","status":"ok","latency_ms":N},     <- rqlite
                                           #   {"name":"semantic","status":"ok","latency_ms":N}]} <- Qdrant
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

## Troubleshooting: rqlite stuck `unhealthy` / `/readyz` 503

**Symptom.** `docker ps` shows `rqlite` as `unhealthy`, `curl localhost:4001/readyz`
returns 503 indefinitely, the healthcheck's `FailingStreak` climbs without bound, and
the log repeats:

```
[raft] [WARN]  not part of stable configuration, aborting election
```

The facts tier is down; `GET /health` reports `degraded`.

**Cause.** rqlite derives its Raft node id from the container hostname unless told
otherwise, while the named volume persists the Raft configuration. Any container
recreate — `up --force-recreate`, an image bump, an edit to `docker-compose.yml` —
gives the node a new hostname, so the persisted configuration names a peer that no
longer exists and no election can ever succeed.

**Prevention (already applied).** The reference `docker-compose.yml` pins
`-node-id`, so the identity survives recreates. Measured under controlled
conditions on 2026-07-27, isolated project and volume: without the flag `/readyz`
after a recreate returns **503**; with it, **200** and the pre-recreate row still
readable.

**Repair, if you are already stuck.** The flag prevents the breakage; it does not
repair a volume that is already orphaned — also measured: adding `-node-id` to an
already-broken volume still returns 503. Two options:

```bash
# (a) Drop the Raft state. Fast, and the facts tier restarts EMPTY.
docker compose down && docker volume rm aisotropy_rqlite-data && docker compose up -d

# (b) Preserve the data: rqlite's documented peers.json recovery.
#     https://rqlite.io/docs/clustering/general-guidelines/  (not exercised here)
```

A single-node deployment loses nothing structural with (a) if the facts tier is
rebuildable from source documents; check before you drop it.

**Note on host-side clients.** This node deliberately does *not* set
`-http-adv-addr`: its client is the in-network `aisotropyd`, and one advertised
address cannot serve both in-network and host-side callers. Tools running on the
host (the `-tags manual` tests) need `localhost:<port>` advertised, which would
break `aisotropyd` — they use [`deploy/dormant-lane/docker-compose.yml`](dormant-lanes.md)
instead, which exists for exactly that reason.

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
  beyond local use ([reverse-proxy.md](reverse-proxy.md); the k8s prod overlay
  sets `AISOTROPY_REQUIRE_TLS`).
- No Qdrant/rqlite clustering — both run single-node; scale Qdrant independently
  when needed (the only tier meant to, per the stack's golden rule).
- The **tiers** are authenticated (Qdrant API key + rqlite `-auth`) with
  **local-only default credentials** — but **aisotropyd itself runs NoAuth** in
  this compose stack (no `AISOTROPY_JWT_SECRET`). Do not expose the `8080` port
  publicly without setting a JWT secret and a TLS-terminating proxy; the k8s
  `overlays/prod` is the hardened equivalent.

## References

- [ADR-0016 — Dual surface](../decisions/0016-dual-surface-sealed-runtime-composition-sdk.md) (lever #3)
- [Distroless deployment](distroless.md) — the production single-binary image
- [Services configuration](../../.claude/rules/services-config.md) — the env contract
- [Competitive landscape](../research/competitive-landscape.md) — why the tiered moat matters
</content>
