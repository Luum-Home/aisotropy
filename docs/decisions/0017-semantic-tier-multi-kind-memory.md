---
type: ADR
title: "ADR-0017 — Semantic tier as a multi-kind associative memory"
description: The semantic (vector) tier stops being an undifferentiated blob fed raw chat turns. Every Point carries a Kind (Document | Fact | Turn) with a defined lifecycle, bound, and retrieval weight; a MemoryStore facade owns the cross-tier lifecycle of Fact-kind vectors (the corrected G1 aggregate), Turn-kind vectors are bounded, and Document-kind is the tier's intended curated-knowledge input.
tags: [decisions, adr, semantic, memory-tiers, aggregate, ddd, rag, consolidation]
timestamp: 2026-07-10T00:00:00Z
---

# ADR-0017 — Semantic tier as a multi-kind associative memory

## Status

Accepted — 2026-07-10. Supersedes the implicit "consolidation embeds every raw turn" behavior
and corrects the aggregate framing sketched in the DDD-G1 planning note.

## Context

The semantic (vector) tier ([`internal/semantic`](../../internal/semantic), Qdrant / in-memory
cosine) is described in [memory-tiers.md](../architecture/memory-tiers.md) as RAG over "the tenant's
docs/massive knowledge" — cold, unstructured, similarity-retrieved knowledge, explicitly *not* the
place for discrete distilled facts ("not everything is a vector — stable facts go to tier 2").

As built, `consolidation.Sink.consolidateSemantic` embeds **every raw turn** (`OutboxEvent.Content`,
"typically the user prompt") into semantic, keyed by the outbox event id. Two problems follow:

1. **Purpose violation.** The tier fills with raw conversational prompts, not the curated
   documents/knowledge it is meant to hold. Retrieval quality degrades (RAG returns raw prompts,
   noise), and the tier's stated intent and its actual input diverge.
2. **Unbounded low-value growth.** One vector per turn, forever, with no lifecycle, TTL, or bound —
   the semantic store grows without limit with individually low-value vectors.

A prior planning note framed the fix as a cross-tier *Aggregate* owning "a fact and its vector as
one unit." Investigation showed that premise is **false for the tier as a whole**: a distilled fact
(FTS5, tier 2) and a raw-turn embedding (vectors, tier 3) are *different* memories with independent
lifecycles, so an aggregate owning both is a forced DDD fit — and superseding a fact should not, in
general, delete an unrelated raw-turn vector.

The real question is not "add an aggregate" but **"what kind of knowledge does the semantic tier
hold, and with what lifecycle?"** — and the honest answer is that it must serve *more than one*
kind, each with different semantics. The design flaw is the absence of that distinction: the tier
has no notion of what a given vector *is*.

## Goals / Non-goals

**Goals**
- Make the *kind* of each vector first-class, so each kind gets its own lifecycle, bound, and
  retrieval weight.
- Stop the unbounded raw-turn pollution without losing the option of conversational recall.
- Give the tier its intended curated-knowledge input (documents).
- Establish a *correct, scoped* cross-tier consistency owner — for the one kind where a vector truly
  is a projection of a fact.

**Non-goals**
- Building the document-ingestion HTTP surface in this ADR (the model enables it; the endpoint is a
  follow-up feature).
- Changing the facts tier's FTS5-only retrieval for keyword-shaped queries.
- A general event bus / multiple subscribers (the outbox stays single-consumer).

## Decision

The semantic tier becomes a **multi-kind associative memory**. Every `semantic.Point` carries a
`Kind`, and each kind has a defined policy:

| Kind | Source | Lifecycle | Bound | Retrieval weight |
|---|---|---|---|---|
| **Document** | Curated tenant knowledge ingested through a dedicated path | Persists until explicitly removed | Unbounded (curated) | High |
| **Fact** | The distilled fact's own content, dual-indexed, keyed by the **fact id** | Tied to the fact: superseding/deleting the fact deletes its vector | One per current fact | High |
| **Turn** | The raw turn (today's consolidation input) | Ephemeral | TTL / per-(tenant,user) cap, evicted by the sleep cycle | Low (fallback only) |

Concretely:

1. **`Point.Kind`** (enum `Document|Fact|Turn`) is added; `PointID` encodes the kind into its
   UUIDv5 name (`tenant \x00 user \x00 kind \x00 sourceID`) so a fact's vector and a turn's vector
   never collide and can be addressed/deleted independently.
2. **`semantic.Store` gains `Delete(ctx, sc, ids)`** (all implementations) and `Search` grows an
   optional kind filter/weighting so high-value kinds outrank raw turns.
3. **A `MemoryStore` facade** (the corrected, *scoped* G1 aggregate root) owns the cross-tier
   lifecycle of **Fact-kind** vectors only: `Save(fact)` writes the fact (facts tier) **and** its
   `Fact`-kind vector; `Supersede`/`Delete(fact)` removes both. This makes "a fact and its Fact-kind
   vector live and die together" an invariant enforced by construction — the legitimate part of G1.
4. **Turn-kind** vectors keep being written by consolidation but are **bounded** (TTL/cap + sleep-cycle
   eviction) and retrieval-deprioritized, so they inform recall without polluting or growing without
   limit.
5. **Document-kind** is the tier's intended curated input; the ingestion path is a follow-up feature
   the model now supports.

## Alternatives considered

- **A — Single purpose: documents only.** Semantic = ingested documents; drop turn embedding
  entirely. Cleanest vs the doc, but throws away conversational recall and leaves the tier dormant
  until a doc path exists. Rejected: too narrow; the tier can serve more than one kind.
- **B — Single purpose: dual-indexed facts only.** Every vector is a fact projection. Resurrects the
  aggregate but contradicts "not everything is a vector," blurs the facts/semantic separation, and
  still has no home for documents or conversational recall. Rejected as the *whole* tier's model,
  but adopted as the **Fact** kind within this ADR.
- **C — Single purpose: bounded conversational recall.** Keep embedding turns, add a TTL. The
  minimum fix, but keeps the exact direction flagged as the problem (a tier of low-value turn
  vectors) and abandons the doc/knowledge intent. Rejected as the whole model, but adopted as the
  **Turn** kind within this ADR.
- **D — Status quo (undifferentiated raw-turn blob).** Rejected: the purpose violation and unbounded
  growth this ADR exists to fix.
- **E — Cross-tier aggregate owning facts *and* raw-turn vectors as one unit.** The false-premise
  plan. Rejected: a distilled fact and a raw-turn vector are different memories.

The decision is the **union** of B and C *scoped by Kind*, plus A's document input as the intended
high-value source — which is what "the tier serves all three" means done rigorously.

## Consequences

**Positive**
- Root cause fixed: raw turns no longer pollute high-value retrieval and no longer grow without
  bound (Turn kind is capped + deprioritized).
- The G1 aggregate becomes *correct*: the `MemoryStore` facade is the structural owner of Fact-kind
  vector consistency (supersede/delete a fact → its vector goes), instead of by-convention drift.
- The semantic tier gains its intended purpose (Document kind) without discarding conversational
  recall (Turn kind).
- Retrieval quality improves via kind weighting/filtering.

**Negative / cost**
- Real surface: `Point.Kind` + id scheme change, `semantic.Store.Delete` on 3 implementations,
  `Search` kind-awareness, the `MemoryStore` facade, consolidation rewire, sleep-cycle eviction of
  Turn vectors, and doc updates ([memory-tiers.md](../architecture/memory-tiers.md),
  [qdrant component](../components/qdrant-semantic-memory.md)). Phased (see below).
- A migration concern for any existing event-id-keyed vectors (dev/in-memory only today; no
  production Qdrant data — low risk).

**Mandatory going forward**
- Every `semantic.Point` MUST carry a `Kind`; no untyped vectors.
- Fact-kind vector mutations MUST go through the `MemoryStore` facade, never `semantic.Store`
  directly, so the fact↔vector invariant holds.

**Phased implementation**
1. `Point.Kind` + kind-encoded `PointID` + `semantic.Store.Delete` + kind-aware `Search`.
2. `MemoryStore` facade owning Fact-kind lifecycle (dual-index + supersede/delete); rewire the
   fact-producing path through it.
3. Turn-kind bounding (TTL/cap) + sleep-cycle eviction; consolidation writes Turn vectors through the
   bounded path.
4. (Follow-up feature) Document-kind ingestion path.

## Open questions

1. **Turn bound policy**: TTL (age-based), per-(tenant,user) cap (ring buffer), or importance-decay?
   Start with a simple cap + age TTL, tune with the bpt metric.
2. **Fact dual-index: always or opt-in?** Default to always (every current fact gets a Fact vector)
   vs. only facts above an importance threshold (cheaper embeds). Leaning importance-gated to bound
   embedding cost.
3. **Document ingestion API shape** (out of scope here): a `POST …/knowledge` endpoint, chunking
   strategy, and per-document lifecycle — a separate ADR when the feature is built.

## References

- [memory-tiers.md](../architecture/memory-tiers.md) — the tier the "distilled vs unstructured"
  split this ADR enforces comes from.
- [ADR-0011](0011-tenant-scoped-statement-builder.md) — the tenancy Scope discipline the facade
  reuses (`semantic.NewScope`, now a VO by construction after the ADR-0011 residual was closed).
- [ADR-0005](0005-datafusion-go-rust-boundary.md) — the sleep cycle that will evict Turn-kind
  vectors.
- [consistency.md](../architecture/consistency.md) — the outbox → consolidation flow the facade
  restructures.
- `internal/semantic/semantic.go` (`Point`, `PointID`, `Store`), `internal/consolidation/sink.go`
  (the current raw-turn embed this ADR replaces), `internal/sleepcycle/coordinator.go` (dedup +
  future Turn eviction).
