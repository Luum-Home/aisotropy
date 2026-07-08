---
type: ADR
title: "ADR-0005 — Go↔Rust boundary for Apache DataFusion"
description: How to cross the Go↔Rust boundary for the sleep cycle's batch analytics engine, DataFusion — separate Rust worker vs. CGO/FFI vs. WASM.
tags: [decisions, adr, datafusion, rust, ffi, sleep-cycle]
timestamp: 2026-07-08T00:00:00Z
---

# ADR-0005 — Go↔Rust boundary for Apache DataFusion

## Status

**✅ Accepted (ratified by the user, 2026-07-08).** The boundary is a **separate Rust worker**
(batch subprocess with Parquet/Arrow IPC as the exchange format).

## Context

Aisotropy's core is a native **Go** binary (Huma + Gollem + BadgerDB + rqlite) that compiles
*static, Distroless, MBs of RAM* — that single binary is a pillar of the design (see
[overview](../architecture/overview.md)). The only heavy service that scales separately is
Qdrant.

[Apache DataFusion](../components/datafusion-sleep-cycle.md) is a columnar, **Arrow-native
engine written in Rust**. It's the analytics engine for the **"sleep cycle"**: the cold
consolidation of [tiered memory](../architecture/memory-tiers.md) — dedup, contradiction
reconciliation, noise cleanup, and pattern detection over rqlite/LiteFS `.db` files and
compacted Parquet.

**The decisive fact:** the sleep cycle is **BATCH and OFFLINE**. It runs overnight or during
idle windows, triggered by the BadgerDB Outbox workers. **It's not on the request hot path.**
The user never waits on DataFusion. Therefore **in-process latency is not a requirement** —
unlike working memory (µs) or facts memory (ms). What does matter in the cold path is: don't
break Go's static binary, isolate failures of the analytics process, and keep each language
idiomatic. This ADR chooses how Go invokes a Rust engine given that profile.

## Goals / Non-goals

**Goals**

- Run DataFusion (Rust) from the consolidation pipeline orchestrated in Go.
- **Preserve the static Go binary and trivial cross-compilation** (`GOOS`/`GOARCH` without a C toolchain).
- Failure isolation: a panic/OOM in the analytics engine **cannot** bring down the process
  serving requests.
- Take advantage of DataFusion being Arrow-native: columnar data exchange without expensive
  row-based serialization.
- Build/CI simplicity: avoid cross C/Rust toolchains in the core pipeline.

**Non-goals**

- In-process latency or low per-call latency (it's offline batch; doesn't apply).
- Interactive analytics queries on the request hot path (that's rqlite/Qdrant, not DataFusion).
- Embedding DataFusion in the core binary "for elegance." Tight coupling is not a goal.
- Rewriting DataFusion or porting columnar analytics to Go.

## Options considered

### Option A — Separate Rust worker/service (process invocation)

An independent Rust binary links DataFusion. Go workers **invoke** it during sleep-cycle
windows. Two sub-variants:

- **A1 — Batch subprocess (recommended):** the Go worker *spawns* the Rust binary, which reads
  the compacted Parquet/`.db` files, runs the analytics, and **writes the results as Parquet /
  Arrow IPC** to a path Go then reinjects into Qdrant/rqlite. No long-lived service to
  maintain; fits naturally with the Outbox's periodic trigger.
- **A2 — Arrow Flight SQL service:** the Rust binary exposes an **Arrow Flight SQL** endpoint
  (gRPC + Arrow IPC); Go queries it via the **ADBC Flight SQL** driver (`apache/arrow-adbc/go`,
  *Stable* status). Useful if on-demand/interactive analytics over the corpus is wanted in the
  future.

Low coupling, each language idiomatic, full process isolation. The cost is a separate process
and (in A2) local network overhead. Since it's Arrow-native end to end, the "serialization
cost" is minimal: Parquet/Arrow IPC is already DataFusion's working format, not a row-based
conversion.

### Option B — CGO / FFI (Rust `cdylib` with C ABI)

Compile DataFusion as a native library exposed via **C ABI** and link it into the Go binary
via CGO. In-process. The [`datafusion-ffi`](https://crates.io/crates/datafusion-ffi) crate
exists, **but its design intent is Rust↔Rust interop between DataFusion versions, not
Go↔Rust** — for Go, everything has to be manually wrapped in a `cdylib` with a stable C ABI.
Documented CGO costs: requires a **C compiler**, **breaks cross-compilation** (needs
cross-toolchains), **drags in libc and kills Go's static binary**, and adds ~200ns of overhead
per call (Go 1.26 cut it ~30%, but it still exists). Per-call overhead is irrelevant for batch —
the real problem is that **it destroys the core's build/deploy invariants**.

### Option C — WASM (DataFusion → WASM, run with wazero or another Go runtime)

Compile DataFusion to WebAssembly and run it inside the Go process with a runtime like
**wazero** (pure Go, no CGO). Attractive in theory: it would keep the static binary **without**
a C toolchain. **2026 reality:** DataFusion *compiles* to `wasm32-unknown-unknown` /
`wasm32-wasi` since v40 (2024), with `usize→u64` fixes for WASM through v47 (Jul 2025), **but
it's still playground/experimental grade** (the live targets are the web playground and
splitgraph PoCs). WASM 3.0 brings threads, SIMD, and Memory64, but: in a server-side pure-Go
runtime like wazero, **thread support is practically nil and SIMD is limited**, file I/O
requires WASI, and going past 4 GB of heap depends on Memory64. For a heavy batch engine that
wants to read large Parquet files and use multiple cores, WASM today would be
**single-threaded, memory-capped, and lacking the maturity** the sleep cycle needs. Not viable
as a production engine in 2026; re-evaluable in the future.

## Trade-off table

| Criterion | A. Separate Rust worker | B. CGO / FFI | C. WASM (wazero) |
|---|---|---|---|
| **Coupling** | Low (process/service) | High (in-process, ABI) | Medium (in-process, sandbox) |
| **Build / CI** | Simple: two independent binaries | **Complex: C + cross Rust toolchains** | Medium: WASM toolchain, but no C |
| **Go binary portability** | **Intact** (static, trivial cross-compile) | **Broken** (libc, no static, no cross-compile) | Intact (wazero is pure Go) |
| **Overhead** | Process + Parquet/IPC (irrelevant in batch) | ~200ns/call (irrelevant in batch) | High: single-threaded, no real SIMD, memory ceiling |
| **Failure isolation** | **Total** (panic/OOM isolated from the core) | **None** (a Rust panic can bring down Go) | Partial (WASM sandbox, but runtime in the same process) |
| **Data format** | **Native Arrow IPC / Parquet** (columnar, no conversion) | Arrow via C Data Interface (manual) | Arrow in WASM memory (copies across the boundary) |
| **Maturity 2026** | High (DataFusion + ADBC/Flight SQL stable) | Medium (FFI exists, aimed at Rust↔Rust) | **Low (experimental/playground)** |

## Recommended decision

**Option A — separate Rust worker, sub-variant A1 (batch subprocess with Parquet /
Arrow IPC as the exchange format) is recommended.** *Ratified by the user.*

Rationale, anchored in the sleep cycle being batch/offline:

1. **There's no in-process latency requirement**, which was the only strong argument for B/C.
   Without that requirement, low coupling and build simplicity win by a landslide.
2. **Preserves the Go static-binary pillar.** CGO (B) would break it: libc, no cross-compile,
   C toolchain in CI. That directly contradicts the core design in the
   [overview](../architecture/overview.md). Not worth it for an engine that runs overnight.
3. **Total failure isolation.** Consolidation can scan large datasets and OOM; having that kill
   a subprocess instead of the process serving requests is exactly the desired behavior.
4. **Fits the Arrow model.** DataFusion already reads/writes Parquet and Arrow IPC natively;
   using Parquet as the boundary doesn't add a row-based serialization layer — it's the working
   format, and it fits reading the sleep cycle's `.db`/Parquet files.
5. **Each language idiomatic.** Go orchestrates (Outbox, scheduling, idempotency); Rust does
   the columnar analytics where it shines. No fragile ABIs or hand-written C glue.

**A2 (Arrow Flight SQL)** remains an explicit evolution path: if on-demand or interactive
analytics over the corpus is needed later, the binary is promoted to a Flight SQL service and
Go consumes it via the stable ADBC driver — same engine, same Arrow data boundary, only the
transport changes (subprocess → gRPC). The decision isn't redone.

**B (CGO/FFI) is discarded** for breaking build/portability/isolation with no gain (latency
doesn't matter here), and **C (WASM) is discarded** for immaturity and lack of
threads/SIMD/memory for heavy batch work in 2026.

## Consequences

**Positive**

- The Go core remains a static, cross-compilable, Distroless binary. Zero C toolchain in its CI.
- The analytics engine is an independent Rust artifact, versionable and deployable separately.
- Sleep-cycle failures don't affect the request hot path.
- Clean, columnar data boundary (Parquet/Arrow IPC); reinjection into Qdrant/rqlite already
  described in [consistency](../architecture/consistency.md).

**Negative / costs**

- Two toolchains in the repo (Go + Rust) and two artifacts to version and release together.
- An explicit Go↔Rust I/O contract (paths, Parquet schema, exit codes, signals) that must be
  defined and tested; a Rust panic is observed as a non-zero exit code, with retries/backoff on
  the Go side.
- Intermediate materialization to disk (Parquet) instead of sharing in-process memory —
  acceptable, even desirable, in batch (checkpointing, reproducibility, debugging).
- Packaging: the deploy includes the sleep cycle's Rust binary in addition to the core (image
  or sidecar).

## Open questions

- **Invocation contract:** CLI flags + a path convention, or a small job manifest
  (JSON/TOML) the Go worker writes and the Rust binary consumes?
- **A1 vs A2 from day one:** start with subprocess (A1) and promote to Flight SQL (A2) only if
  the interactive use case appears, or go straight to Flight SQL to avoid migrating later?
- **Data source:** does DataFusion read the rqlite/LiteFS `.db` files directly, or does the
  pipeline compact to Parquet first (more idiomatic for DataFusion and decouples from the
  SQLite format)?
- **Scheduling and multi-tenancy:** one consolidation job per tenant, or a global one
  partitioned by `tenant_id`? Tie it to the [ADR-0007](index.md) strategy.
- **Packaging/deploy:** Rust binary embedded in the same image as the core, or a separate
  sidecar?

## References

- [Apache DataFusion — documentation](https://datafusion.apache.org/)
- [DataFusion 47.0.0 release (Jul 2025) — usize→u64 changes for WASM](https://datafusion.apache.org/blog/2025/07/11/datafusion-47.0.0/)
- [`datafusion-ffi` crate — interop designed for Rust↔Rust](https://crates.io/crates/datafusion-ffi)
- [PoC: DataFusion to WebAssembly (splitgraph, experimental)](https://github.com/splitgraph/experimental-datafusion-webassembly)
- [datafusion-wasm-playground](https://github.com/datafusion-contrib/datafusion-wasm-playground)
- [Arrow Flight RPC — format](https://arrow.apache.org/docs/format/Flight.html)
- [ADBC Flight SQL Driver (Go, Stable status)](https://arrow.apache.org/adbc/current/driver/flight_sql.html)
- [apache/arrow-adbc — Go ADBC](https://github.com/apache/arrow-adbc)
- [Calling Rust FFI libraries from Go (Arcjet) — CGO costs](https://blog.arcjet.com/calling-rust-ffi-libraries-from-go/)
- [WebAssembly SIMD & Threads Flags — Cheat Sheet 2026](https://techbytes.app/posts/webassembly-simd-threads-flags-cheat-sheet-2026/)

---

**Cross-links:** [tiered memory](../architecture/memory-tiers.md) ·
[DataFusion component](../components/datafusion-sleep-cycle.md) ·
[overview](../architecture/overview.md)
