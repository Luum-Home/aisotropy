---
type: Component
title: Qdrant — Semantic Memory
description: Rust vector engine implementing the semantic (long-term associative) memory layer with native multi-tenant isolation via payload filtering during the HNSW scan.
tags: [components, memory, rust, vector-search, multi-tenancy, qdrant]
timestamp: 2026-07-08T00:00:00Z
---

# Qdrant — Semantic Memory

## Role in the system

In the [LLM OS](../architecture/overview.md#llm-os) model, Qdrant is **long-term associative
memory**: the coldest, highest-volume tier of the [memory hierarchy](../architecture/memory-tiers.md#3-semantic-memory-evolving-profile).
It stores embeddings of the tenant's massive, unstructured knowledge — documentation, manuals,
knowledge bases — not one-off facts or user preferences. Those go to
[rqlite](rqlite-facts-memory.md) via FTS5, following the
[KB-over-RAG](../vision/principles.md#3-curated-knowledge-base--rag-for-stable-knowledge) principle:
vectors are reserved for what *cannot* be curated by hand due to volume or lack of structure.

It is also the **only heavy service that scales independently** of the monolithic Go binary — see
the [golden rule of the stack](../architecture/overview.md#the-stack-closed-100-mitapache-20). Huma,
Gollem, BadgerDB, and rqlite coexist in-process or in the same lightweight mesh; Qdrant lives behind
an explicit network boundary.

## Responsibilities

- Index tenant knowledge embeddings (docs, manuals, bulk content) into collections.
- Resolve similarity searches (kNN / ANN via HNSW) with **hard multi-tenant isolation** in
  the search engine itself, not in a downstream application layer.
- Apply quantization to keep the RAM footprint viable at scale.
- Serve as the destination for reinjecting [sleep cycle](datafusion-sleep-cycle.md) conclusions
  (vectors updated after deduplication/consolidation).

## Integration surface: how Go calls it

Communication is exclusively via **gRPC** (Qdrant exposes both HTTP and gRPC; gRPC is used for
production for lower latency and serialization overhead — *(exact recommended Go client version
to verify)*). The conceptual pattern, already sketched in
[tiered memory](../architecture/memory-tiers.md#3-semantic-memory-evolving-profile):

```go
// Search with isolation (conceptual)
qdrant.Search(ctx, userEmbedding, Filter{
    "tenant_id": tenantID,
    "user_id":   userID,
})
```

The Go `ctx` (carrying `tenant_id`/`user_id` from Huma, see
[ctx-based isolation](../vision/principles.md#derived-operating-principles)) is what builds the
filter on every call. **There is no global state or tenant session cache** between requests: the
filter is rebuilt per request from the `ctx`.

Typical integration point in the [request lifecycle](../architecture/overview.md#lifecycle-of-a-request):
parallel memory hydration (step 3) fires the Qdrant query as one more goroutine,
alongside BadgerDB and rqlite, only when the prompt requires RAG over unstructured knowledge.

## Key configuration/patterns

| Pattern | Detail |
|---|---|
| **Rigid payload filtering** | The `tenant_id`/`user_id` filter is applied *during* the HNSW graph traversal (single-pass), not as a post-filter after fetching the k-nearest-neighbors. This gives multi-tenancy by design: it is structurally impossible for a scan to cross another tenant's data, and it's faster than filtering afterward because it doesn't waste ANN candidates on discardable results. |
| **Per-collection vs. per-payload multi-tenancy** | Qdrant supports both separate collections per tenant and a shared collection with an indexed payload filter. Aisotropy uses the second model (shared collection + payload index on `tenant_id`/`user_id`) to avoid exploding the collection count with many small tenants *(to verify whether recent Qdrant versions' "tiered multitenancy" model is worthwhile for large vs. small tenants)*. |
| **Quantization** | Binary/scalar quantization reduces RAM by up to ~90% (Qdrant's documentation reports even larger reductions in recent versions) in exchange for tunable precision — a configurable trade-off per collection. |
| **Idempotency** | Point IDs are generated as **deterministic UUID v5** from `tenant_id + user_id + chat_message_id`. So if the worker consuming the [BadgerDB Outbox](../architecture/consistency.md) reprocesses an event (retries, at-least-once), the upsert is a true no-op and doesn't duplicate vectors. |
| **Reranking** | The ANN result passes through a reranker before entering Gollem's context — provider decision (Cohere SaaS vs. local BGE) still open, see [ADRs](../decisions/index.md). |

## Gotchas and risks

- **It's the only real horizontal scaling point in the stack.** A capacity incident in
  Qdrant is the only one that can't be resolved by scaling the Go binary — it requires
  sharding/replicas of Qdrant itself.
- **A poorly built filter = tenant leak.** Payload filtering is strong *if the filter is always
  built*; a call site that omits `tenant_id` in the `Filter{}` breaks isolation without the
  engine detecting it (Qdrant doesn't enforce the filter, it applies it if given one). It's the
  Go layer's responsibility to guarantee that no code reaches `qdrant.Search` without the filter
  derived from the `ctx`.
- **Layer confusion.** The most likely design risk is treating Qdrant as general memory:
  if it starts receiving facts/preferences that should live in rqlite, the
  [KB-over-RAG](../vision/principles.md#3-curated-knowledge-base--rag-for-stable-knowledge) principle is lost and you pay in embedding
  cost + recall noise.
- **Eventual consistency with the sleep cycle.** The vectors Qdrant serves during the day can
  become stale until [DataFusion](datafusion-sleep-cycle.md) consolidates and reinjects them;
  see the guarantees in [consistency](../architecture/consistency.md).

## Related decisions

- [ADR-0001](../decisions/index.md) — Go+Rust in-process-first stack: Qdrant is the explicit
  exception to "everything in-process" for being the dedicated vector engine.
- [ADR-0003](../decisions/index.md) — specialized hybrid stack vs. multi-model SurrealDB:
  consolidating the vector store into SurrealDB was ruled out because dedicated vector engines
  mature better at scale.
- [ADR-0006](../decisions/index.md) — Reranker: Cohere SaaS vs. local BGE-reranker (pending):
  directly affects Qdrant's result post-processing pipeline.
- [ADR-0007](../decisions/index.md) — Multi-tenancy strategy: single filtered DB vs. DB-per-tenant
  (pending): decides whether the shared-collection + payload-filter model is kept or migrated to
  per-tenant collections past a certain size.

## References

- [Architecture overview](../architecture/overview.md)
- [Tiered memory](../architecture/memory-tiers.md)
- [Design principles](../vision/principles.md)
- [Consistency](../architecture/consistency.md)
- Repository: [github.com/qdrant/qdrant](https://github.com/qdrant/qdrant) (Rust, Apache-2.0)
