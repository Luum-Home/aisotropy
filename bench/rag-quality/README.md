# `bench/rag-quality` — measured retrieval quality for the semantic tier

This harness answers a question the eval **foundation** (ADR-0026,
`internal/eval`) explicitly left open: the metrics and the harness seam shipped,
but with **no gold dataset and no numbers** — retrieval *quality* was
**unmeasured**. The footprint / isolation / latency benchmarks measure how *fast*
and how *isolated* retrieval is, never whether it returns the *right* things.

`bench/rag-quality` supplies a real, licensed, **multilingual** gold set and
produces measured **recall@{1,5,10}, MRR@10, and nDCG@10** on aisotropy's REAL
`semantic.Store` — English, Spanish, and both cross-lingual directions.

## What it does

1. Loads a self-contained gold set (`testdata/xquad_slice.json`) — a
   QA→retrieval reduction of **XQuAD** (`google/xquad`, CC-BY-SA-4.0; see
   `LICENSE-DATA.md`), a *parallel* EN/ES benchmark.
2. Embeds each language's paragraph corpus with the configured embedder using
   the e5 **`passage:`** role (ADR-0054) and upserts it into an in-process
   `semantic.InMemoryStore`, tenant/user-scoped exactly like production.
3. Embeds each query with the e5 **`query:`** role and runs `Store.Search`.
4. Scores the retrieved ids against the labeled qrels with `internal/eval`'s IR
   metrics and writes `results.txt`.
5. Optionally runs an LLM-as-a-judge pass over the top-retrieved passage, gated
   the **provider-agnostic** way via `eval.JudgeClientFromEnv`
   (`OPENROUTER_API_KEY` > `OPENAI_API_KEY` > `ANTHROPIC_API_KEY` > stub,
   ADR-0009); **skips cleanly** when no provider key is set.

## The four slices

| Slice | Query → Corpus | What it proves |
|---|---|---|
| `en_monolingual` | EN → EN | baseline English retrieval |
| `es_monolingual` | ES → ES | baseline Spanish retrieval |
| `cross_en_to_es` | EN → ES | **cross-lingual**: find the parallel Spanish paragraph |
| `cross_es_to_en` | ES → EN | **cross-lingual**: find the parallel English paragraph |

The two `cross_*` slices are the multilingual proof: an English query must find
the Spanish paragraph that answers it with **no lexical overlap** to lean on.
Only an embedder that projects both languages into one shared space scores above
the floor there — a deterministic hash embedder does not.

## Running it

**Real multilingual-e5-small** (sovereign local embedder, ADR-0023; warm daemon):

```bash
AISOTROPY_EMBED_BIN=crates/embed/target/release/embed \
AISOTROPY_EMBED_MODEL_DIR=<dir with config.json + model.safetensors + tokenizer.json> \
AISOTROPY_EMBED_DIM=384 \
AISOTROPY_EMBED_MODE=daemon \
AISOTROPY_EMBED_E5_PREFIXES=1 \
go run ./bench/rag-quality
```

**Deterministic harness validation** (no model, no network, no key — proves the
pipeline end-to-end but the numbers are lexical-overlap artifacts, **not**
semantic quality):

```bash
go run ./bench/rag-quality
```

Flags: `-data <path>` (gold set), `-out <path>` (report), `-judge-samples N`,
`-quiet` (suppress embedder logs).

## The honesty contract

The report **labels every run**. On the deterministic `HashEmbedder` it prints a
`HARNESS VALIDATION — NOT SEMANTIC QUALITY` banner: the numbers prove the harness
and metrics compute correctly, nothing about the e5 model. The harness detects
the active backend at runtime and probes whether the e5 `query:`/`passage:`
asymmetry is actually engaged (it compares a query-role vector against a
passage-role vector for the same text), so a mislabeled run cannot slip through.

## The honest gap (generic benchmark ≠ aisotropy's real RAG)

XQuAD is a clean Wikipedia QA benchmark: single-relevant, professionally
translated paragraphs. aisotropy's real workload is multi-tenant **conversational
memory** — noisy turns, distilled facts, per-user isolation, several relevant
chunks per query. These numbers measure the **embedder + store** on a generic
task; they do **not** yet measure retrieval over aisotropy's own conversational
corpus. A **domain gold set** (labeled tenant memories) is the next step. What
this delivers today: retrieval moves from **unmeasured** to **measured** on a
real, licensed, multilingual benchmark — with a reproducible command.

## Files

| Path | Role |
|---|---|
| `main.go` | orchestration: load → embed → upsert → search → score → write |
| `dataset.go` | gold-set loader (`corpora` + `slices` + qrels) |
| `report.go` | report rendering (honesty banner, per-slice table, macro-avg) |
| `judge.go` | optional, key-gated LLM-as-a-judge pass |
| `scripts/fetch_xquad.py` | one-shot data-prep: builds the slice from XQuAD |
| `testdata/xquad_slice.json` | the vendored gold set (self-contained) |
| `results.txt` | latest run output |
| `LICENSE-DATA.md` | dataset license + attribution |
