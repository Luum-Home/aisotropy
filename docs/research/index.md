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

> **The measured numbers live in [evaluation](evaluation.md)** — the canonical node that surfaces
> the real, reproducible results (token-economy reduction, Bet 6's +1.469 bits/token offline lift,
> Bet 1's inconclusive spike, the distroless footprint) into the graph, each with the caveat that
> keeps it honest. Read a bet's hypothesis here; read what has actually been measured there.

## The ledger

| # | Bet | Maturity | Success criterion (summary) |
|---|---|---|---|
| 1 | [Surprisal-gated memory](bet-01-surprisal-gated-memory.md) | 🟠 mechanism partial via surprisal (0.72 AUC, no clean gap) — but **embedding-density separates it far better (AUC 0.89, d=1.61)**; the viable gate is the *semantic* soft-weight (Bet 5), not surprisal | ↓ memory size at equal retrieval quality |
| 2 | [Working memory as a codec](bet-02-working-memory-codec.md) | 🟢 method confirmed — lossless + beats gzip (−42%→−54%); ladder **2.34→1.88 bits/char (82M→774M)**, plateaus **~1.9 ≫ 1.0**. Tokenizer confound quantified: DeepESP 124M-Spanish = **1.97 ≈ gpt2-medium 355M-English** (matched tokenizer ≈ 1.5 doublings). < 1 bit/char needs nncp-scale even with a matched tokenizer | < 1 bit/char, 100% lossless reconstruction |
| 3 | [Context as rate-distortion](bet-03-rate-distortion-context.md) | 🟡 **first support measured**: smart selection beats naive by **+0.47/+0.68 bpt** at constrained budgets (B=64–256); gap→noise once budget ≥ total content. Selection > volume in the budget-bound regime | lower distortion at fixed token budget |
| 4 | [Compressibility routing](bet-04-compressibility-routing.md) | 🟡 **refuted under every cheap signal** — compressibility ρ=0.41, embedding-density ρ=−0.52, cascade output-entropy ρ=0.56 (all length in disguise; raw length ρ=0.94 wins). Difficulty is computational, not textual; a real cascade needs an *instruct* model (off-thesis) | bits↔difficulty correlation; ↓ cost without ↓ quality |
| 5 | [Sovereign NN core](bet-05-sovereign-nn-core.md) | 🔴 ambitious — **next-invest substrate with evidence** (ADR-0021): embedding-density (borrowed MIT model) separates Bet-1 novelty at **AUC 0.89**; the `embed()`-first path is now measured, not just a vision | cognitive hot path in µs, 0 network calls |
| 6 | [Bits-per-token-per-tenant](bet-06-bits-per-token-metric.md) | 🟢 solid | downward curve over time per tenant |

**Legend:** 🟢 start now · 🟡 research phase · 🟠 spiked, inconclusive (needs a re-run with a
changed method before trusting either direction) · 🔴 convergence vision.

**Why Bet 1 is 🟠, not 🟢:** the [compression spike](../../spikes/compression/README.md) ran
this bet twice, against two different causal LMs (`distilgpt2`, then the Spanish-fluent
`DeepESP/gpt2-spanish`, the spike's definitive model). Neither run found a clean surprisal gap
between the "redundant" and "surprising" fact groups: binary classification against the naive
midpoint threshold τ scored 2/7 redundant + 6/8 surprising correct with `distilgpt2`, and
scored *worse* with the Spanish-fluent model — 1/10 redundant + 3/10 surprising correct with
`DeepESP/gpt2-spanish`. Moving to a model that actually understands Spanish ruled out "the
model doesn't know the language" as the cause but did not close the gap; the spike's revised
diagnosis is that sentence-level bits/token measured with `context=""` (surprisal "in a
vacuum") captures **syntactic/lexical predictability**, not the **semantic novelty** the bet
needs to separate the two groups. Bet 6, spiked in the same session, came back positive with
both models (+1.469 and +1.731 bits/token memory lift) — today only Bet 6 has supporting
evidence; Bet 1 does not. A decisive re-run, per the spike's own conclusion and
[bet-01's "Next experiment"](bet-01-surprisal-gated-memory.md#next-experiment-the-minimum-that-validatesrefutes)
note, needs `context` = the user's real profile instead of `context=""`, and/or normalizing for
lexical rarity instead of using raw bits — not just a bigger or better-fit model.

## Recommended order

1. **Bet 6 first:** it builds on the already-decided [sleep cycle Rust worker](../decisions/0005-datafusion-go-rust-boundary.md), is 🟢 solid, has a positive spike result with two models, and instruments the North Star metric.
2. **Bet 1's re-run:** before treating it as "start now," re-spike with `context` = real user profile (not `context=""`) and/or lexical-rarity normalization, per the spike's own diagnosis above. Do not build the sleep-cycle surprisal gate on the current inconclusive evidence.
3. **Yellows (2, 3, 4):** research with bounded prototypes; each has its own "minimum experiment."
4. **Red (5):** the convergence — the in-process NN core that everything else points to. Built once the greens/yellows prove their value.

## Relationship to the rest of the KB

- Rationale: [compression manifesto](../vision/compression-thesis.md) and [principles](../vision/principles.md).
- Execution substrate: [tiered memory](../architecture/memory-tiers.md) and the [sleep cycle](../components/datafusion-sleep-cycle.md).
- Metric: the [OKRs](../roadmap/okrs.md) (objective O8).

## Related research

- [Competitive landscape](competitive-landscape.md) — where aisotropy sits vs. LangChain/LangGraph,
  Mastra, Letta, and the Go-native contenders (Eino, ADK Go, Genkit Go), with the memory-window
  time-to-threat judgment (mid-2026 snapshot).
