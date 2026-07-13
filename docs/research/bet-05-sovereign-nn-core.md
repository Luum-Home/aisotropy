---
type: Research Bet
title: "Bet 5 — Sovereign NN core"
description: An embeddings + small from-scratch model runtime, quantized, embedded in the binary (libnc/ts_server style), that runs all cognitive hot-path plumbing without going out to the network.
tags: [research, compression, sovereignty, inference, libnc, in-process]
timestamp: 2026-07-08T00:00:00Z
---

# Bet 5 — Sovereign NN core

> **Maturity:** 🔴 ambitious — the convergence vision. Built once the greens/yellows prove their value.
> **Promoted to next-invest substrate (2026-07-10, [ADR-0021](../decisions/0021-compression-signal-small-lm-surprisal-ceiling.md)):** the compression-bets round measured that small-LM subword surprisal is a *lexical*, not *semantic*, signal — so the viable path for Bet 1 (surprisal gate) and Bet 4 (routing) runs through **embedding-density / semantic distance**, which is this bet's territory. The next minimum experiment is a semantic-distance probe (does embedding-density separate redundant-vs-surprising / easy-vs-hard?), *before* any full NN-core build.
> **One line:** let Aisotropy think without going out to the network. This project's "run a 4G station on a laptop."

## Hypothesis

Today the hot path's "cognitive plumbing" — the surprisal scorer ([Bet 1](bet-01-surprisal-gated-memory.md)),
the reranker, the routing classifier ([Bet 4](bet-04-compressibility-routing.md)), the summarizer, the
codec ([Bet 2](bet-02-working-memory-codec.md)) — are **calls to external APIs**: the biggest
hole for latency, cost, and sovereignty. A **tiny, quantized, in-process NN core** (embeddings + a
small model) runs all of them locally, in µs, with no network.

## Rationale (why it's Bellard)

Bellard wrote **libnc** (a neural network in C, zero dependencies) and **ts_server**/TextSynth
(LLM inference in C). He proved that **a single person can build a competitive inference
stack from scratch.** That's exactly the level of ambition that separates Aisotropy from "just another
API wrapper." It's the climax of the [sovereignty thesis](../vision/thesis.md#sovereignty): the system
doesn't depend on third parties even to *think*.

## How it works

- A minimal runtime (Rust/C, no heavy deps) with: an embeddings model + a small quantized LLM
  (int8/int4), custom or minimal kernels.
- Embedded in the binary or as a lightweight sidecar; exposes: `logprob(x)`, `embed(x)`, `generate(...)`
  for internal consumers (bets 1–4, summary buffer).
- Large/frontier models remain external via [Gollem](../components/gollem-orchestrator.md);
  the sovereign core covers only high-frequency, low-complexity plumbing.

## Success criterion (in µs, MB, bits)

- **Latency:** cognitive hot-path operations (logprob/embed/rerank) in-process, **without the
  process-spawn + model-load tax**. *Measured reality* (`cmd/embed-bench`, 2026-07-13, e5-small on
  arm64-mac CPU): a warm in-process `embed()` floor is **~22 ms p50 (min 17 ms)** — an 18.8× win over the
  423 ms per-call subprocess, but **not µs**: for a BERT-class embedder on CPU the inference itself is
  tens of ms, so "µs" is unreachable by transport alone (it needs a smaller/quantized model, or GPU, or a
  cheaper op). The honest target is **low-tens-of-ms warm, zero spawn/load tax**; µs stays a per-op
  aspiration for the smallest ops, not a blanket claim. See [ADR-0023](../decisions/0023-sovereign-local-embedder-semantic-tier.md).
- **Footprint:** core + small model in a few MB of RAM; consistent with the Distroless binary from the
  [overview](../architecture/overview.md).
- **Network:** **zero** network calls for scoring/routing/rerank/summary.
- **Parity:** quality on those tasks within an ε of the external-API baseline (measured by bits/
  task quality, not by vibes).

## Architectural fit

- It's the **substrate** for bets 1–4 and the [codec (Bet 2)](bet-02-working-memory-codec.md) — it resolves
  the determinism constraint (the codec's model lives here, versioned).
- Doesn't replace Qdrant/Gollem/frontier; it removes from them the plumbing that currently pays for network.

## Risk and maturity

🔴 High. This is the most expensive bet: building/porting efficient quantized inference is a tour de force.
**Strategy:** don't start here. First validate the greens (1, 6) and yellows with "borrowed" local models
(external runtime). Once value is proven, internalize the runtime. Risk mitigated
by incrementalism: embeddings first (easiest), then logprob, then generation.

## Dependencies

None outward-facing (that's the point). Inward: consumes validation from bets 1–4.

## Next experiment

Prototype only `embed()` in-process (a small quantized embeddings model) and measure latency/footprint
vs. the current external API. If the numbers work out, extend to `logprob()`.

## References

- libnc / ts_server / TextSynth — <https://bellard.org/> · <https://textsynth.com/>
- [Thesis: sovereignty](../vision/thesis.md) · [Compression manifesto](../vision/compression-thesis.md)
