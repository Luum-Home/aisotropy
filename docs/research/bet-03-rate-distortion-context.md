---
type: Research Bet
title: "Bet 3 — Context as rate-distortion"
description: Formalize context assembly as a rate-distortion problem — given a token budget, choose the memory that minimizes task distortion.
tags: [research, compression, context-engineering, rate-distortion, gollem]
timestamp: 2026-07-08T00:00:00Z
---

# Bet 3 — Context as rate-distortion

> **Maturity:** 🟡 solid theoretical framework, non-trivial engineering.
> **One line:** context isn't "surgically concatenated"; it's optimized against a budget.

## Hypothesis

Today [Gollem](../components/gollem-orchestrator.md) "surgically concatenates" and reranking
"brings the top-4." That's a heuristic. The real problem is one of **rate-distortion** (Shannon): given
a budget of `B` tokens (the *rate*), choose the memory subset/compression that **minimizes
task distortion** (the loss of response quality). Formalizing it yields an optimal policy instead of a fixed rule.

## Rationale (why it's Bellard)

Rate-distortion theory is the twin of lossy compression: how much quality is sacrificed for
each bit saved. Bellard thinks exactly this way in FFmpeg (lossy codecs): not "discard data,"
but **discard the data that matters least for perception**. Here, "perception" is the quality of
the agent's response.

## How it works

- Define a **distortion function** `D(response_with_reduced_context, ideal_response)` —
  it can be task-specific or learned per tenant.
- Solve: `min D` subject to `tokens(context) ≤ B`. In practice: a selector that scores each
  fragment by *quality gain per token* and fills the budget (knapsack / greedy submodular).
- The budget `B` can be dynamic: tied to the tenant's quota or to [routing (Bet 4)](bet-04-compressibility-routing.md).

## Success criterion (in bits/tokens)

- **D(B) curve:** at each fixed token budget, task distortion is **lower** than the
  heuristic baseline (top-k + concatenation). Pareto dominance in the (tokens, quality) plane.
- **Efficiency:** same response quality with ≥ X% fewer context tokens, or better quality at
  the same budget.

## Architectural fit

- Lives in the context-assembly step of [Gollem](../components/gollem-orchestrator.md) (step 4
  of the [lifecycle](../architecture/overview.md)).
- Consumes the three [tiered memory](../architecture/memory-tiers.md) layers as sources of
  candidate fragments; the reranker provides initial scores.
- It's the direct formalization of [context engineering](../vision/principles.md#2-context-engineering--prompt-engineering).

## Risk and maturity

🟡 Medium. The framework is solid; the difficulty is **defining/measuring distortion** cheaply in the
hot path (you can't run N responses per request). Mitigation: approximate distortion with a proxy
(embedding similarity, a small model) and reserve the expensive optimization for the sleep cycle, caching
policies per task type/tenant.

## Dependencies

A cheap distortion proxy and relevance scores (reranker). Benefits from the [NN core (Bet 5)](bet-05-sovereign-nn-core.md) for the in-process proxy.

## Next experiment

On a set of tasks with reference answers, compare *quality at fixed budget* between
heuristic top-k and a greedy selector by gain/token. If the D(B) curve dominates, it holds.

## References

- Rate-distortion theory (Shannon) — foundation.
- [Manifesto: the compression thesis](../vision/compression-thesis.md) · [Principles](../vision/principles.md)
