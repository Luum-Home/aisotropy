---
type: Section Index
title: Deployment
description: How Aisotropy is packaged and deployed — the distroless single-binary build (with measured footprint against the Phase 7 DoD) and the docker-compose reference stack that lights up the networked memory tiers (rqlite + Qdrant) instead of the in-process fallbacks.
tags: [deployment, distroless, docker, compose, memory-tiers]
timestamp: 2026-07-10T00:00:00Z
---

# Deployment

Two ways Aisotropy runs, matching the two states in the [reality ledger](../architecture/reality-ledger.md):

| Document | What it covers |
|---|---|
| [Distroless deployment](distroless.md) | The single self-contained Go binary — two-stage `CGO_ENABLED=0` build on `distroless/static`, with the **measured** binary size / startup / idle-RAM against the Phase 7 DoD target. This is the zero-config in-process runtime. |
| [Reference deployment — docker-compose](docker-compose.md) | The compose stack that boots `aisotropyd` with its **networked memory tiers active** (rqlite facts + Qdrant semantic) instead of the in-process fallbacks, plus the sleep-cycle opt-in overlay (ADR-0016 lever #3). This is how the dormant tiers in the reality ledger get lit up. |

Which one you run is exactly the "REAL by default vs CODE-ONLY behind env vars" split the
[reality ledger](../architecture/reality-ledger.md) records: the distroless binary alone is the
in-process engine; the compose stack is what makes the Rust/networked tiers real for a deployment.
