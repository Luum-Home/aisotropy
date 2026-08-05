---
type: Reference
title: "Services and configuration — ports, environment variables, and where each is authoritative"
description: >-
  One place to find what aisotropyd listens on, what every environment variable does, and what
  happens when each is unset. Deliberately an index rather than a copy: each row points at the file
  that owns the detail, because a consolidated reference that restates five sources becomes a sixth
  source and the next thing to go stale. This is the tracked canonical;
  .claude/rules/services-config.md is a harness projection of it and is gitignored.
tags: [deployment, reference, configuration, ports, environment, index]
timestamp: 2026-08-05T00:00:00Z
---

# Services and configuration

## Ports

| Service | Port | Notes |
|---|---|---|
| aisotropyd (chi + Huma HTTP API) | **8080** | Default; override with `AISOTROPY_ADDR`. |
| rqlite (facts tier) | **4001** HTTP | Only under `docker compose`. The bare binary defaults to in-process SQLite+FTS5. |
| Qdrant (semantic tier) | **6334** gRPC / 6333 REST | Only under `docker compose`. The bare binary defaults to an in-process cosine index. |
| BadgerDB (working memory) | — | Embedded in-process. No port, no service, no container. |
| DataFusion sleep-cycle worker | — | Invoked as a subprocess, not a server. Dormant unless `AISOTROPY_SLEEPCYCLE_BIN` is set. |

The property worth stating plainly: **the default boot needs none of these.** No Docker, no
credentials, no network. Every tier has an in-process fallback, and the networked forms are opt-in
through the variables below.

## Environment variables

| Variable | Unset means | Detail is authoritative in |
|---|---|---|
| `AISOTROPY_ADDR` | listens on `:8080` | [runtime as built](../architecture/runtime-as-built.md) |
| `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` / `OPENROUTER_API_KEY` | the keyless stub client answers — the runtime boots and serves without any provider | [model provider](../architecture/model-provider.md) |
| `AISOTROPY_JWT_SECRET` | **`NoAuth` mode**: the path identity is trusted with no credential check, with a loud startup warning. Set it for anything that is not local. | [API reference](../architecture/api-reference.md), [threat model](../architecture/threat-model.md) |
| `AISOTROPY_DATA_DIR` | working memory and the embedded facts store are **in-memory** and reset on restart — including, until it is made durable, the per-tenant cost accumulator that the cost-budget routing threshold reads | [adoption surface](../architecture/adoption-surface.md), [runbook](runbook.md) |
| `RQLITE_URL` | facts tier runs in-process | [memory tiers](../architecture/memory-tiers.md) |
| `QDRANT_HOST` / `QDRANT_PORT` / `QDRANT_COLLECTION` | semantic tier runs in-process | [memory tiers](../architecture/memory-tiers.md) |
| `AISOTROPY_OPS_TOKEN` | `/costs` and `/bitrate` answer **503** — the operator endpoints are closed by default, not open | [threat model](../architecture/threat-model.md) |
| `AISOTROPY_REQUIRE_TLS` | plaintext requests are served; TLS termination is the proxy's job | [reverse proxy](reverse-proxy.md) |
| `AISOTROPY_SLEEPCYCLE_BIN` | the Rust consolidation worker never runs | [dormant lanes](dormant-lanes.md) |

Secrets arrive **only** through the environment. Never committed, never logged — and when adding a
`slog` call near credential material, check the call site rather than assuming the formatter hides it.

## Health

| Endpoint | What it means |
|---|---|
| `GET /healthz` | Liveness only. Always 200 if the process is up. No dependency checks. |
| `GET /health` | Readiness. Cheap, read-only, `_health`-tenant-scoped probes of the facts and semantic tiers in parallel, 2s per probe. 200 `ok` / 503 `degraded`. |

`/health` **deliberately does not probe the LLM.** Calling the model on every readiness poll would
spend tokens to answer a liveness question, which is the exact inversion the
[constitutional gates](../architecture/constitutional-gates.md) forbid.

## Local development

The reference `docker-compose.yml` provisions rqlite and Qdrant with no credentials — local-only
defaults on localhost. Never reuse production credentials or URLs in local config.

## Deployment postures

Which shapes are valid, and what each costs, is not a configuration question but a state question —
working memory is embedded per process, so replica count interacts with persistence. That belongs in
one place and it is the [runbook](runbook.md), which also carries the per-replica quota and cost-meter
caveats. Do not infer a posture from this table alone.
