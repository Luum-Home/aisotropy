---
type: ADR
title: "ADR-0054 — e5 query:/passage: instruction prefixes on the Go semantic seam"
description: ADR-0049 swapped the sovereign embedder default to intfloat/multilingual-e5-small and added an OPTIONAL crate-side `prefix` request field, but the Go side still embedded unprefixed. The e5 family needs `query: ` on search text and `passage: ` on stored documents/facts for correct retrieval. Decision — wire the prefix through the Go seam via a role-aware `RoleEmbedder` capability and `EmbedQuery`/`EmbedPassage` helpers, guarded model-aware (e5 only; a non-e5/external embedder is untouched, since a blanket prefix hurts it). Re-embedding an old bge corpus is a documented operator data-migration (re-ingest), not a code break — the local embedder is opt-in.
tags: [decisions, adr, bet-05, embeddings, semantic, multilingual, i18n, e5, cross-lingual-rag]
timestamp: 2026-07-17T00:00:00Z
supersedes: none — closes the deferred query/passage prefix follow-up named in ADR-0049 and ADR-0023 #4
---

# ADR-0054 — e5 query:/passage: instruction prefixes on the Go semantic seam

## Status

✅ **Accepted — implemented 2026-07-17.** Closes the Go-caller half of the e5 prefix work that
[ADR-0049](0049-multilingual-embedder.md) deferred ("Deciding *which* prefix a call needs … is the Go
caller's job and remains the documented follow-up") and that [ADR-0023](0023-sovereign-local-embedder-semantic-tier.md)
first flagged as debt #4. The crate contract (the optional `prefix` request field, `#[serde(default)]`)
is unchanged — this ADR only adds the Go wiring that decides and supplies the prefix.

## Context

The intfloat/e5 model family — the ADR-0049 default `multilingual-e5-small` — was trained with
**asymmetric instruction prefixes**: search text is embedded as `query: <text>` and indexed documents as
`passage: <text>`. The model learned to place a query and its matching passage close in vector space
*only when both carry their prefix*. Using no prefix (or the same prefix on both sides) measurably
degrades retrieval on e5.

After ADR-0049 the crate (`crates/embed`) accepts an optional per-request `prefix` field, but the Go
core still called `Embedder.Embed(ctx, texts)` everywhere with **no prefix** — so on the shipped e5
default, both the stored corpus and the search query were unprefixed, and retrieval ran off-nominal. The
crate deliberately never infers query-vs-passage intent; that decision belongs to the Go call site, which
alone knows whether it is indexing (`internal/semantic.Ingest`, fact/turn consolidation) or searching
(the query-embedding step before `Store.Search`).

Two constraints shaped the design:

1. **Model-aware, not unconditional.** The prefix is correct *only* for e5. `HashEmbedder` (the
   credential-less default), `APIEmbedder` (an external OpenAI-compatible endpoint), and any non-e5 local
   model must stay unprefixed — a blanket `query: `/`passage: ` is noise that hurts a model not trained
   for it. So the guard is per-embedder, defaulting off.
2. **Byte-compatibility.** An unprefixed request must remain byte-identical to the pre-ADR-0054 NDJSON, so
   the swap changes nothing for existing non-e5 deployments.

## Decision

### 1. A role vocabulary + a role-aware capability (`internal/semantic/role.go`)

- `EmbedRole` — `RoleUnspecified` (zero value, no prefix), `RoleQuery` (`"query: "`), `RolePassage`
  (`"passage: "`). The prefix strings mirror the crate's `apply_prefix` convention byte-for-byte.
- `RoleEmbedder` — an **optional** interface (`Embedder` + `EmbedForRole(ctx, texts, role)`). Only the
  sovereign local embedders (`LocalEmbedder`, `DaemonEmbedder`) implement it; `HashEmbedder` and
  `APIEmbedder` do not.
- `EmbedForRole(ctx, emb, texts, role)` — the single choke point. If `emb` is a `RoleEmbedder` it
  forwards the role; otherwise it falls back to `emb.Embed` — a **non-e5/external embedder is
  structurally unaffected**. `EmbedQuery` / `EmbedPassage` are the two named call-site helpers over it.

### 2. The model-aware guard on the local embedders

`LocalEmbedderConfig` / `DaemonEmbedderConfig` gain `E5Prefixes bool`. `EmbedForRole` sends the role's
prefix only when `E5Prefixes` is true; when false it sends `""` (byte-identical to before). The shared
NDJSON `localEmbedRequest` gains `Prefix string json:"prefix,omitempty"` — `omitempty` + the crate's
`#[serde(default)]` means an empty prefix is absent from the wire and the crate embeds unchanged.

`E5Prefixes` is set once, model-aware, in `EmbedderFromEnv`:

- `AISOTROPY_EMBED_E5_PREFIXES` (any `strconv.ParseBool` form) is **authoritative** when set.
- Unset ⇒ autodetect the e5 family from the pinned model dir path: `isE5Model` tokenizes on
  non-alphanumeric boundaries and matches the exact token `e5` (so `multilingual-e5-small` and
  `e5-base-v2` enable it, while an unrelated `base5-experimental` does **not** false-match). A non-e5
  path leaves prefixing off.

### 3. Call-site wiring (this change owns `internal/semantic`)

- **Ingest (`internal/semantic/ingest.go`) → `EmbedPassage`.** Every stored `KindDocument` chunk carries
  the passage prefix on an e5 embedder.
- **Search / fact-and-turn indexing → `EmbedQuery` / `EmbedPassage`.** The query-embedding step before a
  `Store.Search` MUST use `semantic.EmbedQuery`, and fact/turn consolidation that embeds stored content
  MUST use `semantic.EmbedPassage`. Those call sites live **outside** `internal/semantic` (the query
  embed is in `internal/api`'s hydration path and `internal/responsecache`; fact/turn embedding is in
  `internal/engram` and `internal/consolidation`). They are a one-line swap from `emb.Embed(ctx, …)` to
  `semantic.EmbedQuery(ctx, emb, …)` / `EmbedPassage(...)` and are the **remaining adoption step** for
  the owners of those packages — the seam and the passage-side (ingest) are wired here. Until they adopt
  it, those paths simply behave as today (unprefixed): correctness degrades gracefully, it does not break.

## Re-embed migration path (old bge corpus)

This is a **data migration, not a code break**, and only affects deployments that had opted into the
sovereign local embedder (`AISOTROPY_EMBED_BIN`) with the pre-ADR-0049 `bge-small-en` model *and* now
run e5 with prefixes on. The keyless `HashEmbedder` default and any external `APIEmbedder` are unaffected
(they never prefix). Vectors already written by bge live in the same 384-dim space as e5 (ADR-0049 is
dimension-preserving), so nothing *fails* — but a bge vector was produced by a different model and an
`e5` `query: `-prefixed search will not match it well. To realign a corpus:

1. **KindDocument** — re-run `semantic.Ingest` over the operator's source documents. Ingest is idempotent
   per document id (it `DeleteBySource` + re-upserts), now embeds passages with the passage prefix on an
   e5 embedder, and re-ids deterministically — so re-ingesting the same corpus simply overwrites the old
   bge vectors in place. The source corpus, not the vector store, is the source of truth.
2. **KindFact / KindTurn** — these re-embed through their normal lifecycle: a fact re-saved or a turn
   re-consolidated is embedded with the current embedder + prefix. A one-shot bulk re-embed of existing
   facts is an operator batch that re-drives consolidation over the retained turns.

**Why no in-place bulk re-embed CLI ships here (honest scope note).** An automatic "re-embed every vector
for (tenant,user)" helper is deliberately *not* provided: the `semantic.Store` contract exposes no scope
*enumeration* (`Search` needs a query vector and returns only a ranked top-k, not a full scan), so a
generic in-place re-embed cannot read the existing points' content to re-vectorize them. The content's
authoritative source is the operator's own corpus, which the re-ingest procedure above already covers
cheaply and idempotently. Adding a scan/scroll capability to `Store` (and its Qdrant/routing
implementations) to enable in-place re-embed is a separate, larger change and is left as a future ADR if
an operator without the source corpus ever needs it.

## Consequences

**Positive**
- e5 retrieval runs as trained: query and corpus are prefix-aligned on the sovereign default.
- Non-e5 and external embedders are provably untouched (the fallback in `EmbedForRole`; tests assert a
  plain embedder never sees a role and the NDJSON omits `prefix`).
- Byte-compatible: unprefixed requests are unchanged, so existing deployments see no wire difference.
- The query/passage decision lives at the call site that has the intent, matching the crate's
  intent-agnostic contract.

**Negative / residual**
- Full search-side benefit requires the `internal/api` / `internal/responsecache` /
  `internal/engram` / `internal/consolidation` call sites to adopt `EmbedQuery` / `EmbedPassage` (tracked
  as the follow-up adoption step). The seam makes this a one-liner each.
- Operators who ran the local e5 embedder *before* this change (unprefixed) carry a corpus/query prefix
  mismatch until they re-embed per the migration path above.
- The e5-family autodetect is a heuristic on the model dir name; `AISOTROPY_EMBED_E5_PREFIXES` is the
  authoritative override for any non-standard path.

## Verification

- `go build ./...`, `go vet ./internal/semantic/...`, `gofmt -l` (clean), `go test ./internal/semantic/...`
  all pass.
- Tests (`internal/semantic/role_test.go`): `EmbedQuery`/`EmbedPassage` forward the right role to a fake
  `RoleEmbedder`; `Ingest` embeds with `RolePassage`; a plain (non-`RoleEmbedder`) embedder is unaffected
  and never observes a role; `LocalEmbedder.writeRequest` emits `"prefix":"query: "` / `"passage: "` in
  the NDJSON for an e5 embedder and **omits** the field for a non-e5 one; `isE5Model` accepts the e5
  family and rejects look-alikes; `AISOTROPY_EMBED_E5_PREFIXES` overrides the autodetect both ways.

## References

- [ADR-0049 — Multilingual default for the sovereign local embedder](0049-multilingual-embedder.md) (the swap + crate `prefix` field; named this as the follow-up)
- [ADR-0023 — Sovereign local embedder for the semantic tier](0023-sovereign-local-embedder-semantic-tier.md) (debt #4: the deferred e5 prefix)
- `crates/embed/src/main.rs` — the `Request.prefix` field (`#[serde(default)]`) and `apply_prefix`
- `internal/semantic/role.go`, `local_embedder.go`, `daemon_embedder.go`, `ingest.go`, `embedder_env.go`
