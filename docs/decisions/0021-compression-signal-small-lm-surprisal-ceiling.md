---
type: ADR
title: "ADR-0021 — The compression bets' signal ceiling: small-LM surprisal ≠ semantic/difficulty; route the semantic bets through embedding-density (Bet 5), not more small-LM spikes"
description: Records the measured conclusion of the 2026-07-10 compression-bets round (Bets 1/2/4) — subword bits/token from a small LM measures syntactic/lexical predictability, not the semantic novelty (Bet 1) or reasoning difficulty (Bet 4) those bets need — and the resulting research-prioritization decision: pursue Bet 1's soft-weight framing and Bet 4's replacement via embedding-density / the sovereign NN core (Bet 5), keep Bet 2's codec as a scale question, and stop iterating small-LM-surprisal spikes for the semantic signals.
tags: [decisions, adr, research, compression, bets, surprisal, embeddings, bet-1, bet-2, bet-4, bet-5, prioritization]
timestamp: 2026-07-10T00:00:00Z
---

# ADR-0021 — The compression bets' signal ceiling

## Status

✅ **Accepted (2026-07-10).** Records a **measured** finding (not an opinion) from the compression-bets
research round and the research-prioritization decision it forces. Grounded in
[evaluation.md](../research/evaluation.md) (the canonical measured-numbers node) and the three spikes
committed this round. Complements the compression thesis
([compression-thesis](../vision/compression-thesis.md)) and the bets ledger
([research index](../research/index.md)).

## Context

The compression thesis ("compression = prediction = intelligence", Bellard) is instrumented by six
bets. Bet 6 (bits-per-token North Star) is done. This round ran the decisive minimum experiments for
**Bets 1, 2, and 4** on real LMs, under the Bellard rule (no claim without a number in bits). The
results ([evaluation.md](../research/evaluation.md)):

- **Bet 1 — surprisal-gated memory** ([`bet01_rerun_context_profile.py`](../../spikes/compression/bet01_rerun_context_profile.py)):
  the decisive re-run (profile-conditioning + rarity-normalization, the two fixes the prior spike
  prescribed). Profile-conditioning **confirms the direction** (+0.10 AUC on both models: DeepESP
  0.61→0.72, distilgpt2 0.73→0.83; gap +1.14 bits/tok, d≈1.0), but the best is only **0.72 AUC /
  15-of-20** on the definitive Spanish model — **no clean separating gap** — and
  rarity-normalization *alone* is **refuted** (AUC 0.49/0.34).
- **Bet 4 — compressibility routing** ([`bet04_compressibility_routing.py`](../../spikes/compression/bet04_compressibility_routing.py)):
  the length-free signal (bits/token) correlates with difficulty only **ρ=0.41** (below the ρ≥0.6
  bar); every signal that clears 0.6 (bits_total, gzip) is **raw length in disguise** — plain byte
  length scores **ρ=0.94**, beating all of them.
- **Bet 2 — working-memory codec** ([`bet02_lm_arithmetic_codec.py`](../../spikes/compression/bet02_lm_arithmetic_codec.py)):
  LM + arithmetic coder → **2.34 bits/char, byte-for-byte lossless, beats gzip ~42%** (2.34 vs 4.06).
  The **< 1 bit/char target is not reached with distilgpt2** — a model-capacity ceiling.

**The load-bearing pattern (measured twice):** a **small LM's subword `bits/token` measures
syntactic/lexical predictability, not the semantic novelty (Bet 1) or reasoning difficulty (Bet 4)
those bets actually need.** It flags rare tokens and long strings, not "the model already knew this
about the user" or "this prompt is hard to reason about." Stripping lexical rarity (Bet 2's FIX2)
*removes* the signal (AUC 0.49); adding length *is* the signal (Bet 4 ρ=0.94). This is the same
failure the README first noted for the `context=""` runs, now confirmed across profile-conditioning
and difficulty-routing. Bet 2 is different: it *uses* the LM as a probability model for lossless
coding, where lexical predictability is exactly the right thing — so it works, bounded only by scale.

## Goals / Non-goals

**Goals**
- Record the ceiling as a **measured decision** so it is not re-discovered by a fourth small-LM spike.
- Reprioritize the compression-bets research toward the signal that the measurements point at.

**Non-goals**
- **Not** abandoning the compression thesis — Bet 2 (codec) and Bet 6 (North Star) both hold; only the
  *small-LM-surprisal instrument* for the **semantic** bets is limited.
- **Not** committing to build Bet 5 now — this ADR reprioritizes it as the enabling substrate; the
  build itself remains a separate, large, ADR-gated decision.

## Options considered

### Option A — Keep iterating small-LM-surprisal spikes for Bets 1 & 4 — rejected
Try more models / thresholds / normalizations. Rejected: three spikes now agree the signal is
lexical, not semantic; the definitive Spanish model tops out at 0.72 AUC (Bet 1) and 0.41 ρ (Bet 4).
Diminishing returns against a measured ceiling — the classic "one more epoch will fix it" trap.

### Option B — Abandon the compression-as-cognition thesis — rejected
Overcorrects. Bet 2 is method-confirmed (lossless, beats gzip 42%) and Bet 6 is the working North
Star. The thesis is intact; only one *instrument* (small-LM subword surprisal) is the wrong probe for
semantic/difficulty signal.

### Option C — Route the semantic-signal bets through embedding-density / the sovereign NN core (Bet 5); keep Bet 2 as a scale track; keep Bet 6 as measurement — chosen
Separate the bets by *what signal they need*:
- **Lexical-predictability signal** (Bet 2 codec, Bet 6 measurement) → small LM is correct; keep going,
  scale later.
- **Semantic/difficulty signal** (Bet 1 surprisal gate, Bet 4 routing) → small-LM surprisal is the
  wrong probe; the right one is **embedding-density / semantic distance** (is a fact *close* to what
  the profile already implies? is a prompt *far* from the covered manifold?), which is the
  [sovereign NN core (Bet 5)](../research/bet-05-sovereign-nn-core.md)'s territory.

## Decision

**Adopt Option C.**

1. **Bet 1 — reframe to a soft weight, drop the hard gate.** Use surprisal as a *continuous importance
   weight* (it directionally works, d≈1.0), **not** a binary sleep-cycle gate (no clean 0.72-AUC gap).
   Do **not** build the sleep-cycle surprisal gate on the current evidence.
2. **Bet 4 — mark refuted as a length-independent proxy.** Compressibility-per-token is not a reliable
   difficulty signal; a plain `len(prompt)` router beats it. Any future difficulty router must use a
   *semantic* signal, not compressibility.
3. **The semantic bets' next probe is embedding-density, i.e. Bet 5.** This measured result promotes
   [Bet 5](../research/bet-05-sovereign-nn-core.md) from "far 🔴 convergence" to **the enabling
   substrate the greens/yellows now point at** — the next-invest research candidate, when invested in.
4. **Bet 2 — method confirmed; `< 1 bit/char` is a model-scale question**, deferred until a larger
   sovereign model exists (also Bet-5-adjacent). Keep the codec track.
5. **Bet 6 stays the measurement North Star** (already instrumented, ADR-0014/0015).

## Consequences

**Positive**
- The negative/partial results are preserved as **evidence in `evaluation.md`**, so the ceiling is a
  decision, not a forgotten dead end. No fourth small-LM spike for the semantic signal.
- Research effort has a clear next direction (semantic/embedding-density) grounded in measurement, not
  vibes — consistent with the project's "if you haven't measured it, it's not a claim" discipline.

**Negative / debt**
- The two most product-relevant bets for the sleep cycle (surprisal gate = memory pruning; routing =
  cost) are **not** yet viable, and their viable path (Bet 5) is the most ambitious/expensive bet.
  The compression *cost/pruning* payoff is therefore further out than the North-Star measurement.

## Open questions

1. **Does embedding-density actually separate redundant vs. surprising / easy vs. hard?** That is the
   Bet-5-substrate spike this ADR implies but does not run — the next minimum experiment (semantic
   distance to a profile/coverage manifold), before any Bet-5 build.
2. **Which larger model** clears Bet 2's `< 1 bit/char` and sharpens Bet 1's signal, within the
   sovereignty/MIT-Apache constraint (ADR-0001).
3. **Bet 3 (rate-distortion)** is still un-spiked; it builds on 2/4 and should be sequenced after the
   embedding-density probe.

## References

- [evaluation.md](../research/evaluation.md) — the measured numbers (Bets 1/2/4/6) with caveats.
- [research index](../research/index.md) — the bets ledger (maturities updated this round).
- Bets: [1](../research/bet-01-surprisal-gated-memory.md) · [2](../research/bet-02-working-memory-codec.md) · [4](../research/bet-04-compressibility-routing.md) · [5](../research/bet-05-sovereign-nn-core.md) · [6](../research/bet-06-bits-per-token-metric.md)
- Spikes: [`bet01_rerun_context_profile.py`](../../spikes/compression/bet01_rerun_context_profile.py), [`bet02_lm_arithmetic_codec.py`](../../spikes/compression/bet02_lm_arithmetic_codec.py), [`bet04_compressibility_routing.py`](../../spikes/compression/bet04_compressibility_routing.py)
- [compression thesis](../vision/compression-thesis.md) — the manifesto this round tests.
