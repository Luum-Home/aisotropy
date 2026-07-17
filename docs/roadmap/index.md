---
type: Section Index
title: Roadmap
description: Aisotropy's MVP-first, executable plan — measurable objectives (OKRs), milestones by phase, and what gets coded first.
tags: [roadmap, okr, mvp, milestones]
timestamp: 2026-07-08T00:00:00Z
---

# Roadmap

How to go from design to code, without building the entire skyscraper before having one
habitable room. Philosophy: **walking skeleton first** — the thinnest working end-to-end flow,
then thicken each layer.

## Documents

- [OKRs](okrs.md) — Measurable Objectives and Key Results by phase.
- [Milestones](milestones.md) — Breakdown by phase with "done" criteria.
- [Adoption learnings](adoption-learnings.md) — what the first external adopter validated and the
  evidence-driven generic gaps it revealed (frontend adapters, `WithDevUI`, a scaffold, a
  grounded-output check). Roadmap items that map to what a real dev hand-rolled.

> **Global status (2026-07-14):** Phases 0–7 implemented and Phase 8 (public adoption surface)
> shipped; Phase 9 (composition surface) partially delivered. The per-phase "done" criteria and
> what is actually **running vs dormant vs aspirational** live in
> [Milestones](milestones.md) and the [reality ledger](../architecture/reality-ledger.md).

## The phases at a glance

| Phase | Name | Goal | What it proves |
|---|---|---|---|
| 0 | Contracts | Huma + one struct → OpenAPI + validation | The single contract works. |
| 1 | Walking skeleton | Huma → Gollem → LLM → SSE, one tenant, no memory | TTFT and `ctx` cancellation. |
| 2 | Working memory | + BadgerDB (live chat + summary buffer) | Context that doesn't blow up in tokens. |
| 3 | Facts memory | + rqlite/LiteFS + FTS5 (Engram pattern) | Recall across sessions. |
| 4 | Semantic memory | + Qdrant with payload filtering | RAG isolated per tenant. |
| 5 | Consistency | Transactional Outbox + workers + reconciliation | The system doesn't desync if it crashes. |
| 6 | Sleep cycle | + DataFusion (dedup, contradictions, noise) | Offline self-improvement. |
| 7 | Granular control | Quotas, circuit breaker, scope-based tools, OTel | Ready for multi-tenant enterprise. |
| 8 | Adoption surface | Public API / SDK: register tools/executors/personas without editing `main.go` | A third party can build their bot on top **without forking**. |
| 9 | Composition surface | Both a hostable multi-tenant runtime **and** a composable framework (graphs/workflows/teams, embeddable bots) | Composition without regressing tenant isolation (sealed kernel). |

Detail and metrics in [OKRs](okrs.md); per-phase "done" criteria in [Milestones](milestones.md).
