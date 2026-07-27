---
type: Concept
title: "Machine resource guard — reject heavy dev-lane work before saturating the host"
description: The resource-guard.sh / with-heavy-lane.sh pair — a pre-flight gate and cross-session mutex for heavy local work (full test sweeps, voice-model inference, docker builds), applying the runtime's reject-before-spending discipline to the developer machine itself. Distinct from (and complementary to) the token-economy governance layer.
tags: [tooling, guards, resources, testing, concurrency, dev-experience]
timestamp: 2026-07-23T00:00:00Z
---

# Machine resource guard

## Origin (the incident this prevents)

On 2026-07-23, several concurrent agent sessions (full `go test ./...` sweeps, voice-model
inference for the ADR-0066 PoC lane, docker builds) saturated the dev machine: **swap 18.0/19.4 GB
(93%), ~500 MB free RAM, load/core 2.2+**. macOS responded by OOM-killing a test worker — the
`internal/guardrails` "worker exit: signal: killed" flake, which passed in isolation and only
failed under full-suite load. A killed worker looks like a test failure; the real defect was
starting heavy work on a machine that had nothing left to give.

## The discipline

The runtime's constitutional gate is **reject/degrade before spending tokens**. This is the same
rule with the machine as the budget: **heavy lanes check resources BEFORE starting, and refuse or
degrade instead of getting killed mid-run.** A guard that cannot measure must never absolve
(the prune-guard fail-closed principle).

## The two pieces (`scripts/`)

| Script | Role | Exit codes |
|---|---|---|
| [`resource-guard.sh`](../../scripts/resource-guard.sh) | Pre-flight check: swap %, free RAM, 1-min load/core vs documented thresholds (env-overridable; the defaults are the contract, calibrated against the origin incident) | `0` OK · `1` DEGRADED (run serially, small models, capped threads) · `3` BLOCKED (do not start; also on unmeasurable) |
| [`with-heavy-lane.sh`](../../scripts/with-heavy-lane.sh) | Canonical wrapper for heavy commands: runs the guard, takes a **cross-session mutex** (atomic `mkdir` lock with stale-pid reclaim — two sessions can no longer run full sweeps simultaneously), executes under `nice 19`; DEGRADED additionally caps `OMP_NUM_THREADS=2` / `GOMAXPROCS=4` | wrapped command's code; `3` if guard-blocked or lock timeout |

Canonical usage — any invocation that can eat the machine goes through the wrapper:

```bash
scripts/with-heavy-lane.sh go test ./... -count=1
scripts/with-heavy-lane.sh pytest examples/voice-pipecat/tests/
```

Verified live on the origin day: the guard correctly reported `BLOCKED (saturated)` on the real
93 %-swap state and the wrapper refused to start the suite (exit 3).

## What already existed, and why this is a different layer

Per the dependency-reuse-first discipline, the pre-existing surface was inventoried before this
shipped. The the agent tooling `resource-governor` skill and the `resource-governance` /
`workload-scheduling` rules are the **token-economy layer**: LLM budget percentages, model
routing/downgrade chains, agent slot scheduling — they read cost metrics and optimize *spend*.
None of them measures **host resources** (swap/RAM/load) before shell-level heavy work. This pair
fills exactly that gap and only that gap: it is ADDITIVE to the governance layer, not a
reimplementation of it. The two compose: the governor decides *what work is worth doing*; this
guard decides *whether the machine can do it right now*.

## Integrations

- **Voice test lane** (ADR-0066 PoC follow-on): the opt-in real lane consults the guard and
  SKIPs with an explicit reason when BLOCKED — a visible skip instead of an OOM-killed worker.
- **Multi-session convention**: agent sessions wrap full test sweeps, model inference, and
  docker-heavy phases in `with-heavy-lane.sh`. Light commands do not use the wrapper.

## Capacity is not permission (the ADR-0071 incident, and what it changed)

While building the k8s lanes, a test written to prove the gate *refuses* instead sailed through
and began creating a Kind cluster and a 3.5 GB build: **the guard had flipped DEGRADED → OK
between two probes minutes apart.** It was stopped and the residue force-cleaned, but the lesson
is structural, and three things now encode it:

1. **A verdict is a snapshot, so re-take it.** `with-heavy-lane.sh` re-runs the guard **after**
   acquiring the cross-session lock whenever it waited — the machine handed back by another
   session is not the machine measured before the wait. (The original script checked only once,
   before a wait that can last `HEAVY_LANE_WAIT_S`; that was the same latent bug.)
2. **Declare what you need.** `--need-mb N` makes a lane state its RAM requirement; the guard
   answers against actually-available memory, not against a mood. "Not saturated" is not
   "has 4 GB free" — on a small machine a lane can pass a pressure check and still thrash.
   `with-heavy-lane.sh --need-mb 4000 <cmd>` passes it through.
3. **Provisioning needs a human, not a metric.** Lanes that create clusters, pull multi-GB
   images, or build containers require an explicit `--yes` on top of a green guard
   (`scripts/k8s-cluster-lane.sh`, ADR-0071). No resource verdict, however fresh, authorises
   them — hence the rule: **capacity is not permission.**

## Honest boundary

The guard samples at a point in time; it cannot stop a run that *becomes* heavy after passing
the check (fix #1 narrows the window, it does not close it), and the mkdir mutex only serializes
lanes that opt into the wrapper. `--need-mb` trusts the caller's estimate — a lane that
under-declares is not caught. Thresholds are calibrations from one machine's incidents — tune per
host via the `RESOURCE_GUARD_*` env overrides.
