---
type: Section Index
title: Research bets — compression as a cognitive layer
description: The six actionable bets that bring the compression thesis down to experiments with a success criterion in bits.
tags: [research, compression, bellard, bets]
timestamp: 2026-07-08T00:00:00Z
---

# Research bets — compression as a cognitive layer

These six bets bring the [compression thesis](../vision/compression-thesis.md) manifesto
down to concrete experiments. Each doc follows the same template: **Hypothesis · Rationale · How it works ·
Success criterion (in bits) · Architectural fit · Risk and maturity · Dependencies · Next
experiment**. The Bellard rule: nothing is declared "better" without a number in bits.

## The ledger

| # | Bet | Maturity | Success criterion (summary) |
|---|---|---|---|
| 1 | [Surprisal-gated memory](bet-01-surprisal-gated-memory.md) | 🟢 solid | ↓ memory size at equal retrieval quality |
| 2 | [Working memory as a codec](bet-02-working-memory-codec.md) | 🟡 proven by Bellard | < 1 bit/char, 100% lossless reconstruction |
| 3 | [Context as rate-distortion](bet-03-rate-distortion-context.md) | 🟡 solid framework | lower distortion at fixed token budget |
| 4 | [Compressibility routing](bet-04-compressibility-routing.md) | 🟡 plausible | bits↔difficulty correlation; ↓ cost without ↓ quality |
| 5 | [Sovereign NN core](bet-05-sovereign-nn-core.md) | 🔴 ambitious | cognitive hot path in µs, 0 network calls |
| 6 | [Bits-per-token-per-tenant](bet-06-bits-per-token-metric.md) | 🟢 solid | downward curve over time per tenant |

**Legend:** 🟢 start now · 🟡 research phase · 🔴 convergence vision.

## Recommended order

1. **Greens first (1 and 6):** they build on the already-decided [sleep cycle Rust worker](../decisions/0005-datafusion-go-rust-boundary.md). Low risk, immediate value, and they instrument the North Star metric.
2. **Yellows (2, 3, 4):** research with bounded prototypes; each has its own "minimum experiment."
3. **Red (5):** the convergence — the in-process NN core that everything else points to. Built once the greens/yellows prove their value.

## Relationship to the rest of the KB

- Rationale: [compression manifesto](../vision/compression-thesis.md) and [principles](../vision/principles.md).
- Execution substrate: [tiered memory](../architecture/memory-tiers.md) and the [sleep cycle](../components/datafusion-sleep-cycle.md).
- Metric: the [OKRs](../roadmap/okrs.md) (objective O8).
