---
type: Research Bet
title: "Bet 2 — Working memory as a codec"
description: Store chat history arithmetically encoded against a local model (nncp/ts_zip technique) — near-lossless at a fraction of the bits, instead of a lossy summary buffer.
tags: [research, compression, badgerdb, working-memory, arithmetic-coding, nncp]
timestamp: 2026-07-08T00:00:00Z
---

# Bet 2 — Working memory as a codec

> **Maturity:** 🟢 **method confirmed in-repo (2026-07-10)** on top of "proven by Bellard".
> `bet02_lm_arithmetic_codec.py` (LM + a pure-Python arithmetic coder) compresses a chat history
> **byte-for-byte lossless**, **beating gzip** (4.06 bits/char). The GPT-2 ladder
> (`bet02_model_ladder.py`) descends monotonically **2.34 → 2.21 → 1.98 → 1.88 bits/char**
> (82M→124M→355M→774M; margin over gzip widens −42%→−54%). The **< 1 bit/char target is NOT reached** —
> it plateaus ~1.9, extrapolating to nncp-scale models. Tokenizer confound **quantified 2026-07-11**
> (`bet02_lm_arithmetic_codec.py`, recorded in `spikes/compression/bet02_model_ladder_results.md`): DeepESP
> (124M, Spanish) reaches **1.97 bits/char ≈ gpt2-medium (355M, English)** on the same text — a
> language-matched tokenizer buys ~1.5 param-doublings. But 1.97 is still ~2× the target, so `< 1
> bit/char` needs nncp-scale models **even with** a matched tokenizer. Method validated; the target is a
> scale-and-tokenizer question, not a method failure.
> **One line:** history isn't lossily summarized; it's compressed against the model and reconstructed.

## Hypothesis

The current [summary buffer](../architecture/memory-tiers.md) **loses information** (it condenses
old messages into a paragraph). If instead we encode the history **arithmetically against the predictions
of a local model** (the exact ts_zip technique), we get **near-lossless** history at a
fraction of the bits: what the model could anticipate costs almost nothing; you only pay for the unpredictable.

## Rationale (why it's Bellard)

**ts_zip** compresses text with an LLM at ratios no classical compressor reaches (< 1 bit/char
territory, vs. ~8 for raw UTF-8 and ~2–3 for gzip). **nncp** does the same, leading the text
compression benchmark. It's the quintessential Bellard trade: **trading storage for compute** — and the
compute is local and cheap.

## How it works

- **Compress:** run the local model over the history, feed its probabilities into an
  arithmetic coder, store the bit stream in [BadgerDB](../components/badgerdb-working-memory.md).
- **Decompress:** run the *same* deterministic model to reconstruct the exact text.
- Coexists with the summary buffer: the summary serves to *inject* cheap context; the codec serves to
  *retain* the full history without storage exploding.

## Success criterion (in bits)

- **Ratio:** compressed history bits/char < 1.0 (ts_zip target), compared against UTF-8 (8) and
  gzip (~2–3) as baselines.
- **Losslessness:** 100% exact reconstruction (verified byte by byte) — this is the advantage over the
  summary buffer, which is lossy.
- **Cost:** compression/decompression latency acceptable for batch; NOT used in hot-path TTFT.

## Architectural fit

- Storage in [BadgerDB](../components/badgerdb-working-memory.md); compression in the batch worker.
- **Hard constraint — determinism:** decompressing requires the exact model (same version, same
  kernels). This **forces** model versioning and reproducible builds → aligns with
  [Bellard discipline](../vision/compression-thesis.md). A model change forces recompression or
  keeping the old model as a "legacy codec."
- Natural candidate for the [sovereign NN core (Bet 5)](bet-05-sovereign-nn-core.md).

## Risk and maturity

🟡 Medium. The technique is proven, but: (a) model↔codec determinism is a real operational
trade-off; (b) decompression compute isn't free. Apply first where lossless retention value
outweighs the cost (facts/decisions), not to trivial chit-chat (which gets discarded by [Bet 1](bet-01-surprisal-gated-memory.md) anyway).

## Dependencies

A local model with accessible log-probs + an arithmetic coder. [Bet 5](bet-05-sovereign-nn-core.md)
makes it in-process; without it, a local model via external runtime.

## Next experiment

Compress a real history with a small model + arithmetic coder and measure bits/char vs gzip,
verifying 100% lossless reconstruction. If it beats gzip with margin, the bet holds.

## References

- ts_zip: Text Compression using Large Language Models — <https://bellard.org/ts_zip/>
- nncp — <https://bellard.org/nncp/>
- [Manifesto: the compression thesis](../vision/compression-thesis.md)
