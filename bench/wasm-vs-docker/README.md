# wasm-vs-docker benchmark

Head-to-head measurement of the **WASM pure-compute isolation tier** (wazero, via
`internal/sandbox.WASMProvider` over `internal/wasmtool`) against the **container
"hands" tier** (`internal/sandbox.DockerProvider`) for the **same trivial work** —
a JSON→JSON echo tool. It is the Phase-2 companion to `bench/footprint/` and
grounds the "add a WASM provider for cheap per-call pure-compute isolation"
recommendation (`docs/research/wasm-vs-docker-sandbox-2026-07-17.md` §4.1, §6) in
repo numbers rather than vendor blogs.

## Phase-2 follow-up — the lean-guest question

The first pass compiled the guest with **Go/wasip1**, which bundles the **entire Go
runtime** into a 3.18 MiB module. WASM won warm throughput by ~17× but *lost*
cold-start (a ~585 ms module compile) and RSS. That left one question measured-but-
unanswered: **does a lean guest (TinyGo / Rust) plus wazero's `CompilationCache`
make WASM dominate Docker on ALL axes for pure compute?**

This harness answers it by building the **same echo tool three ways** and measuring
them side by side:

| Guest | Build | Module | Imports |
|---|---|---|---|
| `go` | `GOOS=wasip1 go build` (baseline) | ~3.17 MiB — bundles the Go runtime | WASI |
| `tinygo` | `tinygo build -target=wasip1 -buildmode=c-shared` — **same source**, LLVM backend | ~1.05 MiB | WASI |
| `rust` | `rustc --target wasm32-wasip1 -C opt-level=z --crate-type cdylib -C panic=abort` — `#![no_std]`, dependency-free | ~4.9 KiB | **none** |

The Go and TinyGo guests share the **identical source** (`testdata/guest/main.go`,
`encoding/json`), so the go-vs-tinygo delta isolates the **toolchain**. The Rust
guest is a different language with a **hand-rolled minimal parser** (no serde), so
its size/RSS win is *partly* language and *partly* a leaner parse — stated in the
output, not hidden.

## What it measures

Per guest (`go`, `tinygo`, `rust`) and `docker`, holding work constant:

| Metric | WASM method | Docker method |
|---|---|---|
| **Cold start / first dispatch** | `Load` (compile module) + first `Dispatch` | `Create` (`docker run -d`) + first `Exec` |
| **Warm per-op throughput** | N sequential fresh-instance dispatches → ops/s + p50/p95/p99 | N sequential `docker exec` → ops/s + p50/p95/p99 |
| **Host RSS per live instance** | RSS delta to hold one compiled tool live, measured in an **isolated subprocess** (see below) | one idle container's `docker stats` memory |
| **`CompilationCache` effect** | one-time module compile with an **empty** cache vs a later compile sharing a **warm** cache | — |

**RSS is measured in a fresh subprocess per guest** (`-rss-only=<name>`, spawned by
the parent). Measuring all three in one process is worthless for RSS: process RSS is
monotonic, so whichever guest compiles first absorbs the one-time wazero-engine
allocation and the rest read ~0. Isolating each guest in its own process gives a
clean, comparable "hold one compiled tool live" figure.

## The verdict (does WASM kill Docker for pure compute?)

Axis by axis, from a representative run (`results.txt` for the exact numbers):

- **Throughput** — WASM wins outright, every guest: **1–2 orders of magnitude** over
  `docker exec` (which forks the CLI + attaches to containerd per call).
- **Cold-start** — the Go guest's uncached 3 MiB compile is WASM's one soft spot (it
  can trail a warm `docker exec`). Two fixes each land it decisively ahead: a **lean
  guest** (TinyGo ~140 ms, Rust ~2 ms) already beats Docker's create+first-exec, and
  a **warm `CompilationCache`** collapses *every* guest's effective cold-start to
  single-digit ms or less (the compile is a per-*process*, not per-*tool*, cost).
- **RSS** — the subtle one. A single container's `docker stats` (~1 MiB) can look
  *smaller* than a lean wasm tool's in-process delta — but that container number
  **excludes** the dockerd/containerd daemon (hundreds of MiB, always resident). The
  wasm figure is the **complete** marginal cost, and the wazero engine (the bulk of
  the lean-guest delta) is a **one-time cost shared across all tools** in the process.
  Per-instance-at-scale and full-host-cost, lean-guest WASM wins RSS; the single-
  container raw stat is the only framing where Docker looks smaller, and only because
  it hides its daemon.

**Answer:** for pure compute, a **lean guest (TinyGo/Rust) + warm `CompilationCache`**
makes WASM beat Docker on throughput and cold-start outright, and on RSS once the
daemon is counted. The Go-runtime-bundling guest was the whole reason the first pass
saw a cold-start/RSS loss — swapping it for a lean guest erases it.

**The caveat that does not move:** this is **only** the pure-compute (JSON→JSON)
subset. The coding-agent hands (shell, git, `fork`/`exec`) **cannot** run in WASI —
Docker stays the tier for those. WASM adds a cheap compute tier; it does not delete
the container tier.

## Honesty gates (why the numbers are trustworthy — and their limits)

- **The WASM tier serves only the pure-compute (JSON→JSON) subset.** It **cannot**
  run the coding-agent hands. This bench compares the two on the pure-compute work
  they can *both* do; it is **not** a claim that WASM replaces Docker.
- **Same source across Go and TinyGo** isolates the toolchain; the Rust guest's
  different-language/leaner-parse caveat is printed alongside its numbers.
- **A missing toolchain skips that guest** with the reason printed (`SKIPPED: …`) —
  never faked. TinyGo (`brew install tinygo-org/tools/tinygo`) and a Rust toolchain
  with the `wasm32-wasip1` target (`rustup target add wasm32-wasip1`, or set
  `RUSTC=`) are optional; without them the harness reports whichever guests it built.
- **Docker warm N is smaller by default** (`-docker-n 200` vs `-n 10000`): each
  `docker exec` forks the docker CLI and attaches to containerd (~tens of ms). The
  reduced N is printed next to the number, not hidden.
- **Every RSS number carries a caveat** (printed in full): the wasm figure includes
  the shared wazero engine; the docker figure excludes the shared daemon.
- **WASM 3.0 rows (WasmGC / memory64) are UNBENCHMARKABLE on wazero v1.12.0** (Core
  1.0/2.0 only; memory64 is open request `wazero#2452`). The harness emits an explicit
  `N/A` line rather than faking a row.
- **The Docker path skips cleanly** when the `docker` CLI or the image is absent.

## Toolchains & licenses

All three toolchains are permissively licensed (matching the repo's 100% permissive
(MIT/BSD/Apache-2.0/ISC), zero-copyleft posture — `.claude/rules/constitutional-gates.md`):

| Toolchain | License |
|---|---|
| Go / wazero | BSD-3-Clause / Apache-2.0 |
| TinyGo (+ bundled LLVM, bdwgc) | BSD-3-Clause (LLVM Apache-2.0-with-exception, bdwgc MIT-style) |
| Rust toolchain + `wasm32-wasip1` std | MIT / Apache-2.0 |

## Reproduce

Run from the **repo root** (the harness shells out to the three compilers and
resolves guest dirs relative to the CWD). TinyGo and `rustc` must be on `PATH`
(or point `RUSTC=` at a `rustc` whose `wasm32-wasip1` std is installed):

```sh
# default: WASM N=10000, docker N=200, 5 cold/compile samples, image alpine:3.20
go run ./bench/wasm-vs-docker

# tune sample sizes
go run ./bench/wasm-vs-docker -n 10000 -docker-n 200 -cold-runs 5

# WASM only (no Docker needed)
go run ./bench/wasm-vs-docker -skip-docker

# pin the Rust compiler (e.g. a rustup toolchain that has the wasm target)
RUSTC="$HOME/.rustup/toolchains/stable-aarch64-apple-darwin/bin/rustc" \
  go run ./bench/wasm-vs-docker
```

Raw output is written to `bench/wasm-vs-docker/results.txt` and echoed to stdout.

## Docker hygiene

The harness creates containers named `aisotropy-sbx-wasmbench-*` and removes every
one it creates (`Close` per container + `DockerProvider.CleanupAll` on exit). It
**builds and pulls no images** — it uses a local image you already have (default
`alpine:3.20`), so there is nothing to `docker rmi`. It never touches service
containers (`aisotropy-rqlite-1`, `aisotropy-qdrant-1`, etc.).

## Files

- `main.go` — the harness (package `main`).
- `testdata/guest/main.go` — the constant-work guest, shared by the **Go** and
  **TinyGo** builds (`//go:build wasip1`; under `testdata/` so the go tool ignores it
  for normal builds/vet — it only compiles under `GOOS=wasip1 GOARCH=wasm`).
- `testdata/guest-rust/guest.rs` — the `#![no_std]`, dependency-free **Rust** guest
  (a few KiB, imports nothing).
- `results.txt` — raw measured output (regenerated by a run).
