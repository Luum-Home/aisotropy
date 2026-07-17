---
type: Component
title: Apache DataFusion — Sleep Cycle Engine
description: Rust-embedded columnar SQL query engine that runs offline batch consolidation (the "sleep cycle") over the system's memory, replacing GlareDB.
tags: [components, memory, rust, batch, sleep-cycle, datafusion]
timestamp: 2026-07-08T00:00:00Z
---

# Apache DataFusion — Sleep Cycle Engine

## Role in the system

DataFusion is the analytics engine for the coldest tier of the
[memory hierarchy](../architecture/memory-tiers.md#4-batch-consolidation--the-sleep-cycle): the
**consolidation** — the "defragmentation" that runs offline, not during the request. In the
[LLM OS model](../architecture/overview.md#llm-os) it doesn't have its own "temperature" tier like
BadgerDB/rqlite/Qdrant; it's the **process** that reorganizes the other three when the system is
idle (nighttime / low load), analogous to sleep in biological memory consolidation.

It replaces **GlareDB**, abandoned at the end of 2025 — GlareDB used DataFusion as its core engine, so
Aisotropy goes straight to the source instead of depending on an unmaintained intermediate
layer (see [decisions already made](../decisions/index.md#decisions-already-made-in-the-source-conversations)).

## Responsibilities

Per the flow described in [tiered memory](../architecture/memory-tiers.md#4-batch-consolidation--the-sleep-cycle)
and step 7 of the [request lifecycle](../architecture/overview.md#lifecycle-of-a-request).

> **Status note (2026-07-17):** two of the responsibilities below are **implemented and
> run-verified against the real Rust worker**: **deduplication** and a **low-signal noise
> prune**. The worker at `crates/sleep-cycle/src/main.rs` runs, in order, (1) a noise pass that
> drops rows below an importance floor (`--min-importance`, off by default) and (2) a
> `ROW_NUMBER()` dedup partitioned by `(tenant_id, user_id, lower(content))` keeping the
> highest-importance, most-recently-updated row. The Go coordinator
> (`internal/sleepcycle/coordinator.go`) deletes **every** dropped id — noise-pruned or
> de-duplicated — from the live facts store (through the engram facade, so the Fact-kind vector
> goes too) and prunes Turn-kind vectors. **Contradiction reconciliation and pattern detection
> remain aspirational** (the named follow-on). Evidence: `internal/sleepcycle/worker_manual_test.go`
> (`go test -tags manual -run TestWorker ./internal/sleepcycle`) drives synthetic exports through
> the real subprocess and asserts dedup drops the lower-importance duplicate, the noise pass drops
> the sub-floor row (surfaced in `noise_dropped` + `dropped_ids`), and the default (no floor) is
> byte-for-byte the historical dedup-only behavior.

- **Deduplicate** repeated records (the user said the same thing 3 times → 1 clean record).
  **Implemented and run-verified.**
- **Scrub noise** — discard low-signal chatter with no cognitive value ("hi," "thanks").
  **Implemented (deterministic, importance-threshold subset) and run-verified.** The worker's
  first pass drops every fact whose `importance` is below the `--min-importance` floor. It is
  **off by default** (`Worker.MinImportance <= 0` omits the flag, so the argv and behavior are
  identical to the pre-noise dedup-only contract); the wiring (`aisotropy/app.go`) owns whether
  to set a floor. Note the scrub is by the store's numeric importance score, **not** semantic
  content classification — a fact scored low is treated as low-signal. Content-aware noise
  classification (beyond the numeric floor) remains a possible evolution.
- **Update contradictions** — reconcile stale facts with new ones. **Aspirational — the named
  follow-on.** The seam exists: the facts store implements `Supersede` (`internal/facts/sqlite.go` /
  `internal/facts/rqlite.go`, exposed via the engram facade), which archives the old fact and
  saves its replacement atomically — but **nothing calls it yet**, neither on the hot path nor
  from this batch worker. It is deliberately *not* faked here: reconciling contradictions over the
  current free-text `content` (no subject/predicate structure) would require semantic
  understanding, so a precise contradiction *flag* cannot be produced without either structured
  facts or an LLM pass. Implementing it against the `Supersede` seam is the next unit of work.
- **Detect patterns/friction** — input for
  [primitive self-improvement](../architecture/memory-tiers.md#primitive-self-improvement-behavioral-memory):
  measures reflection loop retries and explicit user corrections so an optimizer agent can
  adjust tool metadata. **Aspirational.**
- Reinject refined conclusions into [Qdrant](qdrant-semantic-memory.md) (updated
  vectors) and [rqlite](rqlite-facts-memory.md) (consolidated facts). **Partially implemented:**
  today the "reinjection" is cleanup — deleting the dropped duplicates (fact rows + their
  Fact-kind vectors) and pruning Turn-kind vectors; no refined conclusions are written back yet.

## Integration surface: the Go↔Rust boundary

Unlike Qdrant (a network service via gRPC), DataFusion is a **library embedded in Rust**,
meant to run in-process within whoever invokes it — it doesn't expose its own service. This
makes it ideal for short batch compute spikes (spins up, processes, shuts down), but it raised a
real architectural question: **how does a worker written in Go cross into it?**

That question is **resolved and implemented**
([ADR-0005](../decisions/0005-datafusion-go-rust-boundary.md), accepted 2026-07-08): the boundary
is a **separate Rust worker invoked as a batch subprocess** — not CGO/FFI, not WASM. As built
*(status note 2026-07-17)*:

- `crates/sleep-cycle` is the Rust binary:
  `--input <facts.ndjson> --output <survivors.parquet> [--min-importance <f>]`, a one-line JSON
  summary (`rows_in`, `rows_out`, `dropped`, `noise_dropped`, `dropped_ids`) on stdout, and a
  non-zero exit code (stderr carries the reason) on any failure, including a panic — a Rust
  crash never becomes an in-process crash of the Go core. `--min-importance` is optional; absent
  or `<= 0` disables the noise pass, so the default summary and behavior are byte-for-byte the
  historical dedup-only contract (`noise_dropped` is then `0` and, on an older worker that never
  emits the field, decodes to `0` on the Go side).
- `internal/sleepcycle` is the Go side: the `Worker` spawns the subprocess (forwarding
  `--min-importance` only when `Worker.MinImportance > 0`) and parses the summary; the
  `Coordinator` exports the live facts to NDJSON, runs the worker, then deletes **every**
  `dropped_id` (noise-pruned or de-duplicated) from the live store and prunes Turn-kind vectors.
- The path is **dormant by default**: it runs only when the Rust binary is built and
  `AISOTROPY_SLEEPCYCLE_BIN` points at it (`aisotropy/app.go`).

The rejected alternatives (CGO/FFI, WASM) and the full trade-off table live in
[ADR-0005](../decisions/0005-datafusion-go-rust-boundary.md).

## Configuration/key patterns

| Pattern | Detail |
|---|---|
| **Cold activation, not 24/7** | Workers spin up DataFusion during idle windows (night, or low tenant load) — there's no resident DataFusion process waiting for hot queries. |
| **Read sources** | As built *(2026-07-14)*: the Go coordinator exports the current facts to a scratch **NDJSON** file (`internal/sleepcycle/record.go`), which the worker registers as a DataFusion table — it does not read `.db` files directly. A direct-`.db` or compacted-Parquet input remains a possible evolution. |
| **Embedded columnar SQL** | Used as a SQL query engine over in-memory Arrow — not as a database service; the lifecycle is "instantiate → execute plan → discard." |
| **Output** | As built *(2026-07-17)*: the worker writes the surviving set (post noise-prune + dedup) as **Parquet** and emits every dropped id in its stdout summary (`noise_dropped` breaks out how many were noise vs. dedup); the Go coordinator applies the cleanup (deleting those facts and their Fact-kind vectors, pruning Turn-kind vectors). Writing back resolved contradictions or friction patterns is aspirational. Nothing here is ever served directly to a user request. |

## Enabling the sleep cycle: path, cost, and recommendation

The DataFusion dedup path is **dormant by default** and gated on two things being true:

1. **The Rust worker is built.** `cargo build --release --manifest-path crates/sleep-cycle/Cargo.toml`
   produces `crates/sleep-cycle/target/release/sleep-cycle`. This is a **~67 MB binary** and the
   first build compiles the full DataFusion 43 dependency tree (~2 min cold on an M-series Mac;
   incremental rebuilds of just this crate are ~10 s). This is the cost ADR-0005 accepted: a
   second toolchain (Rust) and a second artifact to build, version, and ship alongside the Go
   core.
2. **`AISOTROPY_SLEEPCYCLE_BIN` points at that binary** (read by `aisotropy/app.go`). With it
   unset, the core boots and runs exactly as today — the coordinator is simply never given a
   `Runner`, so no subprocess is ever spawned. **Turn-kind vector pruning is independent of this
   gate** (`Coordinator.PruneTurnsOnce` / `RunTurnPruneScheduler`) and stays active without the
   Rust binary, because unbounded Turn growth is a pure-Go semantic-tier concern.

The **noise prune is a second, independent opt-in on top of enabling the worker**: it only runs
when the wiring sets `Worker.MinImportance > 0`. Left at its zero value, the enabled worker is
dedup-only and byte-for-byte compatible with the pre-noise contract.

**Should it be default-on?** Recommendation (the app.go wiring is owned elsewhere):

- **Dedup worker: keep dormant-by-default; make it trivial to turn on.** Shipping the Go core as a
  single static binary with **no mandatory Rust artifact** is a core design pillar (ADR-0005). A
  deployment that has not built the Rust binary must keep booting unchanged — so the *default* has
  to stay off. But the value is real and run-verified, so the enable path should be one env var
  (`AISOTROPY_SLEEPCYCLE_BIN`) plus a documented build step, and the reference `docker-compose`
  overlay (`docker-compose.sleepcycle.yml`) should build+wire it so "on" is one compose flag.
  Net: **off by default, first-class opt-in**, not buried.
- **Noise prune: default the floor to 0 (disabled) even when the worker is on.** Dropping facts by
  a numeric importance threshold is a **destructive, tenant-visible policy** — the right floor is
  deployment-specific and too low a value silently deletes real facts. It should be an explicit,
  operator-chosen value, never an implicit default. Dedup (collapsing exact duplicates) is safe to
  run whenever the worker is enabled; noise pruning should require a deliberate floor.

## Gotchas and risks

- **The stack's central architectural friction point.** It's the only Rust component that needs
  to cross a process/language boundary with Go non-trivially (Qdrant solves this with
  standard gRPC; DataFusion doesn't have that luxury, being an embedded library rather than a
  service). ADR-0005 resolved this with the batch-subprocess boundary, but the Go↔Rust I/O
  contract (paths, NDJSON/Parquet schema, exit codes) must stay explicit and tested as the
  worker's scope grows.
- **Scope gap vs. the pitch.** The doc-level pitch is "dedup + contradictions + noise +
  patterns"; the shipped worker does **dedup + a low-signal noise prune** *(2026-07-17)*.
  Until the remaining responsibilities land, the sleep cycle removes exact-duplicate facts and
  sub-threshold low-importance facts, but does not reconcile **contradictory** ones (the
  `Supersede` seam is unwired) or detect friction **patterns**. The noise prune is by numeric
  importance, not content classification — a fact scored low is treated as noise.
- **Staleness window.** While the sleep cycle hasn't run, Qdrant/rqlite serve data that is
  potentially duplicated or noisy — this is honest eventual consistency by design (see
  [principles](../vision/principles.md#derived-operating-principles) and
  [consistency](../architecture/consistency.md)), but it needs to be communicated clearly: it's not
  a bug, it's the model.
- **Dependency on abandoned GlareDB as precedent.** Adopting the core engine directly instead
  of a third-party wrapper reduces the risk of again being tied to an unmaintained project,
  but it means maintaining the integration (whichever boundary is chosen) without the
  convenience layer GlareDB offered.

## Related decisions

- [ADR-0005](../decisions/0005-datafusion-go-rust-boundary.md) — Rust↔Go boundary for DataFusion
  (FFI vs. worker vs. WASM): **accepted (2026-07-08) and implemented** as the batch-subprocess
  worker (`crates/sleep-cycle` + `internal/sleepcycle`).
- Decision already made (retroactively) — discard GlareDB due to abandonment at the end of 2025 and adopt
  Apache DataFusion, its core engine, directly (see
  [decisions already made](../decisions/index.md#decisions-already-made-in-the-source-conversations)).
- [ADR-0001](../decisions/index.md) — Go+Rust in-process-first stack: DataFusion is the second
  Rust piece in the stack, alongside Qdrant, but with a different integration pattern (embedded
  vs. network service).

## References

- [Tiered memory](../architecture/memory-tiers.md)
- [Architecture overview](../architecture/overview.md)
- [Consistency](../architecture/consistency.md)
- [Qdrant — Semantic Memory](qdrant-semantic-memory.md)
- Repository: [github.com/apache/datafusion](https://github.com/apache/datafusion) (Rust, Apache-2.0)
