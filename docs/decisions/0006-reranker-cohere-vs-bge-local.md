---
type: ADR
title: "ADR-0006 — Reranker: SaaS vs local"
description: Which reranker reorders Qdrant candidates before the LLM context — a local in-process cross-encoder (BGE-reranker-v2-m3, Apache-2.0) is recommended over Cohere Rerank SaaS, because in 2026 the quality gap closed and the network hop kills TTFT.
tags: [decisions, adr, reranker, rag, sovereignty]
timestamp: 2026-07-08T00:00:00Z
---

# ADR-0006 — Reranker: SaaS vs local

## Status

**✅ Accepted (ratified by the user, 2026-07-08).** Aisotropy's reranker is a
**local, in-process cross-encoder**: **`bge-reranker-v2-m3`** (BAAI, Apache-2.0) as the default,
with **`Qwen3-Reranker-0.6B`** (Apache-2.0) as a drop-in quality upgrade. **Cohere Rerank is
discarded.**

## Context

[Semantic memory](../architecture/memory-tiers.md) (layer 3, [Qdrant](../components/qdrant-semantic-memory.md))
returns the ~10-20 nearest fragments by vector similarity (HNSW + filtering by
`tenant_id`/`user_id`). But *close in embedding space ≠ relevant to this question*: the
bi-encoder compresses query and document separately, without seeing them together. The
**reranker** is a subsequent step that does cross query × passage (cross-encoder) and
**reorders** those candidates to keep the **top 3-4** before assembling the LLM's context.

This step is what makes real the anti-magic principle from the [thesis](../vision/thesis.md#the-problem):
"magic" frameworks *"inject raw RAG without reranking"* and bloat the prompt. The reranker is
the direct lever for **token efficiency**: going from 15 fragments down to 4 trims thousands of
input tokens per request, along with its cost and latency.

Two facts set the weight of this decision:

1. **It's on the HOT PATH.** Reranking runs **between** the vector search and the LLM's first
   token. Any latency added hits **time-to-first-token (TTFT)** directly — the metric
   Aisotropy states it optimizes for interactive chat (SSE, `ctx` cancellation, see
   [thesis: "What it is"](../vision/thesis.md#what-it-is-and-isnt)).
2. **It's a model of the "sovereign NN core."** The reranker is **explicitly named** among the
   "cognitive plumbing" that [Bet 5](../research/bet-05-sovereign-nn-core.md) wants to
   internalize: a small, quantized, in-process model that runs in µs–ms without going out to
   the network. Choosing a SaaS here would directly contradict that bet.

Hard requirements (inherited from the rest of the stack):

- **Permissive license** — MIT/Apache-2.0 only. Zero proprietary, zero CC-BY-**NC**
  (non-commercial), zero gray areas. Same rule that ruled out Turso/GlareDB in
  [ADR-0004](0004-facts-memory-rqlite-vs-litefs.md).
- **In-process / local-first** — runs on your hardware, embedded or as a lightweight sidecar,
  with no network trip per request. Consistent with the few-MB Distroless binary from the
  [overview](../architecture/overview.md).
- **Quantizable** — int8/int4 to fit in a few MB of RAM and lower latency, as Bet 5 requires.
- **Multilingual** — the reranker can't degrade in Spanish or other tenant languages.

## Goals / Non-goals

**Goals**

- Choose **which reranker** reorders Qdrant's candidates on the hot path.
- Prioritize **sovereignty + in-process + permissive license** given its role as Bet 5's
  substrate.
- Keep TTFT low: reranker latency must be local (no network hop).
- Be **honest** about whether there's a real quality gap vs. the best SaaS (Cohere) in 2026.

**Non-goals**

- **Not** deciding Qdrant's **embedding** model (the layer-3 bi-encoder) — that's another piece
  of Bet 5; this ADR is only the **reranking** step.
- **Not** deciding the inference runtime (ONNX Runtime / Candle / llama.cpp / TEI): that's
  tactical and gets resolved when internalizing Bet 5. Here the **model** and the **topology**
  (local vs. SaaS) are chosen.
- **Not** redesigning the `Relevance × Importance × Recency` scoring of the
  [facts layer](../architecture/memory-tiers.md#2-facts--episodic-memory-engram-style):
  that's keyword/FTS5, not vectors, and doesn't carry a neural reranker.

## Options considered

### Option A — Local BGE cross-encoder (`bge-reranker-v2-m3`, BAAI)

Cross-encoder derived from `bge-m3`. **~568M parameters** (0.6B), **Apache-2.0**, strongly
**multilingual** (>100 languages inherited from m3), explicitly designed as *"lightweight, easy
to deploy, fast inference."* Takes query–passage pairs and returns a 0-1 score (sigmoid).

- **Sovereignty:** total. Runs local/in-process, zero network. Quantizable (int8) → fits in a
  few MB.
- **License:** **Apache-2.0** — clean.
- **Quality (2026):** the **open baseline of reference** for production RAG; solid on
  MIRACL/MTEB/BEIR and multilingual.
- **Bet 5 fit:** it's *literally* one of the models Bet 5 names for internalization. Adopting
  it now is the first step of that bet (start with a "borrowed" model, measure, then
  internalize the runtime).

### Option B — Cohere Rerank SaaS (`rerank-v3.5` / Rerank 4 family)

SaaS service via API. Strongly multilingual (100+ languages), Pro (quality) and Fast (latency)
variants.

- **Sovereignty:** **zero.** Every rerank is an HTTP call to a third party → your retrieved
  fragments (tenant data) leave your infra. Breaks the thesis's sovereignty pillar.
- **License:** **proprietary, closed-source.** Violates the hard MIT/Apache-2.0 requirement
  outright.
- **Latency:** **~80-150 ms p50** and **200 ms+ p99** *purely from the network hop and remote
  compute* — **added directly to TTFT** on the hot path. A small local cross-encoder reranks
  15 candidates in the order of **ms, with no network**.
- **Cost:** **~$2 per 1,000 queries** (~$0.001/search); recurring, per request, forever.
- **Quality (2026):** very good and convenient (managed), but **no longer decisively superior**
  to the best open option (see table). Its real advantage today is operational (zero
  maintenance), not ranking quality.

### Option C — Qwen3-Reranker (Alibaba, 0.6B / 4B / 8B, Apache-2.0)

Qwen3 reranker family, **Apache-2.0**, multilingual (>100 languages), 32k context.

- **`Qwen3-Reranker-0.6B`:** **BEIR ~59.28**, and **surpasses `bge-reranker-v2-m3`** while
  keeping a hot-path-sized footprint (0.6B). Strong instruction-following.
- **`Qwen3-Reranker-4B`:** **BEIR ~62.29** (top-tier open), but 4B is heavy for the quantized
  in-process hot path.
- **Fit:** the natural **drop-in upgrade**. Same sovereignty profile as BGE (local,
  Apache-2.0), better quality at the small size. Ideal candidate for an A/B against BGE.

### Option D — Other open models (mxbai-rerank-v2, jina-reranker, ColBERT)

- **`mxbai-rerank-v2`** (Mixedbread): base 0.5B / large 1.5B, **Apache-2.0**, local.
  `large-v2` ~**BEIR 61.44**. A valid, permissive alternative; kept as an A/B candidate.
- **`jina-reranker-v3`:** the **best open BEIR (~61.94)** with fewer params, BUT Jina's weights
  are published under **CC-BY-NC-4.0 (non-commercial)**; commercial use requires their
  API/Sagemaker/Azure. **Discarded for license** — the same rule as Turso/BSL in
  [ADR-0004](0004-facts-memory-rqlite-vs-litefs.md).
- **ColBERTv2 / answerai-colbert:** **MIT**, but it's *late interaction* multi-vector (a
  different architecture: reorders via token-to-token interaction). Excellent for long-document
  corpora, not a drop-in cross-encoder for reordering 15 candidates. Noted as a future line, not
  as the default reranker.

## Trade-off comparison table

| Criterion | A — Local BGE `v2-m3` | B — Cohere SaaS | C — Qwen3-Reranker 0.6B | D — mxbai / jina / ColBERT |
|---|---|---|---|---|
| **Quality (approx. BEIR 2026)** | Solid open baseline | Very good, not decisive | ~59.28 (> BGE) | mxbai-large ~61.44 · jina-v3 ~61.94 · ColBERT (late interaction) |
| **Latency (hot path / TTFT)** | ms, **local, no network** | **+80-150 ms p50 / 200+ p99** (network hop) | ms, local | ms local (mxbai/ColBERT); jina requires its API if commercial |
| **Cost** | $0 marginal (your hardware) | **~$2 / 1,000 queries**, recurring | $0 marginal | $0 (mxbai/ColBERT) / paid (jina API) |
| **Sovereignty** | **Total** (in-process) | **None** (data leaves to third parties) | **Total** | Total (mxbai/ColBERT) · none if jina-API |
| **License** | **Apache-2.0** ✅ | Proprietary ❌ | **Apache-2.0** ✅ | mxbai Apache-2.0 ✅ · **jina CC-BY-NC ❌** · ColBERT MIT ✅ |
| **In-process / quantizable** | **Yes** (int8, few MB) | No (remote API) | **Yes** | Yes (mxbai/ColBERT) · No (jina-API) |
| **Bet 5 fit** | **Direct** (named model) | Contradicts Bet 5 | **Direct** | Partial (mxbai) / different architecture (ColBERT) |

## Recommended decision

**A local, in-process cross-encoder is recommended: `bge-reranker-v2-m3` (Apache-2.0) as
default, with `Qwen3-Reranker-0.6B` (Apache-2.0) as a drop-in upgrade.** Cohere Rerank **is
discarded.**

Three factors, in order of weight, all point the same way:

**(a) The hot path rules — the network hop is unacceptable.** The reranker runs right before
the first token. Cohere adds **80-150 ms p50 (200+ p99) of pure network travel** to TTFT, the
metric the project states it optimizes. A small cross-encoder reorders 15 candidates
**locally in ms, with no network.** There's no way to hide a round-trip to a third party in the
system's most latency-sensitive path.

**(b) Sovereignty + license — a hard requirement, not a preference.** Cohere is a proprietary
SaaS: it sends **tenant data** (the retrieved fragments) to a third party, and its license is
closed-source. It violates *two* pillars of the [thesis](../vision/thesis.md#sovereignty) and the
MIT/Apache-2.0 filter that already ruled out Turso ([ADR-0004](0004-facts-memory-rqlite-vs-litefs.md))
— the same way it rules out **jina-reranker** (CC-BY-NC, non-commercial). BGE and Qwen3 are
Apache-2.0, local, quantizable: they pass the filter without friction.

**(c) It's the first step of Bet 5.** The reranker is **explicitly named** in
[Bet 5 — sovereign NN core](../research/bet-05-sovereign-nn-core.md) as plumbing to
internalize. Adopting a local cross-encoder now *is* executing that bet's incremental strategy:
run a small "borrowed" model (external runtime like ONNX/Candle), measure latency/footprint/
quality, and internalize the runtime later. Choosing Cohere would dig debt against the very
roadmap.

**Honesty about the quality gap (2026):** this is the key finding of the research. **The gap
closed.** In prior years Cohere Rerank had a defensible quality edge; in 2026 the best local
open option (Qwen3-Reranker-4B ~62.29 BEIR, jina-v3 ~61.94, mxbai-large ~61.44) is on par with
or above it in public benchmarks, and **Qwen3-0.6B already surpasses bge-reranker-v2-m3** at
the small size. Cohere's remaining edge is **operational** (managed, zero maintenance, language
breadth), **not decisive ranking quality**. And every expert agrees on a caveat that applies
fully here: public BEIR numbers **don't guarantee lift on your corpus** — you need to build a
*golden set* of 100-500 tenant queries and measure the actual uplift. That A/B is local and
cheap with open models; impossible to iterate on freely while paying per query to a SaaS.

Start with **BGE-v2-m3** (the most proven, lightweight baseline) and keep **Qwen3-0.6B**
ready as a swap if the golden set shows its extra quality justifies the size.

## Consequences

**Positive**

- **TTFT protected:** reranking stays at local ms; zero round-trip on the hot path.
- **Sovereignty intact:** tenant data retrieved never leaves the infra. Consistent end to end
  with the [thesis](../vision/thesis.md#sovereignty).
- **Zero marginal cost:** no per-query bill; the expense is hardware amortization.
- **Clean license (Apache-2.0):** zero legal friction, compiles/embeds alongside the stack.
- **Aligned with Bet 5:** the reranker becomes the first model of the sovereign NN core; the
  BGE↔Qwen3↔mxbai A/B over a tenant golden set becomes trivial and reproducible.

**Negative / technical debt**

- **You own the inference:** you have to run and operate the model (runtime, quantization,
  warmup, query–passage pair batching). Cohere saved you that. Mitigation: it's exactly the
  muscle Bet 5 wants to build; start with an external runtime (ONNX/Candle/TEI) and internalize
  later.
- **Local compute latency isn't free:** a cross-encoder evaluates query × each candidate. With
  15-20 passages you need to **quantize (int8/int4)**, batch, and maybe cap the input top-K to
  stay within the ms budget. Measure on the target hardware.
- **Quality ceiling depends on your operation:** the benchmark number doesn't materialize by
  itself; it requires a golden set and tuning. It's work the SaaS "solved" (at the cost of
  sovereignty and $).
- **VRAM/RAM footprint:** the model lives in the process; its footprint must be budgeted
  alongside Qdrant and the rest of the binary (see [overview](../architecture/overview.md)).

## Open questions

1. **BGE vs Qwen3-0.6B vs mxbai on *our* corpus** — build the golden set (100-500 queries per
   tenant/language) and measure real answer-quality uplift, not just public BEIR. Which wins
   the A/B?
2. **Inference runtime** — ONNX Runtime, Candle (Rust, consistent with Qdrant/DataFusion),
   llama.cpp, or TEI? Embedded in-process or a sidecar? Tactical decision when internalizing
   Bet 5.
3. **Quantization level** — int8 vs int4: how much quality is lost and how much latency/
   footprint is gained on the target hardware? Measure, don't assume.
4. **Input top-K** — do we rerank all 20 from Qdrant or trim beforehand (e.g., top-10) to
   bound the cross-encoder's compute on the hot path? Recall vs. latency trade-off.
5. **Hot-path latency budget** — set the total TTFT budget and how many ms go to the reranker,
   to choose model/quantization/top-K with a number, not vibes (Bet 5 criterion).
6. **Multilingual** — validate that the chosen reranker doesn't degrade on Spanish or other
   tenant languages against the golden set.
7. **Fallback** — if a local model fails to load, does it degrade to "no rerank" (use Qdrant's
   order) or abort? Define the degradation semantics.

## References

- BGE-reranker-v2-m3 — model card (Apache-2.0, ~568M, multilingual, fast inference):
  <https://huggingface.co/BAAI/bge-reranker-v2-m3>
- FlagEmbedding / BGE — retrieval and reranking toolkit:
  <https://github.com/FlagOpen/FlagEmbedding> · <https://bge-model.com/tutorial/5_Reranking/5.2.html>
- Qwen3-Reranker-0.6B — model card (Apache-2.0, >100 languages, 32k ctx, BEIR ~59.28, > bge-v2-m3):
  <https://huggingface.co/Qwen/Qwen3-Reranker-0.6B>
- Qwen3 Embedding & Reranking — technical blog (BEIR benchmarks 0.6B/4B/8B):
  <https://qwenlm.github.io/blog/qwen3-embedding/>
- mxbai-rerank-v2 (Mixedbread) — Apache-2.0, base 0.5B / large 1.5B (BEIR ~61.44):
  <https://www.mixedbread.com/>
- jina-reranker-v3 — best open BEIR (~61.94) but **CC-BY-NC-4.0** weights (non-commercial → discarded):
  <https://jina.ai/models/jina-reranker-v3/> · <https://huggingface.co/jinaai/jina-reranker-v2-base-multilingual>
- Cohere Rerank — proprietary SaaS product (v3.5 / Rerank 4), pricing and specs:
  <https://cohere.com/rerank> · <https://docs.cohere.com/docs/how-does-cohere-pricing-work> ·
  <https://cloudprice.net/models/cohere-rerank-3-5>
- Evaluating Cohere Rerank in RAG (2026) — p50/p99 latency and caveats:
  <https://futureagi.com/blog/evaluating-cohere-rerank-rag-2026/>
- Best Rerankers for RAG in 2026 (license/latency/language comparison; golden set > public BEIR):
  <https://futureagi.com/blog/best-rerankers-for-rag-2026/>
- Self-Host Embeddings and Rerankers: TEI on GPU (self-hosting runtime):
  <https://www.spheron.network/blog/self-host-embedding-reranker-tei-gpu-cloud/>

---

Cross-links: [Bet 5 — sovereign NN core](../research/bet-05-sovereign-nn-core.md) ·
[tiered memory](../architecture/memory-tiers.md) ·
[Qdrant — semantic memory](../components/qdrant-semantic-memory.md) ·
[thesis: sovereignty](../vision/thesis.md#sovereignty) ·
[ADR-0004 — facts memory: rqlite vs LiteFS](0004-facts-memory-rqlite-vs-litefs.md)
