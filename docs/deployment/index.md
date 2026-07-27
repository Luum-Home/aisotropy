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
| [Dormant lanes — build the Rust workers, run the real-service tests](dormant-lanes.md) | The single operator-runnable lane (`scripts/dormant-lanes.sh`) that compiles `crates/sleep-cycle` + `crates/bitrate-scorer` and exercises them through the `-tags manual` live tests, and that brings up an **isolated** rqlite + Qdrant stack to run the real-service tests against before tearing it down. Status-first (the default spends nothing), resource-guarded with **measured** `--need-mb` budgets, and it leaves the machine exactly as it was found (ADR-0075). |
| [Reverse proxy — SSE streaming](reverse-proxy.md) | Fronting the plain-HTTP `aisotropyd` with Caddy or nginx **without buffering the token-by-token SSE routes** into a single dump. The reference `deploy/proxy/` configs, the `X-Accel-Buffering: no` middleware that makes streaming survive a default nginx, and the executable end-to-end proof under `test/proxy/`. |
| [Supply-chain hardening](supply-chain.md) | The supply-chain security posture — `govulncheck` + dependency scanning as a **local pre-push git hook** (no extra paid GitHub Actions), base images pinned by immutable `@sha256:` digest, and secrets fenced out of the Docker build context. |

Which one you run is exactly the "REAL by default vs CODE-ONLY behind env vars" split the
[reality ledger](../architecture/reality-ledger.md) records: the distroless binary alone is the
in-process engine; the compose stack is what makes the Rust/networked tiers real for a deployment.

## Build-time capability: key-brokering (`-tags keybroker`)

Key-brokering — the sandboxed, untrusted agentic-task primitive (`App.RunTask`/`App.RunTasks` and the
`aisotropy task` CLI subcommand, [ADR-0036](../decisions/0036-sandboxed-task-agent-key-brokering.md))
— is the **one capability that needs a container runtime (Docker or Podman) at call time**. To keep
the default binary Docker-unaware and the [5-minute onramp](../getting-started.md) container-free, it
is compiled behind a build tag:

```bash
go build ./...                    # default: no key-brokering, no Docker intent
go build -tags keybroker ./...    # adds App.RunTask/RunTasks + the `aisotropy task` subcommand
```

This is a positioning decision, not just packaging: the moat is multi-tenant isolation by
construction + tiered memory (the Docker-free substrate the whole `internal/` core serves), and
key-brokering is its **flagship optional demonstration** — untrusted agents whose actions run in a
throwaway sandbox that never receives the provider key. See
[positioning-key-brokering.md](../architecture/positioning-key-brokering.md) for the decision and its
grounding.

**This is an API change**, not a runtime flag: without `-tags keybroker`, `App.RunTask`/`RunTask`s and
the `task` subcommand are not part of the compiled public surface at all (`go doc ./aisotropy App`
shows no `RunTask` method in the default build). A consumer who calls `RunTask` must build with the
tag. The isolation + memory core, the HTTP contract, and `App.Turn`/`App.Run` are identical in both
builds — enabling the tag only *adds* the key-brokering surface, it changes nothing else. The
`internal/taskagent` + `internal/sandbox` engine packages stay compiled and unit-tested in the default
build (they are pure Go; Docker is a *call-time*, not a *build-time*, dependency); only the public
façade and CLI are gated.
