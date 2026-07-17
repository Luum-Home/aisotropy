---
type: Survey
title: "Framework-overhead & cost-governance benchmarks — aisotropy runtime (+ cross-framework method)"
description: Honest, reproducible benchmarks with the MODEL HELD CONSTANT as the keyless stub, so measured time is framework/runtime overhead — not model performance. Class 1 = runtime overhead (real ns/op from a live run of aisotropy's hot paths + one full in-process turn). Class 2 = effectiveness (a token-counting stub proves the pre-spend quota gate and the response cache actually cut token spend, no provider). The cross-framework wall-clock table is documented as a reproducible method, not fabricated.
tags: [arenas, benchmarks, overhead, latency, throughput, cost-governance, token-counting, methodology, competitive]
timestamp: 2026-07-16T00:00:00Z
---

# Benchmarks — framework overhead & cost-governance effectiveness

> **Two honest questions, two classes.**
> **Class 1 — overhead:** how much time does the *framework* add around a model call
> (identity→ctx, quota gate, memory hydration, tool gating, contract enforcement)? Measured
> with the model held constant as a zero-latency stub, so every nanosecond is the framework's.
> **Class 2 — effectiveness:** do the governance levers (pre-spend quota gate, response cache)
> actually *reduce token spend*? Proven with a token-counting stub — no provider, the counter
> delta is the proof.

## Why the model is held constant

Model performance — tokens/sec, time-to-first-token, answer quality — is a property of the
**provider and the model**, not of the framework wrapping it. Benchmarking two frameworks against
two *different* live models measures the models, not the frameworks. So every number here holds the
model constant as the **keyless stub** (`internal/llm.StubClient`, `Delay` ≈ 1 ns): a deterministic,
zero-latency echo/structured-reply client. What remains, once model latency is removed, is exactly
the framework's own overhead — the thing under test. This is the same discipline the isolation and
cost surveys in this directory use (`README.md`, `COST-OBS.md`): a fake model + a counter, run
offline, exit 0.

Everything below runs with **no `ANTHROPIC_API_KEY`, no network, no provider**.

---

## Class 1 — aisotropy runtime overhead (REAL, run-verified ▶)

Machine of record: **Apple M4 Pro, darwin/arm64, Go 1.25.1**, `-benchtime=1s`. Reproduce with
[`scripts/run-overhead-bench.sh`](scripts/run-overhead-bench.sh) — your absolute ns/op will differ
by hardware, the *shape* (which stage costs what) will not. `ns/op` is wall-clock per operation;
`allocs/op` is heap allocations per operation.

### 1a — hot-path micro-benchmarks (the stages a turn touches)

These are pure Go/BadgerDB costs on the request goroutine — the model is not involved at all.

| Benchmark | What it measures | ns/op | allocs/op |
|---|---|---:|---:|
| `internal/tools` `RegistryPermitted/tools=4` | scope-gate: filter 4 tools by granted scopes | **135** | 3 |
| `internal/tools` `RegistryPermitted/tools=16` | same, 16 tools | 435 | 5 |
| `internal/tools` `RegistryPermitted/tools=64` | same, 64 tools | 1 686 | 10 |
| `internal/tools` `RegistryPromptFragment/tools=4` | gate + render the injected tool prompt fragment | 448 | 14 |
| `internal/tools` `RegistryPromptFragment/tools=16` | same, 16 tools | 1 641 | 36 |
| `internal/tools` `RegistryPromptFragment/tools=64` | same, 64 tools | 5 741 | 116 |
| `internal/memory` `KeyValidate` | isolation gate: validate a (tenant,user,session) key | **40** | 0 |
| `internal/memory` `KeyChatMessageKey` | derive the scoped on-disk message key | 239 | 9 |
| `internal/memory` `Summary` | hydration read: condensed summary (1 keyed Get) | 796 | 14 |
| `internal/memory` `Recent` (window=12, n=10) | hydration read: reverse prefix scan of recent turns | 15 556 | 162 |
| `internal/memory` `LimiterAllow` | pre-spend quota gate: 1 token-bucket check+deduct (BadgerDB) | 5 462 | 42 |
| `internal/memory` `WorkingMemory_AppendOnly` | write path: persist one turn (+window count scan) | 302 782 | 372 |
| `internal/memory` `WorkingMemory_AppendRead` | steady-state append+read **including** condensation churn | ~2 044 990 | 15 722 |

Reading the numbers:

- **Isolation is cheap.** The tenant-isolation gate that runs before every memory op
  (`KeyValidate`) is **~40 ns, zero allocations** — isolation-by-construction is not paid for in
  latency. Scope-gating tools is tens to a few thousand ns depending on catalog size.
- **The write path dominates, and it is BadgerDB, not the framework glue.** A single
  `AppendOnly` is ~0.3 ms; the combined `AppendRead` steady-state figure (~2 ms) is inflated by the
  async condensation goroutine and BadgerDB LSM compaction firing every iteration as the bounded
  window overflows — it is a churn stress figure, **not** a pure read. The isolated hydration reads
  (`Summary` 0.8 µs, `Recent` 15.6 µs) are the honest per-turn read cost.
- **Allocation-heavy spots are the string/JSON assembly** (prompt-fragment render, message
  encode/decode), the natural targets if this ever needs tuning.

### 1b — end-to-end in-process turn (`aisotropy.BenchmarkTurn`)

One full `App.Turn` with the zero-latency stub: identity→ctx, pre-spend quota gate, input
guardrail, gateway model selection, working-memory + facts hydration, transactional-outbox persist,
fail-closed reflection loop, output guardrail, cost record. This is the whole framework tax a
library consumer pays around a model call, with the model removed.

| Benchmark | ns/op | allocs/op | notes |
|---|---:|---:|---|
| `aisotropy.BenchmarkTurn` | _run via the script_ | _run via the script_ | one warmed runtime, steady-state session |

> This end-to-end row is produced by `scripts/run-overhead-bench.sh` against a green tree. It is
> intentionally left to the runner rather than transcribed here, because unlike the leaf packages
> above the full `aisotropy` package couples to the whole runtime boot and its number is only
> meaningful from a clean build. The micro-benchmarks above are the stable, independently-verifiable
> core; `BenchmarkTurn` is their sum plus the pipeline glue.

---

## Class 2 — cost-governance effectiveness (NO model, token-counting stub)

The levers under test do not need a provider to *prove*, only a **counter on a fake model**: wrap
the stub in a client that tallies (a) how many times it is actually called and (b) the
provider-reported `Usage` on each call. A turn that a gate rejects, or a cache serves, never
reaches the counter — so the counter delta is a direct, provider-free measurement of spend avoided.
This is the same call-counter method `COST-OBS.md` uses per arena.

### 2a — pre-spend quota gate (`TestQuotaGatePreSpendReducesTokenSpend`)

The constitutional gate: quota is checked **before** the LLM call, so a rate-limited tenant is
rejected (`429`) without spending a token (`internal/api/core.go` → `chatCheckQuota`, before
`selectClient`/`StreamChat`). Setup: a token bucket of **burst = 3, refill = 0** against **10**
in-process turns for one tenant, versus an uncapped control that lets all 10 through.

| Run | turns | reached the model (calls) | rejected pre-spend | tokens spent |
|---|---:|---:|---:|---:|
| **Gated** (burst 3) | 10 | **3** | **7** | _via runner_ |
| **Control** (uncapped) | 10 | 10 | 0 | _via runner_ |

Proven property (from a live run of this exact test): **exactly `burst` turns reach the model; the
other 7 are rejected before `StreamChat`, spending zero tokens.** The gated run's token total is
strictly below the control's — the pre-spend gate is real, not advisory. (Absolute token counts are
the stub's synthetic `Usage`; the load-bearing result is the *call* delta 3 vs 10 and the strict
`gated < control` token inequality.)

### 2b — response cache (`TestResponseCacheReducesTokenSpend`)

The real, stateless `/ask` route consults a per-`(tenant,user)` response cache before the model
(`internal/api/ask.go`). Setup: the **same** question asked **twice** over HTTP, no provider.

| Request | cache | reached the model | tokens for this turn |
|---|---|---:|---:|
| 1st (identical prompt) | MISS | yes (1 call) | > 0 |
| 2nd (identical prompt) | HIT | **no** | **0** |

Proven property: **two identical `/ask` requests produce exactly ONE model call**; the second is
served from cache (`cached: true`) with **zero additional tokens**. Repeated questions are free.

### 2c — memory-lift (bits/token) — plumbing proven, SCORING needs a model

`internal/bitrate` (Bet 6) measures how much a tenant's hydrated memory lowers the surprisal of that
tenant's own text (`lift = bpt_without − bpt_with`). **True scoring requires a reference model's
per-token logprobs** (distilgpt2 in Phase 1, a Rust candle model in Phase 2) run as a subprocess —
that is Class 2a-of-bitrate and stays behind the `manual` build tag (`bitrate_manual_test.go`,
`AISOTROPY_BITRATE_BIN`). What runs offline **today** is the whole measurement *pipeline* with a
deterministic, model-free surprisal stand-in (`internal/bitrate/harness_test.go`
`TestBitrateMeasurementPathNoModel`): it hydrates the two contexts, materializes the NDJSON scorer
contract, computes and **persists** a per-tenant lift, and proves isolation between tenants — with
a directional check (richer memory ⇒ larger lift). A representative run:

| tenant | bpt_with | bpt_without | lift | source |
|---|---:|---:|---:|---|
| globex (tight memory overlap) | 1.500 | 6.000 | **4.500** | model-free stand-in |
| acme (partial overlap) | 2.625 | 6.000 | **3.375** | model-free stand-in |

> These bit values are a **stand-in, not a measurement** — only the *sign and direction* of the
> lift are meaningful without a real model. The number that would go in front of a customer needs a
> provider + eval. The point proven here is that the pipeline that would carry that number is live.

---

## Class 3 — Framework footprint (REAL, run-verified ▶)

The one cross-framework number that **is** honestly executable offline: deployment **footprint**.
Image size, idle RSS, and dependency weight are language-agnostic in a way wall-clock is not — a
400 MB image is 400 MB whether it is fast or slow, and it is a real property every operator pays for
(registry storage, pull time, cold-start, per-pod RAM, attack surface). Measured on a
cross-language **flagship set** (per `docs/architecture/consolidation-proposal.md` "12 → 3
flagship" — not all 12), each a minimal "stand up the framework + one agent" app with the **model
held constant as an offline stub**. Harness + reproduce steps: [`bench/footprint/`](../bench/footprint/)
(`bash bench/footprint/run.sh`); raw output in `bench/footprint/results.txt`.

| Framework | Language / packaging | Image (MiB) | Idle RSS (MiB) | Cold-start ms (med) | Dependency weight |
|---|---|---|---|---|---|
| **aisotropy** | Go / distroless static | **12.4** | **10.6** | **214** | 108 go.sum modules; static-linked (no dep files ship) |
| LangGraph | Python / `python:3.12-slim` | 61.3 | 47.0 | 656 | 75 MiB site-packages, 74 top-level |
| Mastra | TypeScript / `node:22-slim` | 126.5 | 121.2 | 700 | 141 MiB `node_modules`, 198 packages |
| ADK-Go | Go / distroless static | NA | NA | NA | build failed in this run (honest NA — not measured) |

**Takeaway (run-verified):** aisotropy's single static Go binary in Distroless ships at **12.4 MiB** —
**~5× smaller** than the Python (LangGraph) image and **~10× smaller** than the Node/TS (Mastra) one —
with **4–11× less idle RAM** (10.6 MiB vs 47 / 121) and ~3× faster cold-start (214 ms vs 656 / 700).
This is the concrete, fair number behind the "single Go binary" claim: the deps are *statically
linked into the binary*, so nothing ships a `node_modules`/`site-packages` tree at all.

**Honest caveats.** (1) All subjects are *minimal* apps with the model stubbed — footprint of the
framework+runtime, not of a production workload. (2) The aisotropy image is the repo's own
production `Dockerfile` (multi-stage → Distroless); the framework images use the idiomatic slim base
for their ecosystem — a fair minimal, not a stripped strawman. (3) **ADK-Go failed to build in this
run** and is reported as `NA`, not estimated — a same-language Go competitor's footprint is left
unmeasured here rather than guessed. (4) Cold-start is the one timing number, included because it is
a real deployment property (runtime boot: interpreter import vs. static exec), not model behaviour;
the `1683 ms` max on aisotropy is a first-run outlier (image layer cache warm-up), hence median is
reported. (5) A full per-token *latency/quality* wall-clock is deliberately **not** published — see
the method note below for why that would measure the models, not the frameworks.

## Cross-framework method (overhead/latency: documented, NOT executed — and why)

A full cross-framework overhead table (aisotropy vs the 12 arenas) is **not fabricated here**.
Doing it honestly requires each framework's runtime installed and its stub-backed path driven under
an identical load generator — most of those runtimes (Python/TS/JVM/Workers) are not present in this
offline Go environment. What *is* runnable offline is aisotropy's own overhead (Class 1 above, via
the Go `testing.B` harness) and its HTTP path (below). The reproducible cross-framework method, for
whoever has the runtimes:

1. **Hold the model constant.** In every framework, register the *same* stub model: a synchronous,
   zero-latency function returning a fixed short string (and, where the framework surfaces it, a
   fixed synthetic `Usage`). No network, no real provider. This is the single most important control.
2. **Define one identical workload.** e.g. *N = 10 000* single-turn requests for one
   `(tenant, user, session)`, prompt fixed, memory hydration enabled at each framework's default —
   plus a second workload with a growing conversation to expose per-turn context assembly cost.
3. **Drive each framework two ways, matched:**
   - **In-process** (library call, no HTTP) where the framework supports it — aisotropy via
     `App.Turn`, the Go arenas via their equivalent entrypoint — to isolate pure framework overhead.
   - **Over HTTP** (the server each framework ships) under a shared load generator (`bombardier`,
     `wrk`, or `hey`) at a fixed concurrency sweep {1, 8, 64}, reporting **p50/p95/p99 latency and
     throughput (req/s)**.
4. **Report the same cells for every framework:** median in-process ns/op (from that language's
   micro-benchmark harness — Go `testing.B`, `pytest-benchmark`, `vitest bench`, JMH), and the HTTP
   p50/p95/p99 + req/s. Same machine, same load generator, same stub, same workload file.
5. **Publish the harness, not just the table.** Each framework's stub-model wiring + load script
   committed next to its arena (as the isolation/cost demos already are), so any number is
   re-runnable and falsifiable.

### aisotropy HTTP path (runnable offline now)

aisotropy's own HTTP overhead *is* measurable offline and is the reference row for the table above.
Boot the stub-backed server and drive it with any HTTP load generator:

```sh
# 1. Boot aisotropyd with the keyless stub (no API key), NoAuth for local load:
AISOTROPY_ADDR=:8080 go run ./cmd/aisotropyd &
# (or embed via aisotropy.New(WithAuthenticator(aisotropy.NoAuth()), ...).Run(ctx))

# 2. Drive the stateless /ask path (stub model, deterministic) under load:
bombardier -c 64 -n 50000 -m POST \
  -H 'Content-Type: application/json' -b '{"prompt":"benchmark question"}' \
  http://localhost:8080/v1/tenant/acme/user/alice/ask
```

Report p50/p95/p99 + req/s from the load generator. Because the model is the stub, the whole
number is Huma validation + routing + identity→ctx + cache/RAG + the (stubbed) call — i.e.
aisotropy's HTTP framework overhead, directly comparable to any other framework's server driven the
same way.

---

## How to reproduce everything here

```sh
arenas/scripts/run-overhead-bench.sh            # all Class 1 + Class 2, default 1s/bench
BENCHTIME=3s arenas/scripts/run-overhead-bench.sh   # steadier ns/op
arenas/scripts/run-overhead-bench.sh -v          # also print the token-delta proof logs
```

The script runs only aisotropy's own module — no arena runtimes needed — and never contacts a
provider.

## Honest caveats

- **Absolute ns/op is hardware-specific.** The *relative* cost of each stage (isolation cheap, write
  path dominant, allocations concentrated in string/JSON assembly) is the portable finding.
- **The stub is not a model.** These benchmarks deliberately measure everything *except* the model.
  Real end-to-end latency is dominated by provider time-to-first-token and tokens/sec, which no
  framework controls.
- **Token counts in Class 2 are synthetic** (the stub's fixed `Usage`). The load-bearing results are
  the **call-count deltas** (3 vs 10; 1 vs 2) and the strict inequalities, which are exact and
  provider-independent.
- **The cross-framework table is a method, not a result.** It is left un-fabricated on purpose; only
  aisotropy's own numbers are claimed as measured.
