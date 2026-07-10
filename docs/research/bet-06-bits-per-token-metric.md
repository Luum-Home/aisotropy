---
type: Research Bet
title: "Bet 6 — Bits-per-token-per-tenant"
description: Elevate compression of a tenant's corpus to a North Star metric — how many bits the system needs to predict a user objectively measures how well it knows them.
tags: [research, compression, metrics, north-star, okr, observability]
timestamp: 2026-07-08T00:00:00Z
---

# Bet 6 — Bits-per-token-per-tenant

> **Maturity:** 🟢 solid — derived, almost free to instrument. The metric that gives everything meaning.
> **One line:** "the agent learned the user" becomes measurable, in bits.

## Hypothesis

If compression = understanding, then **the bits the system needs to predict a tenant's next
token are the objective measure of how well it knows them.** The job of [tiered
memory](../architecture/memory-tiers.md) is to *lower that curve over time*. A memory that's working
turns the user's history into something more predictable → fewer bits.

## Rationale (why it's Bellard)

It's the metric of every compressor: **bits per symbol**. nncp/ts_zip rank themselves this way. Translated to
the agent: `bits/token` of a tenant's corpus, evaluated by the model *with* hydrated memory vs.
*without* it. The drop is the value memory adds — quantified, not narrated.

## How it works

```
bpt(tenant, t) = − (1/N) Σ log₂ P(token_i | hydrated_context, memory(tenant, t))
```

- Measure `bpt` periodically over a held-out sample of the tenant's traffic.
- **Memory lift** = `bpt_without_memory − bpt_with_memory` (how many bits memory saves).
- Track the **time series**: a downward slope = the system knows the tenant better and better.

## Success criterion (in bits)

- **Positive lift:** `bpt_with_memory < bpt_without_memory` consistently (memory *helps*).
- **Trend:** the `bpt(tenant, t)` curve **decreases** over time for active tenants (real learning).
- **Sanity:** bets 1–3 must move this needle; if they optimize and `bpt` doesn't drop, they're not working.

## Architectural fit

- Computed **offline, in batch** — NOT as a live serving-span attribute. No major serving provider
  exposes the log-probs this metric needs, so a pinned reference model
  ([ADR-0015](../decisions/index.md): `distilgpt2`) re-scores exported turns during the sleep cycle,
  and the result is attached as a **derived** attribute to a dashboard/report, not to the
  request-path [OTel](../architecture/observability.md) span. This is the ADR-0014 decision — the
  "cheap derivative of serving log-probs" framing was the original design and was **rejected**
  because serving providers don't surface them. See
  [bitrate/bpt scorer](../components/bitrate-bpt-scorer.md).
- It's the **fitness function** for the other bets: [Bet 1](bet-01-surprisal-gated-memory.md),
  [Bet 2](bet-02-working-memory-codec.md), and [Bet 3](bet-03-rate-distortion-context.md) are validated by
  their effect on `bpt`.
- Feeds into the [OKRs](../roadmap/okrs.md) as objective O8.

## Risk and maturity

🟢 Low. Single risk: defining a **stable reference model** so the metric stays comparable
over time (if the base model changes, `bpt` isn't comparable). Mitigation: pin a versioned
evaluation model (same determinism problem as [Bet 2](bet-02-working-memory-codec.md);
same solution).

## Dependencies

A pinned **offline** reference model that yields log-probs over exported text
([ADR-0015](../decisions/index.md): `distilgpt2`, env-gated Python runtime) — **not**
serving-provider log-probs, which no major provider exposes. That absence is precisely why `bpt`
is an offline-batch metric ([ADR-0014](../decisions/index.md)), not a live serving-span attribute.

## Next experiment

Instrument `bpt` with and without memory on a pilot tenant over several weeks. If a positive
lift and downward trend appear, the metric is alive and guides the rest of the bets.

## References

- bits-per-character / cross-entropy as a compression measure (nncp benchmark).
- [Compression manifesto](../vision/compression-thesis.md) · [OKRs](../roadmap/okrs.md)
