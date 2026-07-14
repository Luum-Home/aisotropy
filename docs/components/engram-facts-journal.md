---
type: Component
title: "Engram — the Fact-kind aggregate facade over the facts journal"
description: internal/engram, the ADR-0017 phase-2 facade that makes "a fact and its Fact-kind vector live and die together" an invariant by construction — Save dual-indexes, Supersede/Delete remove both sides, gated by an importance threshold.
tags: [components, engram, facts, semantic, aggregate, dual-indexing, adr-0017]
timestamp: 2026-07-14T00:00:00Z
---

# Engram — the Fact-kind aggregate facade over the facts journal

The Engram-style **facts journal** itself (SQLite+FTS5 / rqlite, the distilled
decisions/preferences store) is covered by [rqlite — Facts Memory](rqlite-facts-memory.md);
its as-built implementation is [`internal/facts`](../../internal/facts/facts.go) (in-process
SQLite+FTS5 by default, rqlite via `RQLITE_URL`, byte-identical SQL in both backends). This
document covers the package literally named `engram`:
[`internal/engram`](../../internal/engram/engram.go), the **Fact-kind aggregate facade**
mandated by [ADR-0017](../decisions/0017-semantic-tier-multi-kind-memory.md) phase 2.

Status (2026-07-14): **REAL** — constructed unconditionally in
[`aisotropy/app.go`](../../aisotropy/app.go) and used by both fact-mutating paths (the
[consolidation sink](consolidation-outbox.md) and the sleep cycle's dedup deletes).

## What problem it solves

Once ADR-0017 gave facts a vector projection in the [semantic tier](qdrant-semantic-memory.md)
(`semantic.KindFact`), a fact lived in two stores at once — and the two could drift:

- **Bug #1 (stale vector after supersede):** superseding a fact in the facts tier left its old
  Fact-kind vector retrievable forever.
- **Bug #2 (dedup orphan):** the sleep cycle's dedup dropped a fact row but left its vector
  behind.

`engram.Store` closes both by construction. It is the *scoped* aggregate root — deliberately
NOT an aggregate over the whole semantic tier (a distilled fact and a raw conversational turn
are different memories with independent lifecycles), but the **sole owner of the one kind
where a vector truly is a projection of a fact**: `KindFact`. It never touches `KindTurn`
(that path stays in [`internal/consolidation`](consolidation-outbox.md)) or `KindDocument`
(knowledge ingestion, ADR-0018).

## The API — invariant by construction

`engram.Store` composes `facts.Store`, `semantic.Store`, and `semantic.Embedder`
(Semantic/Embedder optional — nil means facts-only, no dual-indexing; Facts required):

| Method | Facts tier | Semantic tier (Fact-kind) |
|---|---|---|
| `Save` | `Facts.Save` (upsert by content-derived id) | Embed + `Upsert` the content as a `KindFact` point keyed by the fact id — a fresh fact is born with its vector in place |
| `Supersede` | `Facts.Supersede(oldID, newFact)` | `Delete` oldID's vector (closes bug #1), then dual-index newFact |
| `Delete` | `Facts.Delete(ids)` | `Delete` the corresponding Fact-kind vectors (closes bug #2) |

Per the ADR's "Mandatory going forward": **every Fact-kind vector mutation MUST go through
this facade** — never `internal/semantic` directly for Fact-kind vectors. Point IDs are the
deterministic `semantic.PointID(scope, KindFact, factID)`, so every write stays idempotent
under the outbox's at-least-once delivery. Scopes are derived through the blessed
`ScopeFromContext` constructors (ADR-0011) with the caller's ctx threaded through.

### The importance gate

`MinImportance` gates dual-indexing (ADR-0017 open question #2): a fact's content is embedded
into a Fact-kind vector only when its `Importance >= MinImportance`; the zero value
dual-indexes every fact. The runtime threads one value
(`AISOTROPY_FACT_MIN_IMPORTANCE`, parsed in `app.go`) into **every** facade construction —
the consolidation sink and the sleep cycle — so the gate is decided consistently regardless
of which path built the facade.

## Who uses it

- **[Consolidation sink](consolidation-outbox.md)**
  ([`internal/consolidation/sink.go`](../../internal/consolidation/sink.go)): every fact
  distilled from a drained outbox event is saved through `engram.Store.Save`, never
  `facts.Store.Save` directly.
- **Sleep cycle** ([`internal/sleepcycle`](datafusion-sleep-cycle.md)): the DataFusion dedup's
  fact deletions route through the facade (`Engram` field of the coordinator in `app.go`), so
  dropping a duplicate fact also drops its vector.

## Code map

| Path | What it is |
|---|---|
| [`internal/engram/engram.go`](../../internal/engram/engram.go) | `Store` — `Save` / `Supersede` / `Delete` / the importance-gated `indexFact` |
| [`internal/facts`](../../internal/facts/facts.go) | The facts journal the facade fronts (see [rqlite — Facts Memory](rqlite-facts-memory.md)) |
| [`internal/semantic`](../../internal/semantic/semantic.go) | The vector store holding the `KindFact` projections |
| [`aisotropy/app.go`](../../aisotropy/app.go) | Facade construction + `AISOTROPY_FACT_MIN_IMPORTANCE` threading |

## Gotchas

- **Naming collision with the dev tool.** This package implements the *Engram pattern*
  (a learning journal of distilled facts) inside aisotropy's memory hierarchy; it is unrelated
  to any external tool of the same name.
- **Facts-only deployments degrade gracefully.** With nil Semantic/Embedder the facade is a
  thin pass-through to the facts tier — no dual-indexing, no vector deletes — and callers need
  no branching.
- **A raised importance gate is not retroactive.** Facts saved below a later, lower gate value
  are not back-filled with vectors; the gate applies at write time only.

## Related decisions / references

- [ADR-0017](../decisions/0017-semantic-tier-multi-kind-memory.md) — multi-kind semantic tier;
  phase 2 mandates this facade.
- [ADR-0011](../decisions/0011-tenant-scoped-statement-builder.md) — blessed scope
  constructors used on every write.
- [rqlite — Facts Memory](rqlite-facts-memory.md) — the journal design (data model, FTS5,
  scoring, consistency levels).
- [Consolidation outbox](consolidation-outbox.md) · [Embedder](embedder.md) — the write path
  feeding this facade.
