---
type: Concept
title: Multi-tenancy — Isolation via ctx
description: How Aisotropy guarantees real isolation between tenants and users — native propagation via Go's context.Context, anti-leak composite keys, and per-layer filtering. The physical topology is left for ADR-0007.
tags: [architecture, multi-tenancy, ctx, isolation, security]
timestamp: 2026-07-08T00:00:00Z
---

# Multi-tenancy — Isolation via ctx

Aisotropy treats multi-tenancy as a **structural** property of the runtime, not a filter bolted
on afterward. The mechanism is deliberately boring: Go's `context.Context`, the same pattern any
native HTTP service uses to propagate deadlines and cancellation. Zero AI library involved — it's
the direct application of the [zero magic](../vision/thesis.md#the-stance-zero-magic) principle.

## The mechanism: ctx as a process descriptor

[Huma](../components/huma-api.md) is the **only** layer authorized to *write* identity into the
`ctx`: it decodes the request's JWT and extracts `tenant_id`, `user_id`, and `scopes`, injecting
them into the `context.Context` that Go natively propagates through the entire call chain —
handlers, memory-hydration goroutines, [Gollem](../components/gollem-orchestrator.md), tools. No
downstream component parses auth headers on its own or re-resolves identity: everyone *reads*
from the same `ctx`, never reconstructs it.

```
JWT → [Huma: security handler] → ctx{tenant_id, user_id, scopes}
                                         │
                    native Go propagation (no global state)
                                         │
        ┌────────────────┬──────────────┼──────────────┬────────────────┐
        ▼                ▼              ▼              ▼                ▼
    BadgerDB          rqlite         Qdrant          Gollem          Tools/primitives
  (chat:tenant:      (WHERE          (payload        (surgical        (injection
   user:session)      tenant_id=?     filter in        context)         conditioned
                       AND user_id=?)  HNSW)                            by scopes)
```

**Why this is enough for real isolation:** `context.Context` in Go is immutable by design — every
`WithValue` creates a new node in the context tree instead of mutating a shared one, and each
goroutine of a request has its own branch. There's no way for a concurrent request from tenant B
to "see" tenant A's `ctx`, because there is no shared global variable where that could happen.
This is what the project calls
[isolation via ctx](../vision/principles.md#derived-operating-principles): the guarantee comes
from the language and the concurrency pattern, not from a coding discipline someone has to
remember to apply at every layer.

## Anti-leak composite key

The hard rule, no exceptions, at every stateful layer: **never share global agent-memory
instances across concurrent requests from different contexts.** A *context leak* — reusing a
buffer, a cache, or an agent session between two different tenants — is exactly the kind of bug a
"magic" framework with implicit state facilitates, and that Aisotropy makes structurally
difficult: every storage key (cache, session, buffer) carries the composite prefix

```
tenant_id:product_id:session_id
```

(in BadgerDB, see [working memory](../components/badgerdb-working-memory.md#data-model--keys),
the concrete form is `tenant:user:session`; `product_id` applies when the same tenant operates
multiple products/modules on the same framework). The composite key serves a dual purpose: it
isolates *and* documents the scope of each piece of data in the identifier itself, without relying
on whoever reads the data to remember to apply the correct filter.

## Isolation per layer

Each tier of the [memory hierarchy](memory-tiers.md) implements isolation with the tool that's
native to it — there is no generic isolation layer on top; each store resolves the problem with
its own most efficient mechanism:

| Layer | Isolation mechanism | Detail |
|---|---|---|
| [Qdrant](../components/qdrant-semantic-memory.md) (semantic) | **Rigid payload filter during the HNSW scan** | The `tenant_id`/`user_id` filter is applied *during* the graph traversal, not as a post-filter over already-fetched k-nearest-neighbors. It is structurally impossible for a scan to cross into another tenant's data, and it's faster than filtering afterward. |
| [rqlite](../components/rqlite-facts-memory.md) (facts) | **Mandatory `WHERE tenant_id=? AND user_id=?`** | Every FTS5 read or write query carries the explicit filter in the statement — never trusting an implicit filter from an intermediate layer. Applies the same way under the single-filtered-DB strategy; under DB-per-tenant the filter becomes implicit via the connection string, see below. |
| [BadgerDB](../components/badgerdb-working-memory.md) (working) | **Composite keys `tenant:user:session`** | Isolation lives in the key itself, not in a query filter — BadgerDB has no query language, so the key *is* the scoping mechanism. It also leverages the LSM-tree's lexicographic ordering to iterate by prefix without scanning the rest of the keyspace. |

## What this document does NOT decide: the physical topology

There are two possible physical strategies to materialize the logical isolation described above:

1. **Single filtered DB** — a single rqlite cluster (or a single Qdrant collection) shared across
   all tenants, with the `tenant_id`/`user_id` filter applied on every query/search.
2. **DB-per-tenant** — a separate physical database (or collection) per tenant, with isolation
   given by the topology itself (connection string or dynamic collection) instead of an explicit
   `WHERE` in every statement.

This decision **is not made in this document** — it's the exact scope of **[ADR-0007](../decisions/index.md)**
("Multi-tenancy strategy: single filtered DB vs. DB-per-tenant"), which also directly intersects
with [ADR-0004](../decisions/index.md) (rqlite vs. LiteFS for facts memory): the choice of store
affects how cheap it is to spin up a per-tenant database. Qdrant already explicitly documents
that today it uses the shared-collection + payload-index model (see the
[Qdrant component](../components/qdrant-semantic-memory.md#key-configurationpatterns)), but
that the "tiered multitenancy" model for large tenants remains to be verified — the same decision
axis ADR-0007 must resolve for rqlite.

What this document does establish, independent of which topology wins: **logical** isolation —
what identity travels in the `ctx`, what key or filter each layer builds from that identity —
doesn't change based on the chosen physical topology. What changes is whether that filter is
applied within a shared `WHERE` or is implicit in which physical database it connects to.

## Why this wins in enterprise

- **No context leaks between concurrent requests**, the central promise of the framework's
  [thesis](../vision/thesis.md#the-problem).
- **End-to-end auditable**: the `ctx` is readable Go code, not an opaque security layer from an
  external framework — you can trace exactly where identity is written and where it's read.
- **Free observability**: since `tenant_id`/`user_id` travel in the `ctx`, every OTel span can be
  attributed to a tenant with no additional instrumentation — see [observability](observability.md).

*(Patterns described at a conceptual level; exact Huma security-handler function names and the
exact shape of the Qdrant filter — to be verified against the real implementation before coding.)*

## References

- [Architecture overview](overview.md) — request lifecycle, step 1 (ctx extraction).
- [Tiered memory](memory-tiers.md) — the memory triangle whose isolation this document describes.
- [Granular control](granular-control.md) — how the same `ctx` feeds security, cost, and behavior decisions.
- [Huma — Contracts and API layer](../components/huma-api.md) — who writes the ctx.
- [Qdrant — Semantic Memory](../components/qdrant-semantic-memory.md) · [rqlite — Facts Memory](../components/rqlite-facts-memory.md) · [BadgerDB — Working Memory](../components/badgerdb-working-memory.md)
- [ADR-0007](../decisions/0007-multitenancy-fisico-db-unica-vs-por-tenant.md) — physical multi-tenancy strategy (recommended: hybrid/tiered). [ADR-0004](../decisions/0004-facts-memory-rqlite-vs-litefs.md) — rqlite vs. LiteFS (accepted, intersects with 0007).
- [Thesis](../vision/thesis.md) — multi-tenancy with real isolation as one of the four enterprise requirements.
