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

Two variants are reported per interpreted competitor so no single multiplier is mistaken for a
ceiling: the **idiomatic-minimal** image (the smallest *standard* `-slim` way to ship it) and an
**optimized-floor** image (squeezed as hard as still-boots allows — alpine + prune for Python,
esbuild tree-shake for Node). **ADK-Go** is the same-language control: packaged with the *exact*
static-binary→distroless recipe aisotropy uses, so its gap vs aisotropy isolates *framework* from
*language*. Every row below **boots and serves HTTP 200 on `/healthz`** — an image that doesn't run
is not a data point.

| Framework | Variant | Language / packaging | Image (MiB) | Idle RSS (MiB) | Cold-start ms (med) | Dependency weight |
|---|---|---|---|---|---|---|
| **aisotropy** | production `Dockerfile` | Go / distroless static | **12.4** | **8.8** | **262** | 108 go.sum modules; static-linked (no dep files ship) |
| **ADK-Go** *(control)* | static → distroless | Go / distroless static | **5.9** | **3.5** | **310** | 46 go.sum modules; static-linked (no dep files ship) |
| LangGraph | idiomatic | Python / `python:3.12-slim` | 61.3 | 47.0 | 583 | 75 MiB site-packages, 74 top-level |
| LangGraph | optimized | Python / alpine + prune | 31.3 | 54.1 | 1930 | 48 MiB site-packages, 76 top-level |
| Mastra | idiomatic | TypeScript / `node:22-slim` | 126.5 | 121.2 | 514 | 141 MiB `node_modules`, 198 packages |
| Mastra | optimized | TypeScript / alpine + esbuild | 56.9 | 105.5 | 562 | single tree-shaken bundle; no `node_modules` ships |

**The same-language control (Go vs Go).** ADK-Go, packaged identically to aisotropy, lands at
**5.9 MiB / 3.5 MiB RSS** — *smaller* than aisotropy's 12.4 MiB, because aisotropy's image boots a
whole product (memory tiers, quota gate, circuit breaker, router, Huma, BadgerDB, SQLite) while the
ADK-Go subject is one agent + `net/http`. So the readout is: the **Go language floor** is ~6 MiB;
aisotropy's extra ~6 MiB is *what aisotropy does at boot*, not a language tax. The framework-vs-
framework delta between the two Go subjects is **single-digit MiB** — and *both* Go subjects are an
**order of magnitude** under the interpreted images. That is the isolation the control was missing
before: the bulk of the gap vs Python/Node is the **language** (compiled static binary vs
interpreter + dependency tree), not "aisotropy is magic."

**Optimized floors narrow the multiplier but don't close it.** Hand-optimizing the competitors cuts
image size (**LangGraph 61→31 MiB, ~5×→~2.5×; Mastra 127→57 MiB, ~10×→~4.6×** vs aisotropy's 12.4)
but the structural cost survives: **idle RSS barely moves** (LangGraph 47→54 MiB — the CPython heap
is fixed regardless of image; Mastra 121→106 MiB), and LangGraph's optimized image actually
**regresses on cold-start** (583→1930 ms) because musl + skipping `.pyc` bytecode makes first import
slower. You can shrink the *image*, but you cannot make an interpreter + its loaded dependency graph
weigh what a static binary weighs. The multipliers compress; they do not vanish.

**Each framework at ITS OWN recommended packaging (researched, cited).** So the numbers can't be
dismissed as "you built them a bad image," here is what each framework's *official* Docker guidance
actually prescribes, and whether it was runnable here:

| Framework | Official/recommended packaging | Source | Measured here? |
|---|---|---|---|
| **ADK-Go** | `adkgo deploy cloudrun` compiles `CGO_ENABLED=0 GOOS=linux go build -ldflags "-s -w"` and generates `FROM gcr.io/distroless/static-debian11` + `COPY` the binary | [`cmd/adkgo/internal/deploy/cloudrun/cloudrun.go`](https://github.com/google/adk-go/blob/main/cmd/adkgo/internal/deploy/cloudrun/cloudrun.go) | **Yes — our control row IS this pattern** (static binary → distroless/static; we use debian12:nonroot vs its debian11). The 5.9 MiB is the framework's own recommended packaging. |
| **LangGraph** | `langgraph build` / `langgraph dockerfile` (langgraph-cli) generate a full **Agent Server**: the compose stack pulls `redis:6` + `pgvector/pgvector:pg16` (Postgres) + the `langchain/langgraph-api` image | [langgraph-cli docs](https://docs.langchain.com/langsmith/cli) · [`langgraph_cli/docker.py`](https://github.com/langchain-ai/langgraph/blob/main/libs/cli/langgraph_cli/docker.py) | **No — different, heavier class.** LangGraph's *own* recommended deployment is a multi-service server needing a DB, not a one-import embed. Our minimal 31–61 MiB images are a **floor below** LangGraph's official server packaging, not a penalty against it. |
| **Mastra** | `mastra build` → self-contained `.mastra/output/` (a Hono server), run `node .mastra/output/index.mjs`; no official prebuilt image, `node:22-alpine` in the Lambda example | [Mastra server deployment](https://mastra.ai/docs/deployment/mastra-server) · [AWS Lambda guide](https://mastra.ai/guides/deployment/aws-lambda) | **Approach mirrored.** Our optimized variant applies Mastra's own recipe — tree-shake to a self-contained output, ship on `node:22-alpine`, no `node_modules`. The official `mastra build` output is a *fuller* server (its own agent API routes), i.e. heavier than our 57 MiB minimal, not lighter. |

Net: for the **one** framework whose official packaging is a minimal embed (ADK-Go), we measured
exactly that packaging. For LangGraph and Mastra, the frameworks' *own* recommended deployment is a
**fuller server** than our minimal subject — so if anything our idiomatic/optimized numbers are
generous to them, a framework floor below their official server images.

**Takeaway (run-verified).** aisotropy's single static Go binary in Distroless ships at **12.4 MiB**,
**8.8 MiB** idle RSS, **262 ms** cold-start. The **structural** gap — compiled-static-binary vs
interpreter + dependency-tree — is what holds: even *optimized*, the interpreted images stay ~2.5–4.6×
the image and ~12–14× the idle RAM, and the same-language ADK-Go control shows a ~6 MiB **Go floor**
that the interpreted runtimes never approach. What is *language* (most of it) and what is *aisotropy*
(single-digit MiB of boot work) are now separated by the control, not conflated.

**Honest caveats.** (1) Every subject — including the optimized ones — is a *minimal* app with the
model stubbed offline: this is **framework+runtime footprint, not a production workload**, and says
nothing about answer quality or token throughput. (2) The aisotropy image is the repo's own
production `Dockerfile` (multi-stage → Distroless), unmodified; ADK-Go uses the identical recipe.
(3) The "optimized" competitor images are *ours*, built to find each ecosystem's honest floor — and
they visibly trade one axis for another (LangGraph-opt shrinks the image but regresses cold-start).
Where a framework has official packaging we researched and cited it (table above) rather than only
trusting our own build. (4) Cold-start is the one timing number, a real deployment property (runtime
boot: interpreter import vs. static exec), machine- and cache-sensitive — median of `RUNS` reported,
ordering (static binary << interpreted runtime) is the portable finding. (5) A full per-token
*latency/quality* wall-clock is deliberately **not** published — see the method note below for why
that would measure the models, not the frameworks.

## Efficiency across usage modes (REAL, run-verified ▶) — footprint ≠ latency

The three classes above each measure a **different way aisotropy is deployed**, and the honest
picture only appears when they are read together — not collapsed into one "how fast/small is it"
number. aisotropy is an *LLM-OS* with more than one execution granularity, and each granularity has
its own efficiency unit:

| Usage mode | Unit of work | Efficiency metric | Measured number (this repo, M4 Pro / darwin-arm64) | Source |
|---|---|---|---|---|
| **Whole runtime as a service** (Docker/K8s) | one deployed process | image + idle RSS + boot | **12.4 MiB image · 8.8 MiB RSS · 262 ms cold-start** | Class 3 · `bench/footprint/` |
| **In-process library** (embed `aisotropy.App`) | one framework hot-path op | ns/op around a (stubbed) model call | scope-gate **135 ns** · isolation key-validate **40 ns** · pre-spend quota gate **5.46 µs** · working-memory append **303 µs** | Class 1 · `arenas/scripts/run-overhead-bench.sh` |
| **Per-call pure-compute tool isolation** (WASM) | one JSON→JSON tool dispatch | warm dispatch throughput + one-time load | **461 ops/s · p50 2.06 ms/dispatch** warm; **585 ms** one-time module compile · 3.18 MiB module | NEW · `bench/wasm-vs-docker/` |
| *(same, container tier for shell/git/fs tools)* | one `docker exec` | warm exec throughput | **27 ops/s · p50 34.2 ms/exec** (N=200) | NEW · `bench/wasm-vs-docker/` |
| **WASM 3.0 tier** (WasmGC / memory64) | — | — | **N/A — unbenchmarkable on wazero v1.12.0** (Core 1.0/2.0 only; memory64 is open request wazero#2452) | — |

**The per-call isolation row is the new measurement, and it is honestly two-sided.** For the
*pure-compute* subset of tools (no shell, git, or filesystem), the WASM tier's **warm dispatch is
~17× cheaper than a `docker exec`** — **461 vs 27 ops/s**, **2.06 ms vs 34.2 ms** median — which is
the high-fan-out density argument for adding a WASM sandbox provider (research §4.1). But two costs
cut the other way and are reported, not buried: (1) the **one-time module compile is heavier than a
container create** (585 ms vs 288 ms) *because the guest is a Go/wasip1 module that bundles the whole
Go runtime* (3.18 MiB) — a hand-written WAT/Rust guest would slash both; and (2) that same Go-runtime
bundle inflates the **in-process memory** wazero holds to compile+cache the module. So the vendor
"µs cold-start / 10–100× density" numbers are for *minimal* modules — with a Go guest, the win is a
**~17× per-call throughput edge that amortizes the one-time compile**, not a free lunch on every axis.
The `sandbox.WASMProvider` that produces these numbers deliberately serves **only** `NeedPureCompute`
tools; anything needing `fork`/`exec` still routes to the container tier (WASI has no process
creation — a design boundary, not a gap). Harness + reproduce: `bench/wasm-vs-docker/` (`go run
./bench/wasm-vs-docker`); raw output in `bench/wasm-vs-docker/results.txt`.

**Honest caveats that keep the modes from being conflated.**
- **Footprint ≠ inference latency.** Class 3's 12.4 MiB / 262 ms and the WASM 2 ms/dispatch are
  *isolation-and-boot* costs with the model held constant as an offline stub. None of them say
  anything about time-to-first-token or answer quality — those are the provider's, and dominate any
  real end-to-end wall-clock. These tables measure the *mechanism*, on purpose.
- **The RSS figures are not clean per-instance numbers** (both sides). The WASM in-process RSS is
  dominated by wazero compiling the Go-runtime-bearing module (one-time, shared across all
  dispatches); the container "sub-MiB" RSS excludes the dockerd/containerd host-side memory. Treat
  both as directional, per the notes in `results.txt`.
- **Working memory is the local-coupled tier that does NOT go stateless.** The per-call WASM/edge
  efficiency story applies to *tool isolation*, never to the runtime as a whole. aisotropy's moat is
  in-process tiered memory — BadgerDB L1 at µs latency, embedded SQLite+FTS5 facts — which is
  **stateful and latency-coupled by design** (`docs/architecture/runtime-as-built.md`). Forcing the
  whole runtime into a stateless per-request model would break that tiering and gain nothing for a
  long-lived server; and it can't run under `GOOS=wasip1` anyway (no sockets → the Huma/chi server
  deadlocks). WASM helps at the *tool* granularity, not the *whole-runtime* granularity.

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
