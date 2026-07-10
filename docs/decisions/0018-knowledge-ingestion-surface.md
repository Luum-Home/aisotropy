---
type: ADR
title: "ADR-0018 — Knowledge-ingestion surface (Document kind, dual-surface, BYO embedder)"
description: The document-ingestion path ADR-0017 deferred. A dual-surface (SDK IngestKnowledge + HTTP POST .../knowledge) capability that chunks, embeds (BYO Embedder), and stores tenant/user-scoped KindDocument vectors — the framework's answer to LangChain document loaders / Mastra semantic recall, with tenant-isolation-by-construction and the multi-kind lifecycle baked in.
tags: [decisions, adr, semantic, ingestion, rag, framework, sdk, embedder]
timestamp: 2026-07-10T00:00:00Z
---

# ADR-0018 — Knowledge-ingestion surface (Document kind)

## Status

Accepted — 2026-07-10. Implements [ADR-0017](0017-semantic-tier-multi-kind-memory.md)'s deferred
"Document-kind ingestion path" (its open question #3).

## Context

ADR-0017 made the semantic tier multi-kind and enabled a curated **Document** kind, but deferred the
*ingestion surface* to a separate ADR. Two forces shape it:

1. **Aisotropy is a framework for building chat apps** (vs. LangChain/LangGraph, Mastra), not a
   product with its own fixed corpus. Document ingestion is therefore a *capability the developer
   using aisotropy wires for their app* — the equivalent of LangChain's document loaders + vector
   store or Mastra's semantic recall.
2. **Dual surface** ([ADR-0016](0016-dual-surface-sealed-runtime-composition-sdk.md)): every
   capability is exposed both as a sealed runtime (HTTP) and as a composition SDK. Ingestion must
   follow suit.

The embedder is a **BYO seam** (`semantic.Embedder`): aisotropy ships the interface + a deterministic
`HashEmbedder` placeholder; the framework user plugs a real model. Retrieval quality is only
meaningful with a real embedder — the placeholder wires and tests the path but does not rank
semantically. This is stated plainly so no one mistakes the placeholder for a working RAG.

## Goals / Non-goals

**Goals**
- A dual-surface ingestion capability: SDK `IngestKnowledge` + HTTP `POST .../knowledge`.
- BYO embedder via a `WithEmbedder` option; `HashEmbedder` stays the dev/test default.
- Chunk → embed → store as `KindDocument`, tenant/user-scoped, idempotent, isolation-by-construction.
- Fully buildable and testable with **synthetic documents** + the deterministic embedder (zero
  external dependency).

**Non-goals**
- A specific production embedding model (BYO — the user's choice).
- Sophisticated document parsing (PDF/HTML extraction) — callers pass plain text; parsers are their
  concern or a later feature.
- Tenant-wide document sharing across a tenant's users (see Open questions — kept (tenant,user)-scoped
  here to preserve the existing isolation gate).

## Decision

**Ingestion is a first-class, dual-surface, BYO-embedder capability writing `KindDocument` vectors.**

1. **BYO embedder** — new façade option `WithEmbedder(semantic.Embedder)`; default remains
   `semantic.NewHashEmbedder(0)`. A `WithChunkSize(int)` (and overlap) option tunes chunking.
2. **Ingestion core** — a small `ingest` step: for each document `{ID, Content, Source}`, split
   `Content` into chunks (configurable size + overlap), embed them via the Embedder, and
   `Semantic.Upsert` one `Point{Kind: KindDocument, ID: PointID(sc, KindDocument, docID+"#"+chunkIdx),
   Content: chunkText, Source: docID, CreatedAt: now}` per chunk. Idempotent by construction:
   re-ingesting the same document id replaces its chunks in place.
3. **SDK surface** — `App.IngestKnowledge(ctx, tenant, user string, docs ...Document) (n int, err error)`
   on the `aisotropy` façade; identity validated through `semantic.NewScope` (ADR-0011), so a bad
   scope is impossible.
4. **HTTP surface** — `POST /v1/tenant/{tenant_id}/user/{user_id}/knowledge` (Huma, in the OpenAPI
   contract), body `{documents: [{id, content, source}]}`, returning the chunk count ingested.
   `tenant_id`/`user_id` are path params (identity in the path, never the body — the multi-tenancy
   gate), charset-constrained like the chat routes.
5. **Retrieval** — no change needed: `hydrateSystemPrompt` already searches `KindDocument, KindFact`
   (ADR-0017 Phase 3), so ingested documents flow into RAG automatically, outranking bounded raw
   turns.

Differentiators baked in (vs. LangChain/Mastra): tenant isolation **by construction** (scoped Upsert,
never a post-filter), the multi-kind **lifecycle** (Document persists, Turn is bounded, Fact is
lifecycle-tied), token economy, Go-native, MIT.

## Alternatives considered

- **A — SDK-only, no HTTP.** Rejected: violates the dual-surface invariant (ADR-0016); a runtime
  deployment must be able to ingest without embedding the SDK.
- **B — Auto-ingest from a watched directory / loader zoo (LangChain-style).** Rejected as core:
  parsing/loaders are the caller's concern; aisotropy provides the tenant-scoped, lifecycle-aware
  *sink*, not a parser menagerie. A caller can build loaders on top.
- **C — Tenant-wide document scope** (docs visible to all users of a tenant). Attractive for a real
  knowledge base, but it changes the retrieval isolation filter (today strictly (tenant,user)).
  Deferred to an open question rather than silently widening the isolation surface.
- **D — Ship a real embedder.** Rejected: model choice is the framework user's; forcing one breaks
  BYO and the MIT/no-heavy-dep posture. Ship the seam + placeholder.

## Consequences

**Positive**
- The semantic tier finally has its intended high-value input; RAG over curated knowledge works
  (semantically, once a real embedder is plugged).
- Framework parity with LangChain/Mastra ingestion, with the isolation/lifecycle/economy
  differentiators.
- Buildable/testable offline with synthetic docs + `HashEmbedder`.

**Negative / honest limits**
- With the `HashEmbedder` placeholder the RAG wires but does not rank meaningfully — documented, not
  hidden. Real value needs a real embedder (BYO).
- (tenant,user)-scoped docs mean a tenant's shared knowledge is per-user until the tenant-wide
  variant (Open questions) is built.
- Chunking is size-based (simple); semantic/structure-aware chunking is a later refinement.

**Mandatory**
- Ingestion identity goes through `semantic.NewScope` (never a bare Scope); every `KindDocument`
  Upsert is scope-bound. Nil embedder/semantic → ingestion is a clear error, not a silent no-op.

## Open questions

1. **Tenant-wide document sharing** (Alternative C): a scoping variant where Documents are visible to
   all of a tenant's users. Needs a deliberate change to the retrieval isolation filter; its own ADR.
2. **Chunking strategy**: size+overlap now; sentence/structure-aware or model-tokenizer-aware later.
3. **Deletion/update API**: `POST .../knowledge` re-ingests idempotently; an explicit
   `DELETE .../knowledge/{docID}` (remove a document's chunks via `Store.Delete`) is a small
   follow-up.

## References

- [ADR-0017](0017-semantic-tier-multi-kind-memory.md) — the multi-kind model this fills in (Document
  kind, its deferred open question #3).
- [ADR-0016](0016-dual-surface-sealed-runtime-composition-sdk.md) — the dual-surface invariant this
  ingestion follows.
- [ADR-0011](0011-tenant-scoped-statement-builder.md) — `semantic.NewScope` (VO by construction) the
  ingestion identity routes through.
- [competitive-landscape.md](../research/competitive-landscape.md) — LangChain loaders / Mastra
  recall this is the aisotropy answer to.
- `internal/semantic` (`Embedder`, `Point`, `PointID`, `KindDocument`, `Store.Upsert`), `aisotropy/`
  façade (`App`, `Option`, `WithEmbedder` to add), `internal/api` (the new `RegisterKnowledge`).
