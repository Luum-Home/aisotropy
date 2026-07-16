---
type: ADR
title: "ADR-0035 — The WASM plugin sandbox: running an untrusted community tool in-process without handing it the process"
description: ADR-0032 reserved `kind: wasm` as the "sandboxed" plugin tier and named it a follow-on; ADR-0033's trust model stated the honest constraint that Go cannot sandbox in-process code, so untrusted tools need a real isolation boundary (subprocess or WASM). THIS ADR ships the WASM boundary. A plugin declaring `kind: wasm` is compiled and run by internal/wasmtool inside a wazero (github.com/tetratelabs/wazero v1.12.0, Apache-2.0, pure Go, zero CGO) sandbox that has NO ambient access to the host: no filesystem, no network, no clock, no environment — the module can only compute over the JSON args it is handed and return JSON. Host capabilities are opt-in and enumerated (today: an optional `aisotropy.log`); a module that imports a host function it was not granted fails to instantiate, and that instantiation failure IS the capability gate. The guest contract is a small, versioned ABI (a wasip1 reactor exporting `aisotropy_alloc(size)->ptr` and `aisotropy_run(ptr,len)->packed(ptr,len)`), so any language that targets wasip1 (Go, TinyGo, Rust) can author a tool. The result: aisotropy gains a memory-isolated, capability-gated, in-process execution boundary for untrusted plugin code — the strong tier the earlier ADRs promised.
tags: [decisions, adr, plugins, wasm, wazero, sandbox, capability-gated, untrusted-code, isolation, phase-9]
timestamp: 2026-07-16T00:00:00Z
---

# ADR-0035 — The WASM plugin sandbox: running an untrusted community tool in-process without handing it the process

## Status

Accepted — shipped. `internal/wasmtool` (the wazero runner + ABI + capability gate) and the
`kind: wasm` arm of `internal/plugin`'s loader are in the tree with tests. `github.com/tetratelabs/wazero`
v1.12.0 (Apache-2.0) is the only new dependency.

## Context

ADR-0032 built the plugin ecosystem (manifest + loader + registry + CLI) and reserved
`kind: wasm` as a **named follow-on** — the loader returned "not yet supported" for it. ADR-0033's
trust model stated the uncomfortable truth plainly: **Go cannot sandbox in-process code.** A Go
`kind: go-import` tool and even an in-process typed tool run with the full authority of the host
process — they are for *first-party, audited* code only (`trust: trusted-inproc`). For an
**untrusted community tool** (`trust: sandboxed`) the only real boundaries are:

1. a **separate process** (`kind: mcp`, already shipped) — isolated by the OS, sandboxable by the
   operator (seccomp / landlock / a container), but heavyweight and out-of-process; and
2. a **WebAssembly sandbox** — memory-isolated *in-process*, where the guest can reach nothing the
   host does not explicitly export.

This ADR delivers (2): the strong in-process tier for untrusted plugin code.

## Goals

- Run an untrusted plugin tool **in-process** with **no ambient host authority**: no filesystem, no
  network, no clock, no environment, no syscalls — only compute over the arguments it is handed.
- Make every host capability **opt-in and enumerable**, so the trust posture of a plugin is a
  reviewable list, not a leap of faith.
- Keep the guest contract **language-agnostic** (any wasip1 target: Go, TinyGo, Rust) and **small**.
- Add **only permissively-licensed** dependencies (constitutional license gate).
- Plug a WASM tool into the **same** scope-gated tool registry as MCP / typed / Go tools — a wasm
  tool is still gated by its manifest `requiredScope`; the sandbox is a *second* line of defense, not
  a replacement for scope-gating.

## Non-goals

- **Not** a general compute platform: a wasm tool is a pure function `JSON -> JSON`. Streaming,
  long-running processes, and stateful services remain the `kind: mcp` (subprocess) story.
- **Not** a filesystem/network capability set yet: the only host capability shipped is an optional
  `aisotropy.log`. Richer, still-enumerated capabilities (a scoped KV, an HTTP-fetch mediated by the
  host) are future work — each new capability is a deliberate ADR-worthy widening of the trust surface.
- **Not** a replacement for tenant isolation: WASM isolates *execution of code*; tenant *data*
  isolation is still the runtime's ctx-scoping + the ADR-0034 sealed drivers. Different axes.

## Decision

### 1. The runner: wazero, pure Go, zero CGO

`internal/wasmtool` runs guest modules on **wazero** (`github.com/tetratelabs/wazero` v1.12.0,
**Apache-2.0**). wazero is a pure-Go WebAssembly runtime with **no CGO** — it fits the "single Go
binary, 100% permissive" thesis exactly: no native toolchain, no shared library, no supply-chain
surface beyond one Apache-2.0 module. `wasmtool.New` compiles the module, verifies it satisfies the
ABI (exports a memory + the entry points), and returns a `*Tool` that adapts the module into an
ordinary Aisotropy tool.

### 2. The ABI (host ⇄ guest contract)

A wasm tool module is a **wasip1 reactor** that exports two functions (names overridable via
`Config`, defaults shown):

- `aisotropy_alloc(size i32) -> ptr i32` — the guest allocates `size` bytes in its own linear
  memory and returns the offset; the host writes the JSON argument bytes there. The guest owns its
  allocator and keeps the buffer alive until `aisotropy_run` reads it.
- `aisotropy_run(ptr i32, len i32) -> packed i64` — reads the JSON args at `[ptr, ptr+len)`, runs
  the tool, and returns a single i64 packing the `(ptr, len)` of the JSON result in guest memory,
  which the host reads back.

JSON in, JSON out, across a linear-memory boundary the host controls. Any language that targets
wasip1 (Go/TinyGo/Rust) can author one; the reference fixtures (`testdata/echomod`, `testdata/logmod`)
are Go, compiled to `.wasm` **at test time** via `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared`
rather than checked in as opaque binaries.

### 3. The capability gate (the isolation guarantee)

The default sandbox has **no** host access. WASI preview1 is wired **only** so a wasip1 module can
initialize — it is a load-time necessity, deliberately **not** a capability escape (no WASI fs/clock/
random/args/env are surfaced to do work). Host capabilities are an explicit, enumerated set
(`Capabilities`): today the single optional `aisotropy.log`, exported into the `aisotropy` host
module only when the operator grants it.

The gate is structural, not a runtime check that can be forgotten: **a module that imports a host
function it was not granted fails to instantiate.** A guest that `import`s `aisotropy.log` without
the `Log` capability cannot even be loaded — instantiation failure *is* the denial. This is proven
by `TestTool_LogCapabilityGranted` / `TestTool_LogCapabilityDenied`, alongside
`TestTool_ComputesAndIsSandboxed` (a pure-compute module runs and cannot reach the host),
`TestTool_ArgsValidatedAgainstSchema`, and `TestNew_RejectsModuleMissingABI`.

### 4. Loading a `kind: wasm` plugin

`internal/plugin`'s loader now handles `KindWASM`: `loadWASM` reads the module bytes named by the
manifest's `Run.Module`, loads it into the wazero sandbox, and adapts its single ABI entry point into
**one** scope-gated tool (identity from the manifest's declared tool, else the manifest itself). A
wasm plugin exposes exactly one entry point, so it contributes at most one tool — richer surfaces
remain the MCP path. The tool merges into the same registry, gated by the manifest `requiredScope`,
exactly like every other tool.

## Consequences

- **The "sandboxed" trust tier is now real in-process.** A community author can ship a `kind: wasm`
  plugin, and the runtime can run it without granting it the host — the promise ADR-0032/0033 made
  as a follow-on is delivered.
- **A plugin's power is a reviewable list.** Its `requiredScope` (which the model may invoke it) plus
  its granted `Capabilities` (what the host lets it touch) are both explicit. Absent any capability,
  the worst a hostile wasm tool can do is compute a wrong answer to the arguments it was handed.
- **Language-agnostic authorship** without a proprietary format: the ABI is a handful of wasip1
  exports, not an Aisotropy SDK lock-in.
- **One permissive dependency** (wazero, Apache-2.0, no CGO) — the license and single-binary posture
  is preserved.
- **Honest limits.** A wasm tool is a pure `JSON -> JSON` function; it has no I/O beyond granted
  capabilities and cannot stream — that is by design, and stateful/streaming tools stay on `kind: mcp`.
  The capability set is intentionally minimal today; each future capability (KV, mediated fetch) is a
  deliberate widening recorded in its own ADR, not an open door.

## References

- ADR-0032 — Plugin ecosystem (reserved `kind: wasm`, the trust model).
- ADR-0033 — Open-core extension seams (why untrusted code needs a real boundary; Go cannot sandbox
  in-process).
- ADR-0034 — The Tier-2 sealed-driver (the *data*-isolation axis; WASM is the *execution*-isolation
  axis — different guarantees).
- `internal/wasmtool/` — the wazero runner, the ABI, the capability gate, and the tests.
- `internal/plugin/loader.go` — `loadWASM`, the `kind: wasm` arm.
- `github.com/tetratelabs/wazero` v1.12.0 (Apache-2.0).
