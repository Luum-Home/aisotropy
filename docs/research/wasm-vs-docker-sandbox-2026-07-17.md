---
type: Research
title: "Research: WebAssembly (WASM) vs Docker for aisotropy isolation — 2026-07-17"
description: >
  Phase-0 read-only report on whether aisotropy considered running with WebAssembly instead of Docker for isolation.
tags: [research, wasm, docker, sandbox, isolation, phase-0]
timestamp: 2026-07-17T00:00:00Z
---

# Research: WebAssembly (WASM) vs Docker for aisotropy isolation — 2026-07-17

> **Phase 0 research report (read-only).** Operator question: *"Did we consider
> running aisotropy with WebAssembly (WASM) instead of Docker?"* This report
> grounds every aisotropy claim in a file/ADR and every external claim in a cited
> URL. It PROPOSES a Phase 2 benchmark plan; it runs no benchmark and changes no
> code.

## TL;DR verdict

The question contains a hidden assumption — that aisotropy uses "Docker instead
of WASM," a single either/or. The code says otherwise: **aisotropy already runs
both, on purpose, for two different isolation jobs.**

- **WASM is already shipped** as the *pure-compute untrusted-plugin* tier —
  `internal/wasmtool` on wazero v1.12.0 (ADR-0035).
- **Docker/Podman is deliberately chosen** for the *coding-agent-with-hands*
  tier, because that workload needs a real shell, filesystem, and git that WASM
  fundamentally cannot provide (ADR-0036/0038).

So "WASM instead of Docker" is not the right frame. The right questions are (a)
**where should WASM extend** (answer: a pure-compute *sandbox provider* for
cheap per-call tool isolation, and hardening the memory/CPU bounds of the
plugin sandbox that already exists), and (b) **where can WASM never replace
Docker** (answer: any workload needing `exec`/`fork`/shell/git/full filesystem —
i.e. the coding sandbox). Running the *whole aisotropy runtime* in WASM at the
edge is not viable today: a full Go HTTP server (Huma/chi) cannot run under
`GOOS=wasip1` because WASI preview 1 has no sockets. Details and sources below.

**On WASM 3.0** (core spec finalized 2025-09-17 — WasmGC, memory64, multiple
memories, exception handling, tail calls, deterministic profile): the features are
real on paper but **not shipped in the runtime aisotropy uses.** wazero v1.12.0 is
**Core 1.0/2.0 only** (its README says so; memory64 is an open, unimplemented
request). So 3.0 does **not** change the verdict today — it moves no workload from
"Docker required" to "WASM viable," and it gives Go guests no WasmGC benefit (Go
bundles its own GC). The verdict depends on wazero's *shipped* support, not the
spec's promises (§2f).

---

## 1. The honest current state (code-truth)

aisotropy sits on an explicit **execution-isolation axis** with two shipped
tiers plus a strength knob, all cross-referenced in the ADRs:

### 1a. WASM is ALREADY used — the pure-function plugin tier (ADR-0035, shipped)

`internal/wasmtool/wasmtool.go` runs untrusted community plugin code as a
memory-isolated, capability-gated WebAssembly module on **wazero**
(`github.com/tetratelabs/wazero` **v1.12.0**, Apache-2.0, pure Go, zero CGO —
confirmed in `go.mod`). Key code-grounded facts:

- **Sandbox posture:** by default the guest has *no* host access — no filesystem,
  no network, no clock, no env. wazero's WASI preview-1 shim is instantiated only
  so Go/TinyGo/Rust modules can initialize, and it is wired **powerless** (no
  preopens, no env, no args, deterministic clock, and wazero implements no
  networking at all) — `wasmtool.go` package doc lines 44-67.
- **Capability gate is structural:** `verifyImports` (lines 362-382) refuses at
  *load time* any module importing a host function it was not granted; the single
  optional capability is `aisotropy.log`. "A module that imports a host function
  it was not granted fails to instantiate" — the instantiation failure *is* the
  gate (ADR-0035 §3).
- **Per-call isolation:** `dispatch` (lines 268-333) instantiates a **fresh guest
  instance per call** and closes it on return, so no state and no tenant data
  bleed between calls (the constitutional multi-tenant gate).
- **ABI:** a wasip1 reactor exporting `aisotropy_alloc(size)->ptr` and
  `aisotropy_run(ptr,len)->packed(ptr,len)`, JSON in / JSON out (lines 95-107).
- **Robustness bound (with a real gap — see §5):** the runtime is built
  `WithCloseOnContextDone(true)` (line 201) so a cancelled request aborts an
  in-flight guest. But `wasmtool.go` sets **no** `WithMemoryLimitPages` and no
  per-call CPU/fuel bound — DoS containment rests entirely on the request
  context. This is a concrete hardening gap, not a hypothetical.

**A wasm tool is deliberately a pure `JSON -> JSON` function** (ADR-0035 §Non-goals):
no streaming, no I/O beyond granted capabilities, one entry point = one tool.

### 1b. Docker/Podman is DELIBERATE — the coding-agent "hands" tier (ADR-0036/0038, shipped)

`internal/sandbox` + `internal/taskagent` implement `RunTask`: an agent that runs
**real shell/git/file work against a checkout** inside a container it never fully
trusts. This is the *agentic* sibling of the WASM tier, on the same axis but a
different tier (ADR-0036 §Status).

- **Why a container and not WASM:** the workload *is* "run these argv", "edit this
  file", "git commit" — it needs a real OS process, shell, and filesystem. That is
  exactly the capability set WASM/WASI does not offer (§3 below). `internal/sandbox/sandbox.go`
  defines `Sandbox.Exec(argv)`, `CopyIn`, `CopyOut`, `Workdir` over a bind-mounted
  git worktree.
- **The defining property is key-brokering** (ADR-0036): inference runs host-side
  (`internal/llm`), so only tool *execution* is dispatched into the box; the
  provider API key never enters the container. `TestRunTask_BrokerInvariant_KeyNeverEntersContainer`
  proves it against real Docker.
- **Secure-by-default hardening** (`internal/sandbox/hardening.go`, confirmed):
  `--network=none`, `--pids-limit=256` (fork-bomb guard), `--memory=512m`,
  `--cpus=1.0`, `--read-only` rootfs + writable `/tmp` tmpfs, `--cap-drop=ALL`,
  `--security-opt=no-new-privileges`. These are the container-world analogues of
  the resource bounds the WASM tier is *missing* (§5).
- **Pluggable + strength knob** (ADR-0038): `SandboxProvider` seam is exported;
  Docker (default, works with OrbStack) and Podman (rootless, daemonless) ship as
  built-ins driven by CLI `os/exec` (no SDK, no CGO, no new dependency). The
  `Spec.Runtime` field (`sandbox.go` lines 196-215) upgrades isolation strength to
  a **microVM (Kata) or user-space kernel (gVisor)** via `--runtime=<name>` — a
  config value, not a new provider. Kubernetes/Firecracker are documented BYO
  sketches; cloud sandbox services are deliberately excluded to preserve
  self-hostability.

### 1c. Summary of the current posture

| Isolation tier | Backend (as shipped) | Workload it isolates | Source |
|---|---|---|---|
| Pure-compute plugin | **WASM (wazero)** in-process | Untrusted `JSON->JSON` tool | ADR-0035, `internal/wasmtool` |
| Coding-agent hands | **Docker/Podman** container | Untrusted shell/git/file work | ADR-0036/0038, `internal/sandbox` |
| Stronger isolation | Kata / gVisor via `Spec.Runtime` | Same as above, VM-grade | ADR-0038, `sandbox.go:196-215` |

So the honest answer to the operator: **yes, WASM was considered and adopted —
for the workload it fits. Docker was chosen for a different workload it fits
better.** The two are complementary, not alternatives.

---

## 2. Internet findings (2026, cited)

### 2a. wazero and alternative Go WASM runtimes

- **wazero** is a pure-Go, zero-CGO runtime implementing the WebAssembly Core 1.0
  and 2.0 spec; it supports **WASI preview 1 only** — it has **not** implemented
  WASI preview 2 / the Component Model. It is the de-facto default for Go
  WASM-embedding projects (Trivy, k6, dapr, OPA's wasm target, and others).
  ([pkg.go.dev/wazero](https://pkg.go.dev/github.com/tetratelabs/wazero),
  [wasmruntime.com Go guide](https://wasmruntime.com/en/languages/go))
- **Performance (2026):** an independent benchmark measured wazero roughly
  **4.7-4.8x native** and essentially flat over two years, versus **wasmtime**
  steadily improving to **~2.4x native** in 2026. wasmtime is faster and has WASI
  p2 + the Component Model, but **requires CGO** (via `wasmtime-go`), which
  complicates cross-compilation and breaks the single-static-binary posture.
  ([00f.net WASM runtimes 2026](https://00f.net/2026/06/23/webassembly-runtimes-2026/),
  [wasm-runtime-comparison](https://github.com/wasmruntime-io/wasm-runtime-comparison))
- **Trade-off for aisotropy:** wazero's pure-Go/no-CGO property is *exactly* the
  constitutional posture (single Go binary, 100% permissive). Its ~2x slower
  execution vs wasmtime is the price. For short-lived `JSON->JSON` tools this is
  usually dominated by instantiation, not steady-state compute — a Phase 2
  measurement question (§6).

### 2b. WASI preview 1 vs preview 2 / Component Model — what each forbids

- **WASI 0.2 (preview 2)** introduced the **Component Model**, plus
  `wasi-sockets` (TCP/UDP), `wasi-http`, key-value interfaces, and
  capability-scoped filesystem descriptors (a component holding a preopened
  descriptor for `/data/tenant-a` cannot open `/data/tenant-b`). The capability
  *is* the passed resource handle.
  ([eunomia.dev component-model status](https://eunomia.dev/blog/2025/02/16/wasi-and-the-webassembly-component-model-current-status/),
  [Java Code Geeks WASI 0.2](https://www.javacodegeeks.com/2026/04/webassembly-in-2026-where-it-has-landed-what-wasi-0-2-changes-and-why-java-and-kotlin-developers-should-pay-attention-now.html))
- **What WASI does NOT provide (any preview):** there is **no process creation
  (`fork`/`exec`)**, no signals, and no general IPC beyond provided pipes/sockets.
  `fork()` conflicts with the Component Model's isolation philosophy and was ruled
  out of scope; only the non-standard **WASIX** extension bolts on `fork`.
  ([WASI security roadmap](https://www.systemshardening.com/articles/wasm/wasip3-security-roadmap/),
  [WASI p2 vs WASIX](https://wasmruntime.com/en/blog/wasi-preview2-vs-wasix-2026))
- **This is the load-bearing reason the coding sandbox needs Docker:** the coding
  agent's toolset (`shell`, `git`, `write_file`) is `exec`/`fork`/full-filesystem
  by nature. No standard WASI tier offers it; WASIX is non-standard and
  wazero-unsupported. Grounded against `internal/sandbox/sandbox.go` (`Exec(argv)`).
- **WASI 0.3** refactors interfaces for native async (idiomatic concurrency);
  drafts cover clocks/random/filesystem/sockets/CLI/HTTP — still no `fork/exec`.
  ([techbytes WASI 0.3](https://techbytes.app/posts/wasi-0-3-and-beyond-webassembly-interfaces-2026/))

### 2c. Go → WASM: can the whole runtime run in WASM?

- **`GOOS=wasip1 GOARCH=wasm` is single-threaded with no socket support.** A Go
  HTTP server compiled to wasip1 **deadlocks** ("all goroutines are asleep -
  deadlock!") because WASI p1 has no sockets, so `net.Listen()` is impossible.
  ([Go WASI blog](https://go.dev/blog/wasi),
  [golang/go#62304](https://github.com/golang/go/issues/62304))
- **Concurrency:** current wasip1 lacks proper per-goroutine I/O blocking; a
  goroutine blocking on I/O can stall others. The **wasip3 proposal**
  ([golang/go#77141](https://github.com/golang/go/issues/77141)) is the first
  milestone targeting idiomatic goroutines + full TCP/UDP networking.
- **Implication for aisotropy:** its API layer is a full **chi/Huma HTTP server
  with SSE streaming** (`aisotropy/app.go`). That cannot run under wasip1 today.
  **Running the whole runtime as a WASM module at the edge is not viable in 2026**
  for the server itself. (TinyGo produces smaller binaries but shares the same
  wasip1 networking limits — [wasmruntime.com Go guide](https://wasmruntime.com/en/languages/go).)

### 2d. WASM as a sandbox vs containers vs microVMs

- **Cold start:** WASM instances start in **microseconds** (Fastly Compute reports
  cold starts **under 50µs**; Cloudflare Workers report µs-scale), vs
  **milliseconds** for containers and **seconds** for VMs. Fermyon Spin reports
  **<5ms** end-to-end cold start and claims **10-100x container density** for
  suitable workloads.
  ([Akamai/Fermyon Spin cold starts](https://www.akamai.com/blog/developers/build-serverless-functions-zero-cold-starts-webassembly-spin),
  [Gothar 5ms cloud](https://gothartech.com/en/insights/rust-wasm-containers-2025),
  [DZone WASM cloud](https://dzone.com/articles/webassembly-is-eating-the-cloud-why-devs-should-ca))
- **DoS bounding — a critical distinction:** the WASM sandbox prevents *escape*
  but does **not** by itself prevent *resource exhaustion within* the sandbox
  (infinite loop, unbounded allocation). You must add limits.
  ([systemshardening WASM](https://www.systemshardening.com/articles/wasm/))
  - **wasmtime** offers **fuel metering** (per-call instruction budget → trap) and
    **epoch interruption** (background-timer deadline the guest cannot evade),
    both purpose-built to terminate malicious modules safely.
    ([wasmtime epoch interruption](https://www.systemshardening.com/articles/wasm/wasmtime-epoch-interruption-security/),
    [wasmtime Config docs](https://docs.wasmtime.dev/api/wasmtime/struct.Config.html))
  - **wazero has NEITHER fuel nor epoch metering.** Its only CPU bound is Go
    **context cancellation** via `WithCloseOnContextDone(true)` (coarse-grained,
    checked at operation boundaries), plus **`WithMemoryLimitPages(N)`** for a hard
    linear-memory cap (default ceiling 4 GiB).
    ([wazero hardening](https://www.systemshardening.com/articles/wasm/wazero-hardening/))
- **Containers/microVMs** bound resources at the OS/hypervisor layer (cgroups
  `--memory`/`--cpus`/`--pids-limit`, seccomp, dropped caps) — which is exactly
  what `internal/sandbox/hardening.go` already applies. gVisor (user-space kernel)
  and Kata (VM per container) raise the isolation floor at a cold-start cost.

### 2e. Whole-runtime edge WASM vs aisotropy's static-binary + in-process memory model

- Edge WASM platforms (Spin, wasmCloud, Fastly Compute, Cloudflare Workers) target
  **stateless, short-lived, per-request** functions; persistent state is
  externalized to platform KV/DB services.
  ([Fermyon Cloud](https://www.fermyon.com/cloud),
  [yashchudasama WASM beyond browser](https://www.yashchudasama.com/blog/tech/wasm-beyond-browser/))
- aisotropy's moat is the **opposite**: a single static binary with **in-process,
  tiered memory** — BadgerDB L1 working memory at µs latency, embedded SQLite+FTS5
  facts, in-process cosine semantic memory (per `.claude/rules/architecture.md`
  and `docs/architecture/runtime-as-built.md`). This is *stateful and
  latency-coupled by design.* Forcing the whole runtime into a stateless per-request
  WASM model would **break the tiered-memory design** (BadgerDB is embedded, not a
  network service) and gain nothing for a long-lived server process. WASM helps at
  the *tool-isolation* granularity, not the *whole-runtime* granularity.

### 2f. WebAssembly 3.0 (core spec, Sept 2025) — and the wazero code-truth gap

**Distinguish two layers.** WASM 3.0 is the **core language/VM spec** (finalized
2025-09-17); WASI 0.2 / the Component Model (§2b) is the **system-interface**
layer. They are independent — 3.0 does *not* add `fork`/`exec` or sockets (those
are WASI concerns), so **nothing in 3.0 changes the coding-sandbox verdict.**
([webassembly.org Wasm 3.0](https://webassembly.org/news/2025-09-17-wasm-3.0/))

**What WASM 3.0 standardized** (nine production features) and why each matters
for a Go multi-tenant runtime:

| 3.0 feature | What it is | Relevance to aisotropy |
|---|---|---|
| **WasmGC** | Runtime-managed heap (struct/array types, GC) | Lets *managed* languages drop their bundled GC → leaner modules (Google Sheets cut multi-MB binaries, 2x perf). **Go does not target it** (ships its own runtime/GC) — see below. |
| **Memory64** | i64 addressing, up to 16 EiB (was 4 GiB) | Only matters for tools exceeding 4 GiB linear memory — the opposite of aisotropy's small `JSON->JSON` tools. Also *raises* the DoS ceiling (§5). |
| **Multiple memories** | One module, several linear memories | Could isolate arg/result buffers per call; marginal for the current ABI. |
| **Exception handling** | Native `try`/`throw` | Cleaner error propagation for guests; not load-bearing for `JSON->JSON`. |
| **Tail calls** | Guaranteed constant-stack calls | Interpreter/dispatch efficiency for some languages; minor here. |
| **Relaxed SIMD** | Faster vector ops (impl-defined) | Perf for numeric tools, at a determinism cost unless the deterministic profile is used. |
| **Typed function references** | Statically-typed funcrefs | Enables WasmGC/toolchain ergonomics; not user-visible here. |
| **Deterministic profile** | Spec-defined deterministic default for otherwise-nondeterministic ops (float, relaxed SIMD) | *Genuinely interesting* for multi-tenant reproducibility/fairness — same input, same result across hosts. |
([byteiota 3.0 features](https://byteiota.com/webassembly-30-spec-release/),
[x-cmd 3.0 release](https://www.x-cmd.com/blog/250924/),
[Chrome WasmGC](https://developer.chrome.com/blog/wasmgc))

**CRITICAL code-truth check — spec ≠ shipped runtime.** aisotropy runs **wazero
v1.12.0** (`go.mod`, ADR-0035). wazero's own README states it is a
*"WebAssembly Core Specification **1.0 and 2.0** compliant runtime"* — **no 3.0**,
and its only feature toggle `WithCoreFeatures()` selects between Core **V1 and
V2** only. ([pkg.go.dev/wazero](https://pkg.go.dev/github.com/tetratelabs/wazero))
Concretely:

- **Memory64: unimplemented in wazero** — feature request issue #2452 is *Open /
  enhancement*, no PR merged, as of 2025-11-30.
  ([wazero#2452](https://github.com/wazero/wazero/issues/2452))
- **WasmGC, multiple memories, exception handling, tail calls, relaxed SIMD:** not
  in the 1.0/2.0 compliance surface and not documented as supported by wazero.
  (Note: fixed 128-bit SIMD *is* part of Core 2.0 and thus available; **relaxed**
  SIMD is the 3.0 addition and is not.)

**So the WASM 3.0 features are paper capabilities for aisotropy today.** The
robustness/performance verdict rests on **what wazero implements (Core 1.0/2.0),
not on what the 3.0 spec promises.** Adopting any 3.0 feature would require either
waiting for wazero to implement it or switching runtimes to wasmtime/WasmEdge
(which are further along on 3.0 features) — and wasmtime's route (`wasmtime-go`)
**requires CGO**, breaking the single-static-binary thesis. This is a real,
named gap, not a detail.

**Does 3.0 change the Go-in-WASM story?** No, for two independent reasons: (1) the
whole-runtime blocker is **WASI sockets** (§2c), a system-interface gap 3.0 does
not touch; (2) **mainline Go and TinyGo do not compile to WasmGC** — Go bundles
its own runtime and GC into the module, so WasmGC's "drop the bundled GC" benefit
accrues to managed languages (Java/Kotlin/Dart), not Go.
([Java Code Geeks 2026 landing](https://www.javacodegeeks.com/2026/04/webassembly-in-2026-where-it-has-landed-what-wasi-0-2-changes-and-why-java-and-kotlin-developers-should-pay-attention-now.html))
A full Go HTTP server / the aisotropy runtime is **no more runnable in WASM under
3.0 than under 2.0.**

**Net effect on the isolation/perf verdict:** WASM 3.0 does **not** move any
aisotropy workload from "Docker required" to "WASM viable" *today*, because the
one runtime aisotropy uses does not implement 3.0. The deterministic profile and
memory64 are the two features worth tracking for the *pure-compute plugin* path
(reproducibility; large-memory tools) — but only once wazero ships them.

---

## 3. Decision matrix — workload class → recommended isolation

| Workload class | Needs | Recommended isolation | Robustness + performance rationale | aisotropy status |
|---|---|---|---|---|
| **Pure-compute untrusted tool** (`JSON->JSON`, no I/O) | Memory isolation, µs dispatch, capability gate | **WASM (wazero)** | µs cold start, in-process, no ambient authority, no subprocess/port; ~2x slower steady-state than wasmtime but no-CGO fits the single-binary thesis. Add memory + deadline caps (§5). | **Shipped** — `internal/wasmtool` (ADR-0035) |
| **Coding agent needing shell + git + fs** | `exec`/`fork`, real filesystem, git | **Docker/Podman container** | WASI has no `fork`/`exec` — WASM *cannot* run this. Container gives a real OS process + cgroup/seccomp/cap bounds; key stays host-side (broker invariant). | **Shipped** — `internal/sandbox` (ADR-0036/0038) |
| **Per-call cheap isolation for hostile compute at high fan-out** | Sub-ms create/destroy, dense multi-tenant | **WASM provider** (proposed) | Container cold start (ms) dominates for tiny, frequent calls; a WASM sandbox provider gives µs create + 10-100x density. Only valid for the pure-compute subset. On wazero's **Core 1.0/2.0** surface — WASM 3.0 (GC/memory64/etc.) is spec-real but **not in wazero v1.12.0** (§2f), so it adds nothing here yet. | **Extension gap** (§4) |
| **Whole-runtime edge deploy** | HTTP server, SSE, in-proc memory tiers | **Container (static binary)** — NOT WASM | `GOOS=wasip1` has no sockets → Huma/chi server deadlocks; edge WASM is stateless per-request, which breaks BadgerDB in-process tiering. | Container is correct; keep it |
| **Maximum-strength untrusted hands** (adversarial coding agent) | Kernel-level isolation | **microVM: gVisor (`runsc`) / Kata (`kata-runtime`)** | Shared-kernel container is a wider attack surface for truly hostile code; a user-space kernel or VM-per-task narrows it, at a cold-start cost. | **Config knob shipped** — `Spec.Runtime` (ADR-0038) |

---

## 4. Where WASM SHOULD extend in aisotropy

1. **A WASM sandbox *provider* for the pure-compute subset of the "hands" tier.**
   Today `SandboxProvider` (ADR-0038) has only container backends (Docker/Podman)
   and the microVM knob. For tool calls that are **pure computation with no
   shell/git/fs need**, a wazero-backed provider would give **µs create/destroy**
   instead of container-ms, and far higher multi-tenant density
   ([Spin density claims](https://gothartech.com/en/insights/rust-wasm-containers-2025)).
   This slots into the *existing* exported seam — no kernel change — and is the
   natural convergence of the two isolation tiers (ADR-0035's pure-function tier
   reaching into ADR-0038's provider abstraction). **Scope guard:** it can only
   serve `JSON->JSON` tools; anything needing `exec`/`fs` still routes to Docker.

2. **Harden the plugin sandbox's DoS bounds (fixes a real, code-grounded gap).**
   `wasmtool.go:201` sets `WithCloseOnContextDone(true)` but no
   `WithMemoryLimitPages` and no per-dispatch deadline. A hostile plugin can
   allocate toward wazero's 4 GiB linear-memory ceiling, or spin CPU until the
   request context happens to cancel. Recommended (all pure-config, no new
   dependency): add `WithMemoryLimitPages(N)` to the runtime config and wrap each
   `dispatch` in a bounded `context.WithTimeout`, mirroring the container tier's
   `--memory`/`--cpus` defaults in `hardening.go`. This is an ADR-worthy widening
   because it changes the plugin trust surface. ([wazero hardening](https://www.systemshardening.com/articles/wasm/wazero-hardening/))

3. **Watch WASI 0.3 / `GOOS=wasip3`** ([golang/go#77141](https://github.com/golang/go/issues/77141))
   for idiomatic goroutines + sockets. It does **not** unlock the coding sandbox
   (still no `fork`/`exec`), but it could make richer, mediated-I/O WASM tools
   (a capability-scoped HTTP fetch) feasible in-process — each still a deliberate,
   enumerated capability widening per the ADR-0035 discipline.

4. **Track wazero's WASM 3.0 adoption — do not assume it (§2f).** The two 3.0
   features worth aisotropy's attention are the **deterministic profile**
   (reproducible, tenant-fair pure-compute) and **memory64** (large-memory tools).
   Both are **unimplemented in wazero v1.12.0** (memory64 is an open request,
   [wazero#2452](https://github.com/wazero/wazero/issues/2452)). Action is to
   *monitor the wazero changelog*, not to design around 3.0 now — and to record
   that switching to a 3.0-capable runtime (wasmtime/WasmEdge) trades away the
   no-CGO single-binary posture. WasmGC specifically offers Go **no** benefit
   (Go bundles its own GC), so it is not a reason to move.

## Where WASM CANNOT replace Docker

- **The coding-agent "hands" tier** (`shell`/`git`/`write_file` over a real
  checkout). WASI has no `fork`/`exec` and no general filesystem; WASIX (which
  adds `fork`) is non-standard and unsupported by wazero. This is not a temporary
  gap — it is a design boundary of the Component Model.
  ([WASI p2 vs WASIX](https://wasmruntime.com/en/blog/wasi-preview2-vs-wasix-2026))
- **The whole runtime** (Huma/chi HTTP + SSE + in-process BadgerDB tiers). No
  sockets under wasip1; stateless edge-WASM model is incompatible with the
  in-process tiered-memory moat. ([Go WASI blog](https://go.dev/blog/wasi))

---

## 5. Honest risks and limits

- **wazero has no fuel/epoch metering.** Unlike wasmtime, DoS bounding is
  coarse-grained (context boundary checks). The proposed memory-page cap + per-call
  deadline mitigate but do not match wasmtime's per-instruction fuel precision. If
  precise CPU accounting per tenant becomes a hard requirement, that is a
  runtime-choice reconsideration (wasmtime-go, at the cost of CGO and the
  single-binary thesis). ([wazero hardening](https://www.systemshardening.com/articles/wasm/wazero-hardening/),
  [wasmtime epoch](https://www.systemshardening.com/articles/wasm/wasmtime-epoch-interruption-security/))
- **wazero is ~2x slower than wasmtime** in steady-state compute
  ([00f.net 2026](https://00f.net/2026/06/23/webassembly-runtimes-2026/)). For
  short tool calls this is likely instantiation-dominated, but it is unmeasured
  in-repo (§6).
- **A WASM provider is not a drop-in for the existing container tier** — it serves
  a strict subset (pure compute). Mis-routing a shell-needing tool to it would
  fail; routing must classify tools by capability need.
- **Uncertainty in external cold-start figures.** Vendor blog numbers (Fastly
  <50µs, Spin <5ms) are marketing-adjacent and workload-specific; treat them as
  order-of-magnitude, confirm the relevant ones in Phase 2.
- **wazero linear-memory ceiling is 4 GiB** regardless of config — an absolute
  bound, relevant to the memory-cap design. ([wazero hardening](https://www.systemshardening.com/articles/wasm/wazero-hardening/))
  (Note: WASM 3.0 **memory64** would *raise* this ceiling to exabyte scale — which,
  for a DoS-bounding context, is a reason to be cautious about adopting it for
  untrusted plugins, not an unqualified win. It is unimplemented in wazero anyway, §2f.)
- **WASM 3.0 features are spec-real but not shipped in wazero v1.12.0** (§2f):
  GC, memory64, multiple memories, exception handling, tail calls, relaxed SIMD.
  Any plan that assumes them would be building on paper capability. Verified
  against wazero's own README (Core 1.0/2.0 only) and issue #2452 (memory64 open).

---

## 6. PROPOSED Phase 2 benchmark plan (DO NOT RUN YET — operator approval required)

**Goal:** measure, for the *pure-compute untrusted-tool* workload, WASM-tool-exec
(wazero, existing `internal/wasmtool`) vs Docker-tool-exec (existing
`internal/sandbox` `RunTask` with a trivial argv), so the "add a WASM provider"
decision (§4.1) rests on repo-grounded numbers, not vendor blogs.

**Reuse the existing harness pattern.** `bench/footprint/` already contains an
executed, reproducible harness (`run.sh`, `results.txt`, distroless-static
methodology) measuring image size / cold-start / idle RSS with the model held as
an offline stub. Extend that discipline; do not invent a new one.

### What to measure (per candidate: `wasmtool` vs `docker exec`)

| Metric | Method | Expected order-of-magnitude (to confirm) |
|---|---|---|
| **Cold start / first dispatch** | Time from "tool ready" to "first result" for a trivial `echo`-equivalent tool. WASM: `New` + first `dispatch`. Docker: `docker run` + `docker exec`. | WASM µs-to-low-ms; Docker 100s of ms-to-seconds. Gap = the §4.1 thesis. |
| **Warm per-op throughput** | N=10k sequential trivial dispatches; report ops/sec + p50/p95/p99. WASM: fresh-instance-per-call (as `dispatch` does today). Docker: `docker exec` into a warm container. | WASM ≫ Docker for warm calls; quantify. |
| **Steady-state compute cost** | A CPU-bound kernel (e.g. fixed-iteration hash) run identically as a wasm tool and as a native/container binary; report WASM-vs-native ratio. | ~4-5x native for wazero (per 00f.net); confirm in-repo. |
| **Memory RSS per instance** | Host RSS delta per live WASM instance vs per idle container (reuse `bench/footprint` RSS method). | WASM KB-to-MB; container 10s of MB. Density argument. |
| **Isolation under adversarial payload** | Run 4 hostile fixtures in each: (a) infinite loop, (b) unbounded `malloc`, (c) attempted host-file read, (d) attempted socket open. Assert: escape prevented AND host stays healthy. | WASM: (c)(d) already denied (existing tests); (a)(b) expose the §5 gap unless memory-cap + deadline added first. Docker: cgroup `--memory`/`--pids`/`--cpus` + `--network=none` bound all four. |
| **WASM 3.0 (WasmGC) module vs wasip1 Go module vs Docker** | Would compare a WasmGC-compiled module against a `GOOS=wasip1` Go module and a container. | **UNBENCHMARKABLE ON wazero v1.12.0 TODAY (§2f):** wazero implements Core 1.0/2.0 only — no WasmGC, no memory64. This row is *deferred*; running it would require a runtime switch (wasmtime/WasmEdge, which costs CGO) purely to benchmark, which is out of scope. Revisit when wazero ships 3.0 features. Also note Go itself does not emit WasmGC, so the "leaner module" premise does not apply to Go guests regardless of runtime. |

### Method notes / honesty gates

- **Hold the model constant** as an offline stub (the `bench/footprint` rule) — this
  measures isolation mechanism, not model behavior.
- **Docker-availability gate:** container benchmarks `t.Skip`/abort cleanly when the
  `docker` CLI is absent, matching the existing `internal/sandbox` test pattern.
- **Report the machine + versions** (Go, wazero v1.12.0, Docker/OrbStack) and commit
  the raw numbers next to `bench/footprint/results.txt`.
- **The adversarial row is the point.** Cold-start and throughput will favor WASM by
  construction; the decisive question is whether the WASM tier can be made *as
  robust* as the container tier's cgroup bounds. Run row (a)/(b) **twice** — once on
  `wasmtool` as-is, once with the §4.2 memory-cap + deadline patch — to prove the
  hardening closes the gap before recommending a WASM provider for hostile compute.
- **Do not benchmark the whole runtime in WASM** — §2c establishes it does not run
  (no sockets under wasip1); there is nothing to measure.

### Open questions for operator triage (answer before Phase 2)

1. Is the target a **WASM sandbox provider** (§4.1), **hardening the existing plugin
   sandbox** (§4.2), or both? They are separable.
2. Is precise per-tenant CPU accounting a hard requirement? If yes, the wazero →
   wasmtime (CGO) trade-off must be an explicit decision, not assumed away.
3. Acceptable per-instance memory-page cap for `WithMemoryLimitPages`, and per-call
   deadline — should they mirror `hardening.go` defaults (512m / 1.0 CPU) or be
   tighter for pure-compute?
4. **Is any WASM 3.0 feature a hard requirement?** If so (e.g. the deterministic
   profile for tenant-fair reproducibility, or memory64), that forces a runtime
   decision *away from wazero* (which is Core 1.0/2.0 only, §2f) and toward a
   CGO-bearing runtime — an explicit trade against the single-binary thesis, to be
   decided deliberately, not assumed. Default recommendation: **stay on wazero,
   monitor its 3.0 changelog, do not design around 3.0 now.**

---

## References

**Code / ADRs (aisotropy):**
`docs/decisions/0035-wasm-plugin-sandbox.md`,
`docs/decisions/0036-sandboxed-task-agent-key-brokering.md`,
`docs/decisions/0038-pluggable-sandbox-providers.md`,
`internal/wasmtool/wasmtool.go` (esp. lines 199-234, 362-382),
`internal/sandbox/sandbox.go`, `internal/sandbox/hardening.go`,
`bench/footprint/` (existing harness), `go.mod` (wazero v1.12.0),
`.claude/rules/architecture.md`.

**External (all URLs cited inline above):** wazero pkg.go.dev & hardening;
00f.net 2026 runtime benchmark; wasm-runtime-comparison; WASI 0.2/0.3 status
(eunomia, Java Code Geeks, techbytes, wasmruntime.com); wasip3 security roadmap;
Go WASI blog + golang/go#62304 + #77141; Fermyon/Akamai Spin cold start; Gothar;
DZone; wasmtime epoch interruption + Config docs.
**WASM 3.0 (§2f):** webassembly.org "Wasm 3.0 Completed" (2025-09-17);
byteiota 3.0 nine features; x-cmd 3.0 release; developer.chrome.com WasmGC;
wazero README (Core 1.0/2.0 compliance — pkg.go.dev); wazero issue #2452
(memory64 unimplemented); Java Code Geeks 2026 landing (Go/WasmGC).

---

TRUST_REPORT: SCORE=83 STATUS=MEDIUM EVIDENCE=8 UNCERTAINTIES=4
---
Score: 83/100

WHAT I VERIFIED:
  - Read ADR-0035/0036/0038 in full; grounded every tier claim in their text.
  - Read `internal/wasmtool/wasmtool.go` (full) — confirmed wazero v1.12.0,
    `WithCloseOnContextDone(true)` at line 201, fresh-instance-per-dispatch, the
    `verifyImports` capability gate, and the alloc/run ABI. Confirmed via grep that
    NO `WithMemoryLimitPages` is set (the §5 hardening gap is code-real, not
    inferred).
  - Read `internal/sandbox/sandbox.go` (full) + grepped `hardening.go` — confirmed
    the secure-default flags (network=none, pids 256, mem 512m, cpus 1.0,
    read-only rootfs, cap-drop=ALL, no-new-privileges) and the `Spec.Runtime`
    Kata/gVisor knob.
  - Confirmed `go.mod` pins `github.com/tetratelabs/wazero v1.12.0` and
    `bench/footprint/` is a real executed harness (run.sh + results.txt + README).
  - WASM 3.0 code-truth: verified against wazero's own README (via pkg.go.dev) that
    it claims **Core 1.0/2.0 compliance only** and `WithCoreFeatures()` toggles
    V1/V2 only — no 3.0. Verified memory64 is unimplemented (wazero issue #2452,
    Open/enhancement, 2025-11-30). So the 3.0 feature set is paper-capability for
    aisotropy, not shipped — stated explicitly, not assumed.
  - WASM 3.0 spec scope (WasmGC/memory64/multiple memories/exception handling/tail
    calls/deterministic profile) sourced to webassembly.org's official release note
    + secondary summaries; Go-does-not-emit-WasmGC sourced, not inferred.
  - Every external claim (wazero WASI-p1-only, no fuel/epoch; wasip1 no sockets →
    HTTP server deadlock; WASI has no fork/exec; cold-start µs vs ms) is backed by
    a cited 2026 URL, not memory.

UNSURE ABOUT:
  - External cold-start / density figures are vendor-blog order-of-magnitude
    (Fastly <50µs, Spin <5ms, 10-100x density) — directionally reliable, not
    audited numbers. Phase 2 must confirm the ones that drive the decision.
  - I did not read `internal/taskagent/runtask.go` line-by-line (relied on ADR-0036
    + the sandbox interface); the RunTask control flow is ADR-grounded, not
    fully code-verified.
  - The "~4.7x native wazero vs ~2.4x wasmtime" figure is from one independent
    benchmark (00f.net); I did not cross-check a second source with identical
    methodology. wazero may have added an *experimental* 3.0 feature since its
    README was last edited — I trust the README's headline claim but did not audit
    every merged PR.
  - I did not verify whether any in-repo issue/ADR already tracks the wasmtool
    memory-cap gap — it may be known/deferred elsewhere I did not search.

HUMAN SHOULD CHECK:
  - The §4.2 claim that the plugin sandbox lacks a memory cap — confirm no other
    layer (loader/registry) imposes one before treating it as a live gap.
  - Whether a WASM sandbox provider (§4.1) is in scope now or a later phase.
  - Whether any WASM 3.0 feature (deterministic profile / memory64) is a hard
    enough requirement to justify reconsidering wazero (§2f, open question 4).
  - The Phase 2 plan's adversarial-row design before any benchmark runs.

**Top-line verdict:** WASM was not skipped in favor of Docker — aisotropy ships
BOTH deliberately (WASM for pure-compute plugins per ADR-0035; Docker/Podman for
the shell+git coding agent per ADR-0036/0038, because WASI has no fork/exec).
"WASM instead of Docker" is a false binary. **WebAssembly 3.0 (Sept 2025) does not
change this today:** its features (WasmGC, memory64, deterministic profile, …) are
spec-real but unimplemented in wazero v1.12.0 (Core 1.0/2.0 only, memory64 an open
request), and WasmGC gives Go guests no benefit — so no workload moves from
"Docker required" to "WASM viable." The real opportunities remain (1) a WASM
sandbox *provider* for cheap per-call pure-compute isolation and (2) closing a
real DoS-bounding gap in the existing plugin sandbox (no memory cap / no
fuel-equivalent). WASM cannot replace Docker for the coding tier, and the whole
runtime cannot run in WASM today (no sockets under wasip1; 3.0 does not add them).
