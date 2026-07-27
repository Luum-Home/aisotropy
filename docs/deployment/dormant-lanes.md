---
type: Guide
title: Dormant lanes — one command that builds the Rust workers and runs the real-service tests
description: scripts/dormant-lanes.sh, the single operator-runnable lane that compiles crates/sleep-cycle and crates/bitrate-scorer, exercises them through the -tags manual live tests, and brings up an isolated rqlite + Qdrant stack to run the real-service tests against — status-first, resource-guarded with declared RAM budgets, and leaving the machine exactly as it was found.
tags: [deployment, testing, lanes, rust, sleep-cycle, bitrate, rqlite, qdrant, manual-tests, resource-guard, adr-0075]
timestamp: 2026-07-27T00:00:00Z
---

# Dormant lanes — the button that did not exist

## What was actually missing

It is tempting to describe the sleep-cycle and bitrate paths as "not built" and the
networked memory tiers as "untested". Verified against the code on 2026-07-27
([the gap-claims verification](../06-Daily/reports/gap-claims-verification-2026-07-27.md)),
neither is true:

| Assumed gap | Code truth |
|---|---|
| "The Rust workers don't exist" | Both are tracked in-repo — `crates/sleep-cycle` (DataFusion 43) and `crates/bitrate-scorer` (candle) — with Rust-side unit tests. |
| "The Go↔worker contract is untested" | Both contracts are fully unit-tested with executable stubs (`internal/sleepcycle/worker_test.go`, `internal/bitrate/worker_test.go`): argv, NDJSON, exit codes, retry/backoff, ctx-cancel. |
| "Nobody wrote real-service tests" | Fourteen `//go:build manual` test files exist, four of them against live rqlite/Qdrant. |

The real gap was narrower and duller: **`git grep -l "tags manual" -- scripts/ .github/` returned
nothing.** No script and no workflow ever built a Rust worker or invoked a single manual-tagged
test. The code was live and the tests were written; the *button* did not exist.

[`scripts/dormant-lanes.sh`](../../scripts/dormant-lanes.sh) is that button.

## The three stages

```bash
scripts/dormant-lanes.sh                        # status — the DEFAULT, spends nothing
scripts/dormant-lanes.sh --stage workers --yes  # build + exercise the Rust workers
scripts/dormant-lanes.sh --stage services --yes # real rqlite + Qdrant, then tear down
scripts/dormant-lanes.sh --stage all --yes      # both
```

| Stage | What it does | Exit |
|---|---|---|
| `status` (default) | Reports what is present vs missing — cargo, go, docker daemon, built binaries, pulled images, free lane ports, the resource-guard verdict — and prints each stage's declared RAM budget and measured cost. **Starts, builds and pulls nothing.** | `0` all present · `1` something missing |
| `workers` | `cargo build --release` both crates, exports `AISOTROPY_SLEEPCYCLE_BIN` / `AISOTROPY_BITRATE_BIN` at the built paths, runs the Go-side contract tests, then `go test -tags manual` against the **real** binaries. | `0` · `1` findings · `3` blocked |
| `services` | Brings up an isolated rqlite + Qdrant stack, waits for `/readyz` on both (bounded), runs the four `-tags manual` real-service tests, tears the stack back down. | `0` · `1` findings · `3` blocked |

Exit codes follow the repo guard convention: `0` clean (including SKIPPED-with-reason),
`1` findings, `2` usage/environment error, `3` BLOCKED by `resource-guard`.

## Two safety properties this lane is built around

### 1. Consent is separate from capacity

The default invocation is `status`. Doing anything requires naming a stage, and *provisioning*
(cargo build, `docker compose up`) additionally requires an explicit `--yes`. Without it the stage
prints its plan and exits 0 having spent nothing.

This is [ADR-0071](../decisions/0071-k8s-deploy-validation-lanes.md)'s lesson applied: a resource
verdict is a snapshot that says the machine **can**, and only an operator says it **should**. That
ADR exists because `resource-guard` flipped DEGRADED→OK between two probes and a "this should
refuse" test began building a cluster unattended.

Every heavy step runs through [`scripts/with-heavy-lane.sh`](../../scripts/with-heavy-lane.sh)
`--need-mb N` — never bare — so it takes the cross-session mutex and declares a RAM budget the
guard can refuse. The budgets are **measured, not guessed** (see below); an under-declared budget is
the dangerous direction, because it buys a green light the machine cannot honour.

### 2. The machine is left exactly as it was found

The `services` stage runs under its **own compose project** (`aisotropy-dormant-lane`) on its **own
host ports** (14001 / 16333 / 16334 by default), with **no named volumes at all**. It therefore
cannot adopt, disturb, or tear down a reference stack an operator — or a concurrent agent session —
already has running on 4001/6333/6334. Ports are re-checked at execution time, not at plan time,
and an occupied lane port is a refusal, not a fallback.

Teardown runs from an `EXIT`/`INT`/`TERM` trap **and** explicitly at the end of the stage, so an
incomplete teardown is itself a reported finding rather than a silent leftover.

## Why the lane has its own compose file

[`deploy/dormant-lane/docker-compose.yml`](../../deploy/dormant-lane/docker-compose.yml) exists
because the reference [`docker-compose.yml`](docker-compose.md), correct as it is for running
`aisotropyd` next to its tiers, cannot serve host-side manual tests. Three concrete reasons, each
found by trying the reference file first:

1. **rqlite advertises a container-internal address.** Without `-http-adv-addr` the node advertises
   its container hostname (`424f8f2f3d74:4001`). `gorqlite` does cluster discovery and *follows*
   the advertised HTTP address, so a host-side client cannot resolve it — the exact caveat
   `internal/facts/rqlite_manual_test.go`'s own doc comment records. The lane pins
   `-http-adv-addr localhost:<host port>`.
2. **A recreated container loses its Raft leader.** rqlite derives its node ID from the container
   hostname, and the reference stack keeps a named volume. Recreate the container and the persisted
   Raft configuration names a node ID that no longer exists: `not part of stable configuration,
   aborting election`, `/readyz` 503 forever, compose healthcheck permanently `unhealthy`. This was
   **observed live on 2026-07-27** on this machine's reference stack. The lane pins the node ID and
   uses ephemeral storage, so every run starts leader-electable and leaves nothing behind.
3. **Port ownership**, as above.

Images are pinned to the **same tags** as the reference stack (`qdrant/qdrant:v1.12.4`,
`rqlite/rqlite:8.36.3`): the lane file changes the wiring, never the versions under test.

## Declared RAM budgets (`--need-mb`)

Measured on the lane's first full execution (2026-07-27, 24 GB M-series host, both crates cold,
`CARGO_BUILD_JOBS=4`): **peak RSS summed over all cargo+rustc processes = 1924 MB**, sampled every
2 s across the whole `workers` stage. sleep-cycle built in **3m08s** (~330 crates, 72 MB binary),
bitrate-scorer in **41s**. Disk: ~1.6 GB of `target/` across both crates.

| Step | `--need-mb` | Derivation |
|---|---|---|
| `cargo build` sleep-cycle | 2560 | measured stage peak 1924 MB + ~33% headroom |
| `cargo build` bitrate-scorer | 2560 | same — the sample is stage-level, so it cannot honestly be attributed to one crate |
| Go contract tests | 1024 | small packages; generous headroom for the build cache |
| Containers (qdrant + rqlite) | 1024 | Qdrant idles ~300–400 MB RSS, rqlite ~60–100 MB, plus per-container engine overhead |
| Go manual tests | 512 | thin clients; both live suites finished in <2 s wall clock |

Peak build RAM scales with concurrent rustc/codegen units, so the lane caps `CARGO_BUILD_JOBS=4`.
**Raising that cap invalidates the budgets** — raise the `NEED_MB_CARGO_*` env overrides with it.

## What a green run proves

First full execution, 2026-07-27:

```
workers   sleep-cycle + bitrate-scorer built from cold
          internal/sleepcycle + internal/bitrate contract tests: ok
          TestWorkerLive, TestWorkerLiveNoisePrune,
          TestWorkerLiveNoiseFloorDisabledIsDedupOnly: PASS
          — dedup 3→2 and dedup+noise 4→2 through the REAL DataFusion engine
services  rqlite ready in 3s, qdrant ready in 0s
          TestRqliteLive, TestRqliteRebootIdempotent: PASS
          TestQdrantLive, TestQdrantLiveNewPaths: PASS
          — real FTS5 Search/Supersede over HTTP+Raft; real gRPC upsert,
            payload-filtered search, UUIDv5 idempotency, Delete/DeleteBySource/PruneTurns
          teardown: 0 containers, 0 volumes, 0 networks left in the lane project
```

## Honest boundaries

- **The bitrate LIVE test SKIPs by default.** `TestScorerLive` needs `AISOTROPY_BITRATE_MODEL_DIR`
  pointing at converted GPT-2 weights (`model.safetensors` + `tokenizer.json` — a one-time offline
  conversion, see [ADR-0015](../decisions/0015-bpt-eval-model-and-python-batch-runtime.md)). The
  binary **is** built and the Go contract **is** covered; only the weights-dependent run is absent,
  and it is reported as an explicit SKIP, never as a pass.
- **This is not CI.** Nothing here is wired into `.github/workflows/ci.yml` or the pre-push gate. A
  3-minute DataFusion build and two containers per push is a separate decision with a separate cost;
  the registered claim `rust-workers-not-built-in-ci` in
  [`manifests/capability-claims.yaml`](../../manifests/capability-claims.yaml) stays true and stays
  green.
- **It proves the tiers work, not that the runtime uses them under load.** The manual tests exercise
  the Store contracts against real services. End-to-end behaviour of `aisotropyd` wired to those
  tiers is the reference compose stack's job.
- **The measured peak is stage-level.** Attributing RAM to one crate would need a per-build sample
  nobody has taken; the budgets say so rather than inventing a per-crate figure.

## Related

- [ADR-0075](../decisions/0075-dormant-lanes-operator-runnable.md) — the decision and its options.
- [Reference deployment — docker-compose](docker-compose.md) — the stack this lane deliberately does
  not reuse, and why.
- [Machine resource guard](../architecture/machine-resource-guard.md) — `resource-guard.sh` /
  `with-heavy-lane.sh`, of which this lane is the first real `--need-mb` consumer.
- [DataFusion sleep-cycle](../components/datafusion-sleep-cycle.md) ·
  [Bitrate bpt scorer](../components/bitrate-bpt-scorer.md) — the two components the `workers` stage
  lights up.
