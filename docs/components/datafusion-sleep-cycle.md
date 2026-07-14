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

> **Status note (2026-07-14):** of the responsibilities below, only **deduplication is
> implemented**. The worker at `crates/sleep-cycle/src/main.rs` runs a single
> `ROW_NUMBER()` dedup partitioned by `(tenant_id, user_id, lower(content))` (keeping the
> highest-importance, most-recently-updated row), and the Go coordinator
> (`internal/sleepcycle/coordinator.go`) deletes the dropped ids from the live facts store
> (through the engram facade, so the Fact-kind vector goes too) and prunes Turn-kind vectors.
> Everything else in this list is **aspirational**.

- **Deduplicate** repeated records (the user said the same thing 3 times → 1 clean record).
  **Implemented** — the exact scope of the current worker.
- **Update contradictions** — reconcile stale facts with new ones. **Aspirational.** The seam
  exists: the facts store implements `Supersede` (`internal/facts/sqlite.go` /
  `internal/facts/rqlite.go`, exposed via the engram facade), which archives the old fact and
  saves its replacement atomically — but **nothing calls it yet**, neither on the hot path nor
  from this batch worker.
- **Scrub noise** — discard trivial chatter with no cognitive value ("hi," "thanks").
  **Aspirational** — not part of the worker's SQL today.
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
*(status note 2026-07-14)*:

- `crates/sleep-cycle` is the Rust binary: `--input <facts.ndjson> --output <deduped.parquet>`,
  a one-line JSON summary (`rows_in`, `rows_out`, `dropped`, `dropped_ids`) on stdout, and a
  non-zero exit code (stderr carries the reason) on any failure, including a panic — a Rust
  crash never becomes an in-process crash of the Go core.
- `internal/sleepcycle` is the Go side: the `Worker` spawns the subprocess and parses the
  summary; the `Coordinator` exports the live facts to NDJSON, runs the worker, then deletes the
  `dropped_ids` from the live store and prunes Turn-kind vectors.
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
| **Output** | As built *(2026-07-14)*: the worker writes the deduplicated set as **Parquet** and emits the dropped ids in its stdout summary; the Go coordinator applies the cleanup (deleting those facts and their Fact-kind vectors, pruning Turn-kind vectors). Writing back resolved contradictions or friction patterns is aspirational. Nothing here is ever served directly to a user request. |

## Gotchas and risks

- **The stack's central architectural friction point.** It's the only Rust component that needs
  to cross a process/language boundary with Go non-trivially (Qdrant solves this with
  standard gRPC; DataFusion doesn't have that luxury, being an embedded library rather than a
  service). ADR-0005 resolved this with the batch-subprocess boundary, but the Go↔Rust I/O
  contract (paths, NDJSON/Parquet schema, exit codes) must stay explicit and tested as the
  worker's scope grows.
- **Scope gap vs. the pitch.** The doc-level pitch is "dedup + contradictions + noise +
  patterns"; the shipped worker does dedup only *(2026-07-14)*. Until the remaining
  responsibilities land, the sleep cycle removes exact-duplicate facts but does not reconcile
  contradictory ones (the `Supersede` seam is unwired) or filter noise.
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
