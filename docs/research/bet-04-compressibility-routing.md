---
type: Research Bet
title: "Bet 4 — Compressibility routing"
description: Use a query's incompressibility (bits the small model needs to explain it) as a difficulty estimator for dynamic LLM routing.
tags: [research, compression, llm-routing, cost-control, surprisal]
timestamp: 2026-07-08T00:00:00Z
---

# Bet 4 — Compressibility routing

> **Maturity:** 🟡 plausible, elegant.
> **One line:** what's hard is what's incompressible. Measure bits, don't train a classifier.

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

🟡 Medium. Main risk: threshold calibration and edge cases (short trivial queries with spuriously high
perplexity). Mitigation: combine with simple signals (length, presence of tool-intent) and
calibrate with real data before relying on bits alone.

## Dependencies

A local cheap model that gives perplexity fast. Ideally the [sovereign NN core (Bet 5)](bet-05-sovereign-nn-core.md), so the routing decision doesn't cost a network call.

## Next experiment

On a log of queries labeled by difficulty (did the cheap model suffice?), measure whether surprisal
separates the classes. If it separates, wire it into the router.

## References

- Kolmogorov complexity / perplexity as difficulty.
- [Bet 1 — surprisal-gated memory](bet-01-surprisal-gated-memory.md) · [Manifesto](../vision/compression-thesis.md)
