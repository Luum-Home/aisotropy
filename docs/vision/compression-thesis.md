---
type: Manifesto
title: The compression thesis — thinking like Bellard
description: Aisotropy's revolutionary core — compression = prediction = intelligence. Memory, retrieval, routing, and evaluation are a single operation measurable in bits.
tags: [vision, manifesto, compression, bellard, information-theory, north-star]
timestamp: 2026-07-08T00:00:00Z
---

# The compression thesis — thinking like Bellard

> Manifesto. This document sets Aisotropy's *revolutionary* ambition around AI, compaction,
> and efficiency. The base architecture ([Huma/Gollem/tiered memory](../architecture/overview.md))
> stands on its own; **this is the differentiating layer built on top of it.** The concrete,
> actionable bets live in [research/](../research/index.md).

## The pattern: Fabrice Bellard

Bellard doesn't *use* libraries: he writes them. FFmpeg, QEMU, TinyCC, QuickJS, JSLinux, a
software-based **4G/LTE base station**, his own dependency-free neural network library in C
(**libnc**), and an LLM inference engine in C (**ts_server** / TextSynth). He started his
career, in 1989, with **LZEXE** — an executable compressor. He closed the loop decades later
with **nncp** and **ts_zip**.

His method is always the same: **first principles, zero dependencies, absolute minimum,
measured in bits.** There's no magic; it's engineering pushed to the physical limit by one
person.

## The idea that reframes everything

Bellard already built the bridge Aisotropy needs. **nncp** led the *Large Text Compression
Benchmark* (Hutter Prize territory) using a neural network as a predictor and an arithmetic
coder that writes **only the bits of surprise**. **ts_zip** compresses text with an LLM at
ratios no classical compressor reaches. The conclusion isn't "just another compressor." It's
an operational theorem:

> **Compression = prediction = intelligence.**
> (Shannon → Hutter → Sutskever, demonstrated in practice by Bellard.)
> A model that predicts your next token well *is* a model that compresses you well.
> And compressing you well *is* understanding you.

## The four names for the same operation

Today Aisotropy treats efficiency as loose heuristics: *summary buffer*, *noise cleanup*,
*dedup*, *reranking*, *importance scoring*. Bellard would look at that and say: **they're
the same operation, mislabeled.** All of them are compression. Unifying them under a
bits-measurable principle is the leap:

| What we call it today… | …is actually | Honest measure |
|---|---|---|
| Noise cleanup / dedup / importance scoring | Discarding the predictable (low surprise) | surprisal bits `−log P(x)` |
| Summary buffer | Lossy compression of history | reconstructible bits/char |
| Reranking / context selection | Rate-distortion over a token budget | distortion at a fixed rate |
| LLM routing | Estimating difficulty = incompressibility | bits to "explain" the query |
| "The agent learned the user" | The model compresses their corpus better | bits/token/tenant over time |

## The thesis

> **In Aisotropy, memory, retrieval, routing, and evaluation aren't four subsystems: they're
> a single codec. The system's job is to minimize the bits needed to predict the user, and to
> spend the context window's bit budget optimally.**

This connects directly to
[context engineering](principles.md#2-context-engineering--prompt-engineering) and to the
[KB-over-RAG](principles.md#3-curated-knowledge-base--rag-for-stable-knowledge) principle: curating context *is* compressing; a
well-structured KB *is* a low-entropy corpus.

## North Star: bits-per-token-per-tenant

If compression = understanding, then **how many bits the system needs to predict a user is
the objective measure of how well it knows them.** Memory's job is to *bring that curve down
over time*. "The agent got smarter about Juan" stops being marketing and becomes
**measurable in bits** (see [Bet 6](../research/bet-06-bits-per-token-metric.md) and the
[OKRs](../roadmap/okrs.md)).

## The six bets

Ordered from "already proven by Bellard" to "research bet." Actionable detail, with success
criteria measured in bits, in [research/](../research/index.md).

1. 🟠 [Surprisal-gated memory](../research/bet-01-surprisal-gated-memory.md) — store bits, not text. **Spiked twice, inconclusive** — see [bet status](../research/index.md#the-ledger).
2. 🟡 [Working memory as codec](../research/bet-02-working-memory-codec.md) — nncp/ts_zip for the history.
3. 🟡 [Context as rate-distortion](../research/bet-03-rate-distortion-context.md) — optimization, not heuristics.
4. 🟡 [Compressibility-based routing](../research/bet-04-compressibility-routing.md) — bits as a difficulty signal.
5. 🔴 [Sovereign NN core](../research/bet-05-sovereign-nn-core.md) — libnc/ts_server in-process, zero network.
6. 🟢 [Bits-per-token-per-tenant](../research/bet-06-bits-per-token-metric.md) — the metric for "how well we know the user".

## Bellard discipline (how not to betray the thesis)

- **Measure in bits, not adjectives.** Every context optimization must report bits saved.
- **Zero dependencies in the cognitive hot path.** The ultimate goal is for Aisotropy to think without going out to the network ([Bet 5](../research/bet-05-sovereign-nn-core.md)).
- **Determinism.** Compressing with a model requires the *same* model to decompress → versioning and reproducible builds (a constraint, not a detail).
- **Trade storage/tokens for compute when compute is local and cheap.** That's the trade that enables everything.
- **The minimum that works.** Start with what the [spike](../../spikes/compression/README.md)
  actually supports — Bet 6, spiked and positive with two models — on top of the
  [already-decided Rust worker](../decisions/0005-datafusion-go-rust-boundary.md). Bet 1
  is spiked but inconclusive (🟠, see [bet status](../research/index.md#the-ledger)) and needs
  a re-run before it earns the same "start now" treatment; don't build the red one first
  either.
