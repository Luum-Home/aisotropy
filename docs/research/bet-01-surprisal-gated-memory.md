---
type: Research Bet
title: "Bet 1 — Surprisal-gated memory"
description: Replace dedup/denoise/importance-scoring with a single information-theoretic criterion — store an engram only if it is surprising under the model.
tags: [research, compression, memory, sleep-cycle, surprisal]
timestamp: 2026-07-08T00:00:00Z
---

# Bet 1 — Surprisal-gated memory

> **Maturity:** 🟢 solid — the easiest to prototype, the pragmatic entry point.
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

🟢 Low. The theory is proven (arithmetic coding). Single risk: cost of one forward pass per
candidate — **mitigated** because it's batch/offline and over an already-distilled volume (not raw chat).

## Dependencies

A model that returns log-probs (or embeddings + a density proxy). Nothing else for the prototype.

## Next experiment (the minimum that validates/refutes)

Take a corpus of real sessions, extract candidate engrams, compute `surprisal` with a small
LLM, and plot *recall quality* vs *τ*. If the plateau appears (discarding engrams without losing recall),
the bet is validated.

## References

- nncp / Large Text Compression Benchmark — <https://bellard.org/nncp/>
- [Manifesto: the compression thesis](../vision/compression-thesis.md)
