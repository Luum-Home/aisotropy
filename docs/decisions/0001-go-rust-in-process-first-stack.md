---
type: ADR
title: "ADR-0001 — Go+Rust stack, in-process-first"
description: Retroactive record of the stack's golden rule — in-process Go where latency matters, dedicated Rust services where scale matters — with Qdrant as the only component meant to scale independently.
tags: [decisions, adr, stack, go, rust, latency, retroactive]
timestamp: 2026-07-08T00:00:00Z
---

# ADR-0001 — Go+Rust stack, in-process-first

## Status

**✅ Accepted — retroactive formalization (2026-07-08).** The stack's golden rule is:
**in-process (Go) where latency matters; dedicated services (Rust) where scale matters.**
Listed in [docs/decisions/index.md](index.md) as "accepted (implicit in the source
conversations)" since that index was written, with no corresponding ADR file. This document is
that missing file.

## Context

Aisotropy's [architecture overview](../architecture/overview.md) fixes a specific stack:

| Layer | Technology | Language / License | Role |
|---|---|---|---|
| API & Contracts | Huma | Go / MIT | HTTP validation, `ctx` extraction, OpenAPI 3.1, SSE |
| AI Orchestration | Gollem | Go / MIT | In-process cognitive engine |
| Working Memory | BadgerDB | Go / Apache-2.0 | Live chat + summary buffer, in-process (µs) |
| Semantic Memory | Qdrant | Rust / Apache-2.0 | Vectors, tenant-filtered HNSW scan |
| Facts Memory | rqlite (or LiteFS) | Go MIT / Apache-2.0 | Networked SQLite+FTS5 facts journal |
| Batch analytics | Apache DataFusion | Rust / Apache-2.0 | Offline sleep-cycle consolidation |

Every component except Qdrant and the DataFusion sleep-cycle worker runs **in-process, in Go**,
compiled into a single Distroless binary. This is a foundational split that every later ADR
(0004–0010) inherits without re-litigating it: [ADR-0004](0004-facts-memory-rqlite-vs-litefs.md)
picks rqlite over LiteFS partly *because* it's a Go binary that "compiles alongside the rest of
the stack"; [ADR-0005](0005-datafusion-go-rust-boundary.md) treats the DataFusion boundary as a
subprocess specifically because the sleep cycle is the one workload this ADR's rule assigns to
a separate Rust service, not because CGO/FFI/WASM were live options for the hot path.

## Goals / Non-goals

**Goals**

- Fix the split between what runs in-process (Go) and what runs as a separate, independently
  scaled service (Rust), as a standing rule later ADRs can inherit instead of re-deciding.
- Keep the request hot path (chat turn, memory hydration, LLM call, SSE stream) entirely
  in-process with no network hop to a sibling service, except where a component's own nature
  (vector search at scale, batch analytics) requires one.

**Non-goals**

- **Not** picking the specific engine for any one layer — those are separate ADRs
  ([0004](0004-facts-memory-rqlite-vs-litefs.md) for facts memory,
  [0005](0005-datafusion-go-rust-boundary.md) for the DataFusion boundary,
  [0006](0006-reranker-cohere-vs-bge-local.md) for the reranker). This ADR only fixes the
  in-process-vs-service axis those decisions are made against.
- **Not** deciding Qdrant's internal architecture — it is treated as a given, dedicated,
  independently-scaled Rust service per the table above.

## Options considered

### Option A — Everything in-process (single Go binary, no separate services)

Would maximize latency (no network hop anywhere) and minimize operational surface (one binary
to deploy). Rejected because vector search at scale (Qdrant, HNSW over large tenant corpora) and
batch analytics (DataFusion over Parquet/Arrow at rest) are workloads that benefit from
independent scaling and from mature, dedicated engines that are not written in Go — forcing them
in-process would mean either reimplementing HNSW/DataFusion-equivalent engines from scratch in
Go, or accepting a worse-fit substitute.

### Option B — Everything as separate services (microservices-first)

Would maximize independent scalability and language freedom per component, at the cost of a
network hop on every memory-hydration read on the hot path (see the
[request lifecycle](../architecture/overview.md#lifecycle-of-a-request), step 3: memory
hydration runs in parallel goroutines and is explicitly latency-sensitive). Rejected for the
components that sit on the hot path — working memory (BadgerDB) and the facts layer (rqlite,
network but local-first-read) — because a synchronous per-request hop to a sibling service for
data that's read on every turn is exactly the latency Aisotropy is designed to avoid.

### Option C — In-process where latency matters, dedicated services where scale matters — accepted

Split by workload characteristic, not by uniform policy: Huma, Gollem, BadgerDB, and rqlite
stay in-process (Go, compiled into one binary, µs-to-ms local access); Qdrant (vector search at
scale) and the DataFusion sleep-cycle worker (offline batch analytics, invoked as a subprocess
per [ADR-0005](0005-datafusion-go-rust-boundary.md)) run as separate, independently-scaled
components. Qdrant is explicitly called out in the overview as *"the only heavy service meant to
scale independently."*

## Trade-off comparison table

| Criterion | A — All in-process | B — All separate services | C — Split by latency/scale — accepted |
|---|---|---|---|
| Hot-path latency | Best (zero network hops) | Worst (every read is a hop) | Best for the hot path; batch/vector work pays its own cost where it belongs |
| Independent scaling | None — one binary scales as a unit | Best — every component scales alone | Qdrant and the sleep-cycle worker scale independently; the Go core scales as one unit |
| Engine fit | Forces reimplementing mature Rust engines (HNSW, DataFusion) in Go | Best — use the right engine per component | Best — Qdrant/DataFusion are used as designed, not reimplemented |
| Operational surface | Smallest (one binary) | Largest (N services) | Small: one Go binary + Qdrant + an on-demand Rust subprocess |
| Deployment footprint | Minimal RAM/MB (Distroless) | Higher (N runtimes) | Minimal for the core; Qdrant is the one component that scales/costs independently |

## Recommended decision

**Option C is recommended and already implemented across the stack.** The rule — in-process Go
where latency matters, dedicated Rust services where scale matters — is what makes
[ADR-0004](0004-facts-memory-rqlite-vs-litefs.md)'s "compiles alongside the rest of the stack"
argument coherent, and what makes [ADR-0005](0005-datafusion-go-rust-boundary.md) a subprocess
decision rather than an in-process-vs-out-of-process one: the sleep cycle was already assigned
to "separate Rust service" by this ADR before ADR-0005 had to decide *how* that service talks to
Go.

## Consequences

**Positive**

- **Minimal infra cost:** Huma+Gollem+BadgerDB+rqlite compile into one native Distroless binary
  (few MB, per [overview](../architecture/overview.md)). Only Qdrant scales separately.
- **Hot-path latency stays local:** memory hydration (BadgerDB, rqlite local reads) never leaves
  the process on the request path that the user is waiting on.
- **Right engine per workload:** vector search and batch analytics use engines built for that
  job (Qdrant, DataFusion) instead of Go reimplementations.
- **Downstream ADRs inherit a settled axis:** ADR-0004, ADR-0005, and ADR-0006 each cite
  in-process-vs-service reasoning that traces back to this rule rather than re-deriving it.

**Negative / technical debt**

- **Two-language operational surface:** the team must operate, observe, and deploy both a Go
  binary and Rust services (Qdrant, the DataFusion worker), rather than a single-language stack.
- **The rule is a heuristic ("latency" vs. "scale"), not a formula:** future components will
  need judgment calls about which side of the line they fall on — this ADR doesn't provide a
  quantitative threshold.

## Open questions

1. **Formal threshold.** "Where latency matters" vs. "where scale matters" is qualitative here.
   No numeric SLA (e.g., "any read >X ms on the hot path must be in-process") is recorded in the
   source material — components have been placed by inspection so far.
2. **Future components.** As new memory tiers or workloads are added, which side of this split
   they land on isn't pre-decided by a rule beyond the two examples (Qdrant, DataFusion) already
   settled.

## References

- [docs/decisions/index.md](index.md) — original "accepted (implicit in the source
  conversations)" ledger entry for ADR-0001.
- [docs/architecture/overview.md](../architecture/overview.md) — the stack table and the
  "golden rule" statement this ADR formalizes.
- [ADR-0004 — rqlite vs LiteFS](0004-facts-memory-rqlite-vs-litefs.md) and
  [ADR-0005 — DataFusion Go/Rust boundary](0005-datafusion-go-rust-boundary.md) — decisions made
  downstream of this rule.

---

Cross-links: [decisions index](index.md) · [architecture overview](../architecture/overview.md)
