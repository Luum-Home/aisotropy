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
and step 7 of the [request lifecycle](../architecture/overview.md#lifecycle-of-a-request):

- **Deduplicate** repeated records (the user said the same thing 3 times → 1 clean record).
- **Update contradictions** — reconcile stale facts with new ones (complements, at the
  batch level, the *supersede* mechanism that already runs on the hot path in
  [facts memory](rqlite-facts-memory.md)).
- **Scrub noise** — discard trivial chatter with no cognitive value ("hi," "thanks").
- **Detect patterns/friction** — input for
  [primitive self-improvement](../architecture/memory-tiers.md#primitive-self-improvement-behavioral-memory):
  measures reflection loop retries and explicit user corrections so an optimizer agent can
  adjust tool metadata.
- Reinject refined conclusions into [Qdrant](qdrant-semantic-memory.md) (updated
  vectors) and [rqlite](rqlite-facts-memory.md) (consolidated facts).

## Integration surface: FFI boundary with Go

Unlike Qdrant (a network service via gRPC), DataFusion is a **library embedded in Rust**,
meant to run in-process within whoever invokes it — it doesn't expose its own service. This
makes it ideal for short batch compute spikes (spins up, processes, shuts down), but it raises a
real architectural question: **how does a worker written in Go cross into it?**

There are no official Go bindings for DataFusion (there are Python and Java bindings via FFI). The
options under evaluation, still unresolved:

| Option | Description | Tension |
|---|---|---|
| **Direct CGO / FFI** | The Go worker links against a C library compiled from the Rust crate (DataFusion exposes a C-compatible surface for cases like this) and crosses the boundary within the same process. | Lower startup latency, but CGO complicates cross-compilation, reproducible builds, and the "distroless" binary that is currently a stack goal. |
| **Separate Rust worker** | A dedicated Rust binary/process that the Go orchestrator launches or invokes (local RPC, exec, or a work queue) only during the sleep cycle. | Keeps language separation clean and doesn't contaminate the Go binary, but adds a second binary to operate/deploy — a direct tension with the [golden rule of the stack](../architecture/overview.md#the-stack-closed-100-mitapache-20) of minimizing separate pieces. |
| **WASM** | Compile DataFusion (or the needed portion) to WASM and invoke it from Go with an embedded WASM runtime. | Sandboxing and portability are attractive, but performance and toolchain maturity for heavy analytical workloads are uncertain *(to verify)*. |

This tension is exactly the subject of **[ADR-0005](../decisions/index.md)**, currently **open**:
"Rust↔Go boundary for DataFusion (FFI vs. worker vs. WASM)." This document doesn't resolve it —
it only sizes the problem. Any implementation decision must wait for the ADR to close.

## Configuration/key patterns

| Pattern | Detail |
|---|---|
| **Cold activation, not 24/7** | Workers spin up DataFusion during idle windows (night, or low tenant load) — there's no resident DataFusion process waiting for hot queries. |
| **Read sources** | Reads [rqlite](rqlite-facts-memory.md)/LiteFS's `.db` files directly, or compacted Parquet if a prior columnar export step exists *(exact SQLite→Parquet export pipeline to verify, if applicable)*. |
| **Embedded columnar SQL** | Used as a SQL query engine over in-memory Arrow — not as a database service; the lifecycle is "instantiate → execute plan → discard." |
| **Output** | Conclusions (deduplication, resolved contradictions, friction patterns) are written back to rqlite and Qdrant, never served directly to a user request. |

## Gotchas and risks

- **The stack's central architectural friction point.** It's the only Rust component that needs
  to cross a process/language boundary with Go non-trivially (Qdrant solves this with
  standard gRPC; DataFusion doesn't have that luxury, being an embedded library rather than a service).
- **Operational negligence risk.** If ADR-0005 doesn't close and the team picks the "easiest to
  get started" option without evaluating the maintenance trade-off, it could end up with fragile
  CGO in production or a poorly integrated second Rust binary in the Go binary's deployment pipeline.
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

- [ADR-0005](../decisions/index.md) — Rust↔Go boundary for DataFusion (FFI vs. worker vs. WASM):
  **open**, blocks the actual implementation of the sleep cycle.
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
