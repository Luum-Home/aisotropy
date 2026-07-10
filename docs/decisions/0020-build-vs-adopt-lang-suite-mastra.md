---
type: ADR
title: "ADR-0020 — Build Aisotropy vs. adopt the LangChain suite or Mastra"
description: Records the build-vs-adopt positioning as a falsifiable decision. Building a sovereign in-process Go+Rust runtime (rather than adopting LangChain+LangGraph+LangSmith or Mastra) is the right call for a specific ideal-customer profile — a Go-fluent team shipping a long-lived, multi-tenant, sovereignty-constrained SaaS — and the wrong call otherwise. Includes an honest win/loss scorecard and explicit "when this stops being true" triggers.
tags: [decisions, adr, positioning, build-vs-adopt, langchain, mastra, competitive, thesis]
timestamp: 2026-07-10T00:00:00Z
---

# ADR-0020 — Build Aisotropy vs. adopt the LangChain suite or Mastra

## Status

**✅ Accepted (2026-07-10).** This ADR promotes the analysis in
[competitive-landscape](../research/competitive-landscape.md) (a research report) into a
**decision record**: it states the positioning verdict, the ideal-customer profile the decision
serves, and — crucially — the conditions under which the decision should be **revisited**. It does
not change code. It depends on the [reality ledger](../architecture/reality-ledger.md) being
accurate; overstated capabilities would invalidate the "efficiency/sovereignty" side of the trade.

## Context

[ADR-0002](0002-anti-framework-magic-struct-as-contract.md) already discarded **LangChain** on
anti-magic grounds and chose the struct-as-contract approach. But no decision record captured the
**full build-vs-adopt trade** against the modern comparators a founder or a technical investor
would actually weigh: the **LangChain *suite*** (LangChain + LangGraph + LangSmith, 1.0 as of Oct
2025) and **Mastra** (TypeScript agent framework, $22M Series A, relicensed ELv2 → Apache-2.0 in
2026). "We didn't like LangChain classic in 2025" is not the same decision as "building a
from-scratch sovereign runtime beats adopting a funded, GA framework in 2026." This ADR makes that
second decision explicit and falsifiable.

The honest framing up front: **Aisotropy is a from-scratch runtime at Phases 0–9; the comparators
are shipping products with funding, large integration catalogs, and communities.** That maturity
gap is real and it colors every ecosystem/DX dimension. This ADR is a judge, not a fan — it names
where Aisotropy loses.

## Decision drivers

1. **Multi-tenant isolation as a correctness property, not an app-level convention.** The hardest
   thing to retrofit.
2. **Sovereignty / self-hostability** — everything on the operator's hardware, auditable, 100%
   MIT/Apache, no SaaS dependency in the critical path.
3. **Runtime efficiency** — µs in-process memory, MB-of-RAM binary, TTFT, controllable token spend.
4. **Team skill** — the single biggest hidden variable: Go-fluent + infra-owning vs. TS/Python.
5. **Time-to-market vs. long-horizon maintainability.**
6. **Ecosystem breadth** — pre-built integrations, community, docs, hiring pool.

## Decision

**Build Aisotropy — the sovereign in-process Go+Rust runtime — as the right choice for a specific
ideal-customer profile (ICP), and explicitly recommend adopting the LangChain suite or Mastra
outside it.** This is a *scoped* decision, not a claim of dominance.

### Where Aisotropy genuinely wins (validated)

| Dimension | vs. LangChain suite | vs. Mastra | Real today? |
|---|---|---|---|
| Multi-tenant isolation by construction | **wins** (its moat) | **wins** | ✅ (ctx + per-statement/key scoping, constitutional gate) |
| Sovereignty / licensing | **wins** (LangSmith is SaaS) | wins narrowly (Mastra was forced ELv2→Apache) | ✅ |
| Runtime efficiency (in-process µs memory, binary footprint) | **wins** | **wins** | ✅ (working-memory tier; see caveat on the token figure) |
| Dependency / supply-chain surface | **wins** | **wins** | ✅ (Go binary + one Rust subprocess) |
| No-magic / struct-as-contract (auditability) | **wins** | draw (Mastra easier to *start*, Aisotropy easier to *audit*) | ✅ |

### Where Aisotropy loses (conceded, not hidden)

| Dimension | Verdict |
|---|---|
| Ecosystem / integrations / community / docs maturity | **loses badly to both** — hundreds of LangChain integrations, Mastra's polished DX; Aisotropy has a tiny surface and no community. *This dimension can outweigh all the wins above for most teams.* |
| Time-to-first-prototype | **loses** — a RAG chatbot is an afternoon in either comparator; Aisotropy makes you build the pieces. |
| Long-term maintainability | **wins — but only conditionally**: Go-fluent team + long-lived multi-tenant product, past the (late) crossover where upfront build cost amortizes. For a TS/Python team it inverts into "maintaining a bespoke Go runtime nobody chose." |

## The ideal-customer profile (when this decision is right)

Aisotropy is the correct build **only when most of these hold**:

- The team is **Go-fluent and comfortable owning infrastructure**.
- The product is a **long-lived, multi-tenant SaaS** (not a prototype, not single-tenant, not
  consumer throwaway).
- There are **hard sovereignty / data-residency constraints** — on-prem, air-gapped, or regulated
  (HIPAA / GDPR / finance) where a SaaS observability dependency (LangSmith) is disqualifying.
- **Per-tenant cost attribution and isolation-correctness are non-negotiable**, not nice-to-haves.
- The horizon is **years**, so the upfront build cost amortizes.

Outside this profile, the smarter, simpler choice is:

- **LangChain suite** — a Python/TS team that needs many pre-built integrations fast, tolerating
  LangSmith's SaaS/pricing (or swapping Langfuse).
- **Mastra** — a TypeScript team optimizing DX and ergonomics over shaving the last dependency or
  microsecond; single-tenant or consumer; prototyping or pivot-heavy.

In one line: **Aisotropy trades ecosystem and time-to-demo for isolation, sovereignty, efficiency,
and long-horizon maintainability — a good trade for regulated multi-tenant Go shops, a bad trade
for almost everyone optimizing for speed-to-market or ecosystem breadth.**

## When this decision STOPS being true (revisit triggers)

This decision must be **re-opened** (new ADR) if any of these fire. They are the falsifiable core —
the reason this is a decision record and not marketing:

1. **Team composition shifts away from Go.** If the maintaining team becomes TS/Python-native and
   cannot hire Go infra engineers, the "maintainability win" inverts; adopt Mastra/LangChain.
2. **The product stops being multi-tenant** (pivots to single-tenant or consumer). The isolation
   moat — the strongest win — becomes inert, and you are paying the build cost for a property you
   no longer need.
3. **A comparator closes the sovereignty gap.** If LangGraph ships a fully OSS, self-hostable
   observability + durable-execution stack with no SaaS dependency, driver #2 weakens materially.
4. **A comparator ships isolation-by-construction.** If Mastra or LangGraph makes runtime-enforced
   tenant boundaries a first-class, non-opt-in property, driver #1 weakens.
5. **Time-to-market becomes the dominant constraint** (a funding round or competitive race demands
   shipping breadth in weeks). The ecosystem loss then outweighs every efficiency win.
6. **The efficiency claims fail to survive a fair benchmark.** The "−72% tokens" figure is measured
   against a *naive* full-history LangChain baseline; against a competently-configured LangChain
   (summarization/trimming middleware + Store) the gap narrows. If a fair benchmark shows the
   efficiency edge is marginal, the "efficiency" pillar of this decision is weaker than claimed.

## Consequences

**Positive**

- The positioning is now a citable, falsifiable artifact for technical hiring, fundraising, and
  onboarding — its rigor (falsifiable competitor claims, honest loss column) is the asset, not a
  liability.
- The ICP tells the team when to say *no* to a prospect (single-tenant, TS-only, prototype) instead
  of stretching Aisotropy into a fight it loses.

**Negative / honesty debt**

- The decision leans on efficiency and sovereignty claims that are only as true as the
  [reality ledger](../architecture/reality-ledger.md): today the sovereign Rust services (Qdrant,
  networked rqlite, DataFusion) are **dormant by default**, so the "in-process µs + independent Rust
  scale" story is half-wired. Until they are lit up and benchmarked end to end, the efficiency pillar
  rests on the in-process tier alone.
- The "−72% vs LangChain" headline must never be quoted without its synthetic-harness caveat, or the
  whole judge stance loses credibility.
- Choosing this path accepts the ecosystem/community deficit as a permanent cost of doing business,
  to be managed (a small, curated integration surface) rather than eliminated.

## References

- [ADR-0002 — Anti-framework magic, struct as contract](0002-anti-framework-magic-struct-as-contract.md) — the original LangChain rejection this ADR generalizes.
- [competitive-landscape](../research/competitive-landscape.md) — the research report promoted here.
- [reality ledger](../architecture/reality-ledger.md) — the honest as-built state this decision depends on.
- [thesis](../vision/thesis.md) · [principles](../vision/principles.md) — the sovereignty + measurement discipline.
- [Decisions index](index.md)
