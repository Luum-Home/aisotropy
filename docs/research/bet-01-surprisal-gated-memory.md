---
type: Research Bet
title: "Bet 1 — Surprisal-gated memory"
description: Replace dedup/denoise/importance-scoring with a single information-theoretic criterion — store an engram only if it is surprising under the model.
tags: [research, compression, memory, sleep-cycle, surprisal]
timestamp: 2026-07-08T00:00:00Z
---

# Bet 1 — Surprisal-gated memory

> **Maturity:** 🟠 inconclusive — spiked twice, no clean separation found; needs a re-run
> with a changed method before this can move to 🟢. See "Risk and maturity" below and the
> [spike results](../../spikes/compression/README.md).
> **One line:** store bits, not text. An engram enters memory only if the model didn't already know it.

## Hypothesis

The current [sleep cycle](../components/datafusion-sleep-cycle.md) heuristics — "denoise",
"deduplicate", "importance scoring" — are three names for **one** operation:
discarding what's predictable. If we measure the **surprisal** of each engram candidate in bits, we get
a single, objective, tunable criterion that replaces all of them.

## Rationale (why it's Bellard)

This is the heart of **nncp**'s arithmetic coder: high-probability symbols cost almost
zero bits; you only pay for the unexpected. Applied to memory: if the base model **already predicts** a
fact from the user's profile, that fact is redundant — the model already "knows" it. Storing only
what's surprising curates memory down to the minimum information necessary.

## How it works

For each engram candidate `x` (a fact, preference, or episode extracted from the session):

```
surprisal(x) = − log₂ P(x | user_profile, facts_already_stored)
```

- `surprisal(x) < τ` (low surprisal) → **redundant**, not stored (or weighted down).
- `surprisal(x) ≥ τ` (high surprisal) → **real information** about this user → stored in
  [rqlite](../components/rqlite-facts-memory.md).

The threshold `τ` is a knob in bits, not an opaque heuristic. It also gives a **free importance
score** (more surprisal = more informative) and subsumes [contradiction detection](../architecture/memory-tiers.md)
(a fact that contradicts what's stored has very high surprisal → gets prioritized and triggers *supersede*).

## Success criterion (in bits)

- **Primary:** at equal retrieval quality (measured by a battery of recall tasks), the
  facts store is reduced by ≥ X% in bytes vs. the current heuristic criterion.
- **Ablation:** curve of *task quality* vs. *τ (bits)*. There must exist a τ where ≥ 30% of
  engrams are discarded without measurable quality loss.
- **Continuous metric:** average bits per retained engram, monitored over time.

## Architectural fit

- Runs on the **sleep cycle Rust worker** (batch/offline,
  already decided in [ADR-0005](../decisions/0005-datafusion-go-rust-boundary.md)) — doesn't touch the hot path.
- Needs a probability scorer. Initially an external API or a small model; eventually the
  [in-process sovereign NN core (Bet 5)](bet-05-sovereign-nn-core.md).
- Feeds directly into the [Bet 6](bet-06-bits-per-token-metric.md) metric.

## Risk and maturity

🟠 **Inconclusive, not low.** The underlying theory (arithmetic coding, surprisal as an
information measure) is sound, but the [compression spike](../../spikes/compression/README.md)
found the *specific* setup this bet proposes — sentence-level `surprisal(x)` with
`context=""` — does **not** cleanly separate redundant facts from surprising ones in practice.

Two runs, two models:

- `distilgpt2` (English-centric): binary classification against the naive midpoint threshold τ
  scored 2/7 redundant + 6/8 surprising correct — an overlapping middle band, not a clean gap.
- `DeepESP/gpt2-spanish` (the spike's definitive, Spanish-fluent model): 1/10 redundant + 3/10
  surprising correct — **worse** binary accuracy than the English model, even though it
  predicts Spanish text better overall.

Moving to a language-appropriate model ruled out "the model doesn't understand Spanish" as the
cause, but the overlap persisted and got worse. The spike's diagnosis: sentence-level
bits/token with no prior context measures **syntactic/lexical predictability** (how
grammatically ordinary the sentence is), not the **semantic novelty** of the fact relative to
what the system already knows about the user — which is what this bet actually needs to gate
memory correctly. The extremes still rank sensibly (specific named entities like "aisotropy",
"Fitzgerald", "AWS São Paulo" get the highest surprisal in both runs), but the wide middle band
is where redundant and surprising facts mix.

Real risk for a re-run: **cost of one forward pass per candidate** — mitigated because it's
batch/offline, over an already-distilled volume (not raw chat) — is still valid and unchanged.
That was never the blocking risk; the blocking risk is that the naive `context=""` setup
doesn't produce a usable threshold, which the "Next experiment" below addresses directly.

## Dependencies

A model that returns log-probs (or embeddings + a density proxy). Nothing else for the prototype.

## Next experiment (the minimum that validates/refutes)

**Superseded by the spike's diagnosis** (see "Risk and maturity" above and the
[spike results](../../spikes/compression/README.md)): the original plan below — compute
`surprisal` with `context=""` and plot recall quality vs. τ — is exactly the setup that came
back inconclusive twice. Before spending a full corpus-scale run on it, the re-spike needs to
change the *method*, not just the *scale*:

1. **Condition on real context.** Compute `surprisal(x | context)` with `context` = the user's
   actual stored profile + facts already retained, instead of `context=""` ("surprisal in a
   vacuum"). This is what the bet's own formula (`surprisal(x) = − log₂ P(x | user_profile,
   facts_already_stored)`) specified from the start — the spike tested the `context=""`
   degenerate case first because it's cheaper, and that's the case that didn't separate cleanly.
2. **And/or normalize for lexical rarity**, not raw bits — the spike's diagnosis is that raw
   sentence-level bits/token conflates "unusual phrasing" with "informative content"; a
   normalization that discounts for common grammar patterns may recover the semantic-novelty
   signal the raw metric currently misses.
3. Only after (1) and/or (2) show a clean gap on a small corpus should this scale to a full
   corpus of real sessions with a *recall quality* vs. *τ* plateau, as originally planned.

If a clean gap still doesn't appear after conditioning on real context, that's a genuine
refutation of the bet as specified — not just a model or scale problem, and worth documenting
as such.

## References

- nncp / Large Text Compression Benchmark — <https://bellard.org/nncp/>
- [Manifesto: the compression thesis](../vision/compression-thesis.md)
- [Compression spike — Bet 1 + Bet 6 results](../../spikes/compression/README.md) (real,
  reproducible numbers behind the 🟠 inconclusive rating above)
