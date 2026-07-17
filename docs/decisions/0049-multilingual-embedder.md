---
type: ADR
title: "ADR-0049 — Multilingual default for the sovereign local embedder (cross-lingual RAG)"
description: The sovereign local embedder (ADR-0023) shipped BAAI/bge-small-en-v1.5 — English-only — so cross-lingual semantic RAG did not work on the default. Decision — swap the pinned default to intfloat/multilingual-e5-small (MIT, 384-dim, 100+ languages). Because both models are 384-dim the swap is dimension-preserving: existing Qdrant collections stay compatible, no vector-width re-index. The crate now also supports the optional e5 query:/passage: instruction prefix per request. Grounded in docs/research/i18n-multilingual-readiness-2026-07-17.md (R2).
tags: [decisions, adr, bet-05, embeddings, semantic, multilingual, i18n, candle, cross-lingual-rag]
timestamp: 2026-07-17T00:00:00Z
supersedes: partial — the model-default clause of ADR-0023 (the boundary/seam/interface stand)
---

# ADR-0049 — Multilingual default for the sovereign local embedder (cross-lingual RAG)

## Status

✅ **Accepted — implemented 2026-07-17.** Amends the *model-default* clause of
[ADR-0023](0023-sovereign-local-embedder-semantic-tier.md); everything else in ADR-0023 (the ADR-0005
subprocess/daemon boundary, the `semantic.LocalEmbedder`/`DaemonEmbedder` Go seam, the unchanged
`Embedder` interface, env-gated + dormant-by-default posture) stands unchanged.

## Context

The i18n readiness audit ([`docs/research/i18n-multilingual-readiness-2026-07-17.md`](../research/i18n-multilingual-readiness-2026-07-17.md),
read-only code-truth, R2) found that aisotropy's shipped **sovereign default embedder is English-only**:

> `crates/embed/Cargo.toml` — pinned default **`BAAI/bge-small-en-v1.5`** (English-only). …
> Cross-lingual retrieval does **not** work on the shipped default.

Concretely, the storage/transport tiers (BadgerDB, Qdrant, the fence, ctx propagation) are Unicode-clean
and carry any language losslessly, but the vectorizer that has to *understand* text was pinned to an
English-only model. A Spanish query against a Spanish (or mixed-language) corpus embeds into a space the
English model was never trained to place multilingually, so semantic RAG silently degrades off-English.

ADR-0023 itself already named the fix and pre-verified its license: *"`intfloat/multilingual-e5-small`
(MIT, multilingual) is the recommended multilingual pin"*, and flagged the e5 `query:`/`passage:` prefix
as deferred debt (#4). This ADR promotes that recommendation to the shipped default and closes the prefix
debt at the crate level.

## Decision

**Change the pinned default model from `BAAI/bge-small-en-v1.5` to `intfloat/multilingual-e5-small`** in
`crates/embed` (the `model_id()` default, the doc/description strings, the `AISOTROPY_EMBED_MODEL_DIR`
resolve hint). The id/rev stay env-overridable (`AISOTROPY_EMBED_MODEL_ID`/`_REV`); nothing is hardcoded
beyond the default label.

**Support the optional e5 instruction prefix in the crate contract.** The NDJSON request gains an optional
`"prefix"` field (serde default `""`). When present it is prepended verbatim to `text` before
tokenization (`apply_prefix`, unit-tested model-free); when absent the input is embedded unchanged, so
pre-swap callers are byte-identical. Deciding *which* prefix a call needs (`query: ` for a search query,
`passage: ` for an indexed document) is the Go caller's job and remains the documented follow-up — the
pure crate stays intent-agnostic and applies only what it is handed.

### Why multilingual-e5-small (and why it is a clean swap)

| Property | `bge-small-en-v1.5` (old) | `multilingual-e5-small` (new) |
|---|---|---|
| License | MIT | **MIT** (verified against the model card) |
| Hidden dim | 384 | **384** (identical — dimension-preserving) |
| Language coverage | English only | **100+ languages** (af…zh) |
| Canonical pooling | CLS | **mean/average** — matches what the crate already does |
| Architecture | BERT-class | BERT-class (config.json-driven; directory swap) |

Two properties make this low-risk:

1. **Dimension-preserving (384 → 384).** No vector-width change, so **existing Qdrant collections stay
   compatible** — no collection re-creation, no `ErrDimensionMismatch`, no change to
   `AISOTROPY_EMBED_DIM` (still 384). The Go side has no hardcoded local-embedder model constant and reads
   dimension from `AISOTROPY_EMBED_DIM`/the model's runtime output, so the swap needs **zero Go dimension
   change**. (Honest caveat below on vector *comparability*.)
2. **Pooling now matches the weights.** The crate mean-pools + L2-normalizes. Mean/average pooling is the
   *canonical* recipe for the e5 family (bge canonically uses CLS pooling), so the swap actually aligns the
   existing recipe with the model rather than fighting it.

### License gate

All components remain 100% MIT/Apache-2.0 (ADR-0001 / license-policy hard gate), unchanged from ADR-0023
except the model:

| Component | License |
|---|---|
| `intfloat/multilingual-e5-small` (new default weights) | **MIT** |
| candle-core / candle-nn / candle-transformers | dual MIT / Apache-2.0 |
| tokenizers | Apache-2.0 |
| serde / serde_json | MIT / Apache-2.0 |

No new dependency is introduced; only the pinned weights the operator fetches into
`AISOTROPY_EMBED_MODEL_DIR` change.

## Consequences

- **Cross-lingual RAG works on the sovereign default.** A query in one language can now retrieve
  semantically-matching passages written in another, on the local zero-network path — the R2 gap is
  closed for the *embedder* tier. (The other i18n gaps in the R2 matrix — English-hardcoded injection
  regexes, toxicity keywords, FTS5 `porter` tokenizer — are out of scope here and owned elsewhere.)
- **No interface / boundary churn, no re-index.** `Embedder` is unchanged; the subprocess/daemon seam is
  unchanged; 384-dim is preserved so existing collections keep working.
- **Retrieval quality is best with the e5 prefixes.** The crate now supports them; wiring the Go seam to
  emit `query: ` on searches and `passage: ` on ingested documents is the remaining follow-up (ADR-0023
  debt #4). Until then, unprefixed e5 embeddings still work and still beat the English-only model
  cross-lingually — just below e5's prefixed ceiling.

### Honest residuals

- **Vector comparability across the swap.** Vectors already stored with `bge-small-en-v1.5` are not in the
  same embedding space as `multilingual-e5-small` vectors; mixing them in one collection would compare
  apples to oranges. The swap is *dimension*-compatible (no `ErrDimensionMismatch`, no schema change), but
  a corpus embedded with the old model should be **re-embedded** for semantically-correct results. This is
  a data-migration concern for the operator, not a code-compatibility break — and it does not apply to the
  default-dormant path (the local embedder is opt-in via `AISOTROPY_EMBED_BIN`).
- **Prefix intent still lives in the caller.** The crate cannot know query-vs-passage; a blanket prefix
  applied crate-side could *hurt* retrieval, so the crate deliberately stays intent-agnostic and the
  decision is deferred to the Go seam.
- **Weight provenance unchanged from ADR-0023:** MIT weights we run but did not train; still the ADR-0005
  "borrowed local models" incrementalism, not a from-scratch model.

## References

- [`docs/research/i18n-multilingual-readiness-2026-07-17.md`](../research/i18n-multilingual-readiness-2026-07-17.md) (R2 — the gap this fixes)
- [ADR-0023](0023-sovereign-local-embedder-semantic-tier.md) (the embedder this amends; already recommended this model + flagged the prefix debt)
- [ADR-0001](0001-go-rust-in-process-first-stack.md) / [ADR-0005](0005-datafusion-go-rust-boundary.md) (the stack + subprocess boundary, unchanged)
- candle — <https://github.com/huggingface/candle> · `intfloat/multilingual-e5-small` (MIT, 384-dim, 100+ languages)
