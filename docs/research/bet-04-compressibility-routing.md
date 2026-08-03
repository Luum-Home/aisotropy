---
type: Research Bet
title: "Bet 4 — Compressibility routing"
description: Use a query's incompressibility (bits the small model needs to explain it) as a difficulty estimator for dynamic LLM routing.
tags: [research, compression, llm-routing, cost-control, surprisal]
timestamp: 2026-07-08T00:00:00Z
---

# Bet 4 — Compressibility routing

> **Maturity:** 🟡 plausible, elegant — but **refuted under EVERY cheap signal (2026-07-10)**.
> Difficulty vs: compressibility bits/tok **ρ=0.41** (`bet04_compressibility_routing.py`),
> embedding-density **ρ=−0.52** (`bet05_embedding_density_probe.py`), cascade output-entropy
> **ρ=0.56** (`bet04b_cascade_uncertainty.py`) — all length-confounded; plain byte-length **ρ=0.94**
> beats them all. **Difficulty is computational, not textual:** no prompt-intrinsic bits signal
> (lexical, semantic, or output-entropy) is a length-independent difficulty proxy. **Carve-out now
> tested & refuted (2026-07-11, `bet04c_instruct_cascade.py`, Qwen2.5-0.5B-Instruct):** an instruct
> model's answer-confidence measures **ρ 0.42–0.50** (entropy/neg-logprob/self-consistency) — same weak
> length-driven band, *below* the base cascade (0.56); the one >0.6 signal was a token-cap truncation
> artifact (caught, refuted). Cheap self-confidence, base *or* instruct, is not a difficulty router at
> 0.5B scale — a viable one needs a trained classifier or a measured frontier-vs-cheap answer-quality gap.
> **Competence ladder now run (2026-07-13, `bet04d_instruct_ladder.py`, 0.5B→1.5B; 3B excluded —
> Qwen Research License ≠ Apache-2.0):** self-confidence stays sub-usable as competence triples —
> greedy entropy ρ 0.50→0.51, neg-logprob 0.42→0.49 (both FLAT, gap −0.17/−0.18 *under* length),
> self-consistency-by-embedding 0.25→0.42 (RISES +0.16 but still <0.6). **No signal at any rung
> clears ρ≥0.6 with a gap≥0.20 over length** → self-confidence is a *mechanism* failure, not merely a
> 0.5B competence ceiling (only a frontier rung stays untested). **But the trained-classifier hatch is
> now MEASURED and holds (2026-07-13, `bet04e_trained_router.py`):** a supervised model on the e5
> embedding, after regressing length out, recovers difficulty at **length-residual ρ 0.672, p<1e-4** —
> the length-independent signal no *raw* intrinsic signal produced. The **answer-quality-gap** hatch
> (cheap 0.5B vs strong 1.5B) is **refuted at this scale** (ρ 0.330, n.s., length-driven); only a true
> frontier gap is untested.
> **One line:** what's hard is computational, not textual — you can't read difficulty off the prompt *or*
> the cheap model's certainty, but a *trained* classifier on the embedding can (off-thesis, supervised).

## Hypothesis

The current [dynamic LLM routing](../architecture/overview.md) classifies intent to decide
cheap model vs. frontier. Instead of a separately trained classifier, measure **how many bits the
cheap model needs to "explain" the query**: low surprisal (compressible) → the small model resolves it;
high surprisal (incompressible) → escalate to the frontier. Compressibility *is* a difficulty estimator.

## Rationale (why it's Bellard)

Complexity ≈ incompressibility (Kolmogorov). An input a model compresses well is an input it
"understands"; one it can't compress exceeds its capacity. It's the same surprisal signal from
[Bet 1](bet-01-surprisal-gated-memory.md), reused as a routing knob — zero new
infrastructure, a metric we already compute.

## How it works

- When a query arrives, compute its **perplexity/surprisal** under the local cheap model.
- `surprisal < θ` → resolve with the cheap model. `surprisal ≥ θ` → escalate to the frontier.
- The threshold `θ` is calibrated by cost/quality and can shift with the tenant's quota (graceful
  degradation: lower `θ` when the tenant is near its limit → more queries to the cheap model).

## Success criterion (in bits vs. cost)

- **Correlation:** the compressibility signal correlates with the real need to escalate (measured
  against a quality judge) above a threshold (e.g. ρ ≥ 0.6).
- **Cost/quality frontier:** at equal response quality, ↓ cost (fewer frontier calls); or at
  equal cost, ↑ quality. Dominates the baseline routing policy.

## Architectural fit

- Middleware upstream of [Gollem](../components/gollem-orchestrator.md); integrates with the
  [quota circuit breaker](../architecture/overview.md) (step 2 of the lifecycle).
- Reuses the same surprisal scorer from [Bet 1](bet-01-surprisal-gated-memory.md) → strong
  synergy: a single component serves both memory and routing.

## Risk and maturity

**Superseded by the 2026-07 refutation** (see the banner above and [ADR-0021](../decisions/0021-compression-signal-small-lm-surprisal-ceiling.md)): every cheap prompt-intrinsic signal was measured length-confounded and refuted, so the calibration risk below is moot except behind a trained classifier. The original framing follows.

🟡 Medium. Main risk: threshold calibration and edge cases (short trivial queries with spuriously high
perplexity). Mitigation: combine with simple signals (length, presence of tool-intent) and
calibrate with real data before relying on bits alone.

## Dependencies

A local cheap model that gives perplexity fast. Ideally the [sovereign NN core (Bet 5)](bet-05-sovereign-nn-core.md), so the routing decision doesn't cost a network call.

## Next experiment

**Superseded by the 2026-07 refutation** (see the banner above and [ADR-0021](../decisions/0021-compression-signal-small-lm-surprisal-ceiling.md)): the probe below has already been run and refuted for every cheap signal; the only live hatch is a trained classifier or a measured frontier-vs-cheap answer-quality gap. The original plan follows.

On a log of queries labeled by difficulty (did the cheap model suffice?), measure whether surprisal
separates the classes. If it separates, wire it into the router.

## References

- Kolmogorov complexity / perplexity as difficulty.
- [Bet 1 — surprisal-gated memory](bet-01-surprisal-gated-memory.md) · [Manifesto](../vision/compression-thesis.md)
