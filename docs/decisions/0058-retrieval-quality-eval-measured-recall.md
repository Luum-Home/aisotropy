---
type: ADR
title: "ADR-0058 — Retrieval quality, measured: recall/MRR/nDCG on the semantic tier with the multilingual e5 embedder"
description: ADR-0026 shipped the evaluation FOUNDATION (internal/eval — IR metrics + LLM-judge plumbing) but explicitly with "no gold dataset, no CI gate" — so retrieval QUALITY stayed unmeasured while footprint/isolation/latency were measured. Decision — ship bench/rag-quality, a runnable harness that embeds a licensed, PARALLEL multilingual gold set (XQuAD EN/ES, CC-BY-SA-4.0) with the real e5 embedder using the ADR-0054 query:/passage: prefixes, upserts it into the REAL tenant-scoped semantic.Store, and reports measured recall@{1,5,10}, MRR@10, nDCG@10 — English, Spanish, and BOTH cross-lingual directions. Extend internal/eval with nDCG@k and role-aware query embedding. Real e5 macro-avg R@1=0.92 / nDCG@10=0.97, cross-lingual R@1=0.89 (vs a 0.03 hash floor) — the multilingual embedder demonstrably works. Honest gap — a generic public benchmark is not aisotropy's multi-tenant conversational-memory RAG; a domain gold set is the next step.
tags: [decisions, adr, evaluation, quality, retrieval, recall, ndcg, mrr, rag, multilingual, e5, cross-lingual, measured]
timestamp: 2026-07-17T00:00:00Z
supersedes: none — completes the "no gold dataset / no numbers" follow-up ADR-0026 left open
---

# ADR-0058 — Retrieval quality, measured: recall/MRR/nDCG on the semantic tier with the multilingual e5 embedder

## Status

✅ **Accepted — implemented 2026-07-17.** `bench/rag-quality` is a runnable harness
(`go run ./bench/rag-quality`) plus a licensed gold set and a written results file. It moves
retrieval quality from **unmeasured** to **measured** on a real, permissively-licensed,
multilingual benchmark. It is a measurement, not a CI gate (see Consequences).

## Context

ADR-0026 built the evaluation **foundation** — `internal/eval`: information-retrieval metrics
(precision/recall/F1/MRR) run against the REAL `semantic.Store` within a tenant `Scope`, plus an
LLM-as-a-judge that emits a typed verdict through the same reflection loop the product serves with.
It shipped deliberately as a *foundation*, and named its own gap in plain text:

> "a foundation (metrics + judge plumbing + rubric model), **not** a full eval platform … no gold
> dataset, no CI gate."

The consequence: the retriever's *plumbing* was tested (a HashEmbedder over a 6-doc toy corpus), but
its *quality* — does search return the right passages, and does the multilingual embedder (ADR-0049)
actually retrieve **across languages**? — had **no numbers**. Meanwhile the framework measured its
other pillars precisely: compression as bits-per-token (ADR-0014/0015), isolation by construction +
an adversarial cross-tenant suite (ADR-0011/0024), footprint/streaming/latency (the `bench/*`
suites). Retrieval quality was the unmeasured pillar.

Two capabilities landed after ADR-0026 that make a real measurement possible and worth doing now:

- **ADR-0049** swapped the sovereign local embedder default to `intfloat/multilingual-e5-small` — a
  genuinely multilingual model. That raises an empirical question the codebase could not answer:
  *does cross-lingual retrieval actually work?*
- **ADR-0054** wired the e5 `query:`/`passage:` instruction-prefix asymmetry through the Go seam
  (`EmbedQuery`/`EmbedPassage`). Correct retrieval depends on using them; an eval that ignores them
  would measure a crippled retriever.

## Decision

Ship **`bench/rag-quality`** and extend `internal/eval`, so retrieval quality is measured on aisotropy's
real store with the real embedder.

### 1. Adopt a licensed, parallel, multilingual gold set — XQuAD

`testdata/xquad_slice.json` is a QA→retrieval reduction of **XQuAD** (`google/xquad`, v1.1,
Artetxe et al. 2020). License: **CC-BY-SA-4.0** (verified via the HF dataset card; recorded in
`bench/rag-quality/LICENSE-DATA.md`). CC-BY-SA is a permissive *content* license — it is **not** one
of the copyleft/source-available *software* licenses the constitutional gate blocks (AGPL/SSPL/BSL,
`license-policy.md`); this derived slice is redistributed under the same terms with attribution.

XQuAD was chosen over larger benchmarks (MIRACL, mMARCO) for one property that makes the multilingual
claim *provable*: it is **parallel** — the same paragraphs and questions professionally translated
into EN, ES, and 10 other languages, aligned by a shared question id. That yields four retrieval
slices from one fetch:

| slice | query → corpus | proves |
|---|---|---|
| `en_monolingual` | EN → EN | baseline English retrieval |
| `es_monolingual` | ES → ES | baseline Spanish retrieval |
| `cross_en_to_es` | EN → ES | **cross-lingual** — EN query finds the parallel ES paragraph |
| `cross_es_to_en` | ES → EN | **cross-lingual** — ES query finds the parallel EN paragraph |

The gold "relevant passage" for a question is the paragraph it was written about (the standard
QA→IR construction). 154-doc corpus per language, 150 queries per slice. The harness is
**dataset-agnostic** (reads any `corpora` + `slices` + qrels JSON), so a CC0/CC-BY source
(Tatoeba, MQA) can replace XQuAD with no code change.

### 2. Extend `internal/eval` (the shared metric layer)

- **`NDCGAtK`** — binary-relevance nDCG@k (position-aware ranking quality), the metric ADR-0026's
  `metrics.go` was missing. Added to `RetrievalResult`/`RetrievalReport` as `NDCG`/`MeanNDCG`
  (additive fields; existing callers/tests unaffected).
- **Role-aware query embedding** — `EvalRetrieval` now embeds queries via `semantic.EmbedQuery`
  (the ADR-0054 `query:` role) instead of a bare `Embed`. On a non-e5/Hash embedder this degrades to
  a plain `Embed` (byte-identical), so the deterministic tests are unchanged; on a real e5 embedder
  it applies the `query:` prefix so search matches the `passage:`-prefixed corpus — measuring the
  retriever the way production's `chat_stream` actually queries it.

### 3. Ship the runnable harness `bench/rag-quality`

`go run ./bench/rag-quality` loads the gold set, embeds each corpus with the `passage:` role, upserts
into a real tenant/user-scoped `semantic.InMemoryStore`, embeds each query with the `query:` role,
searches, scores with `internal/eval`, and writes `results.txt`. It **labels every run** — the
deterministic HashEmbedder prints a `HARNESS VALIDATION — NOT SEMANTIC QUALITY` banner and the
harness probes at runtime whether the e5 role asymmetry is actually engaged (comparing a query-role
vector against a passage-role vector), so a mislabeled run cannot slip through. An **optional**
LLM-as-a-judge pass (retrieved-context relevance, via the ADR-0026 `LLMJudge`) is gated strictly on
`ANTHROPIC_API_KEY` and skips cleanly without one.

## The numbers (measured 2026-07-17)

Dataset: XQuAD EN/ES validation slice — 154-doc corpora, 150 queries/slice.

**Real `intfloat/multilingual-e5-small` (384-dim), with e5 query:/passage: prefixes:**

| slice | R@1 | R@5 | R@10 | MRR@10 | nDCG@10 |
|---|---|---|---|---|---|
| en_monolingual | 0.947 | 1.000 | 1.000 | 0.972 | 0.979 |
| es_monolingual | 0.960 | 0.993 | 1.000 | 0.978 | 0.983 |
| cross_en_to_es | 0.887 | 1.000 | 1.000 | 0.941 | 0.956 |
| cross_es_to_en | 0.887 | 1.000 | 1.000 | 0.941 | 0.956 |
| **MACRO-AVG** | **0.920** | **0.998** | **1.000** | **0.958** | **0.969** |

**Deterministic HashEmbedder (harness-validation baseline — proves the pipeline, NOT semantic quality):**

| slice | R@1 | R@5 | R@10 | MRR@10 | nDCG@10 |
|---|---|---|---|---|---|
| en_monolingual | 0.300 | 0.493 | 0.587 | 0.371 | 0.421 |
| es_monolingual | 0.260 | 0.447 | 0.540 | 0.342 | 0.389 |
| cross_en_to_es | 0.033 | 0.133 | 0.187 | 0.072 | 0.099 |
| cross_es_to_en | 0.033 | 0.127 | 0.200 | 0.077 | 0.106 |
| **MACRO-AVG** | **0.157** | **0.300** | **0.378** | **0.215** | **0.254** |

**The reading.** The real e5 embedder retrieves **cross-lingually** (R@1 ≈ 0.89) almost as well as
**monolingually** (R@1 ≈ 0.95) — an EN query finds the parallel ES paragraph with no lexical overlap
to lean on. The hash embedder collapses cross-lingually (R@1 = 0.033, the floor), because feature
hashing shares no lexicon across languages. **That gap — 0.89 vs 0.03 — is the multilingual embedder
demonstrably working**, and it is exactly what an eval that ignored the semantic layer (or the
query:/passage: prefixes) would fail to show.

### Measurement caveat (honest, load-bearing)

The **real-e5 numbers were produced by `scripts/e5_batch_probe.py`, not the Go harness**, on this
CPU-only box. The Go harness embeds one query per call — faithful to the product path, but ~600
sequential CPU inferences at ~1.5–2 s each do not finish in a usable window here. The probe measures
the **identical** quantity — same `crates/embed` binary, same pinned e5 weights, same
`query:`/`passage:` prefixes, same recall/MRR/nDCG math as `internal/eval` — but **batches** all 603
embeddings into one call (~45 s total). Two differences from the Go path, both documented: (a)
batched vs per-query (no effect on the resulting vectors/ranking); (b) passages truncated to 900
chars because e5's 512-token cap is hit by 17 long XQuAD paragraphs and the subprocess embed path
does not auto-truncate (standard RAG chunking; the answer sits early in each paragraph). The Go
harness itself is proven correct on the deterministic embedder (identical hash numbers whether run
per-cutoff or derived from one top-10 list), and produces the committed `results.txt` product-path
run. A faster embed path (GPU, or batching the query side inside `EvalRetrieval`) would let the Go
harness produce these numbers directly; that is a follow-up, not a blocker for the measurement.

## The honest gap (generic benchmark ≠ aisotropy's real RAG)

XQuAD is a clean Wikipedia QA benchmark: single-relevant, professionally translated paragraphs.
aisotropy's real workload is **multi-tenant conversational memory** — noisy turns, distilled facts,
per-user isolation, several relevant chunks per query, `KindDocument`/`KindFact`/`KindTurn` mixed in
one scope. These numbers measure the **embedder + store** on a generic task; they do **not** yet
measure retrieval over aisotropy's own conversational corpus. A **domain gold set** (labeled tenant
memories, multi-relevant qrels, the fact/turn kinds) is the further step this ADR does not take.
What it delivers today: retrieval moved from **unmeasured** to **measured**, with a reproducible
command and a real, licensed, multilingual dataset — and a first empirical confirmation that the
ADR-0049 multilingual bet actually retrieves cross-lingually.

## Consequences

**Positive.**
- Retrieval quality is now a number, not an assumption — regressions in the embedder, the store, or
  the query:/passage: wiring show up here first.
- The multilingual claim (ADR-0049) is empirically confirmed: real cross-lingual R@1 ≈ 0.89.
- `internal/eval` gained nDCG@k and role-correct query embedding, improving the shared harness for
  any future eval.
- Isolation holds during evaluation — every read is tenant/user-scoped through a real `Scope`.

**Negative / deferred.**
- **Not a CI gate.** A threshold gate (e.g. macro nDCG@10 ≥ 0.90 on real e5) that the operator wires
  into the pre-push hook is designed-for but not enabled — it needs a fast embed path in CI. Until
  then this is a manual, reproducible measurement.
- **The real-e5 path is CPU-slow** on a keyless box; the batched probe is the practical runner today.
- **Generic, not domain.** See the gap above.

## Alternatives considered

- **MIRACL** (Apache-2.0, the canonical multilingual IR benchmark) — rejected for now: its HF viewer
  runs arbitrary code and its qrels require joining a multi-GB corpus, neither self-contained nor
  in-process-friendly for a small slice.
- **Tatoeba (CC-BY-2.0) / MQA (CC0)** — cleaner (non-ShareAlike) licenses, kept as documented drop-in
  alternatives; rejected as the primary because Tatoeba is sentence-level bitext (not passage
  retrieval) and MQA is not parallel across languages, so neither gives the clean monolingual +
  cross-lingual quartet XQuAD's parallel structure does.
- **Wait for a domain gold set** — rejected: measuring the embedder+store on a generic benchmark now
  is strictly better than measuring nothing, and it is the honest first rung.

## References

- ADR-0026 — the evaluation foundation this completes (`internal/eval`).
- ADR-0049 — the multilingual embedder whose cross-lingual claim this confirms.
- ADR-0054 — the e5 query:/passage: prefixes this eval depends on.
- `bench/rag-quality/README.md`, `LICENSE-DATA.md`, `results.txt`.
