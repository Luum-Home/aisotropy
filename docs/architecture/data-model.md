---
type: Reference
title: Data model — the shapes that cross every boundary
description: The unified schema reference for Aisotropy. One node for the identity/scope keys, the HTTP contract structs (chat, agent, knowledge), and the per-tier storage shapes (facts row + FTS5, semantic Point + Kind, working-memory key layout, outbox event). The Go struct is the single source of truth; this documents the shapes those structs produce, verified against the committed code.
tags: [architecture, reference, data-model, schema, contract, facts, semantic, outbox]
timestamp: 2026-07-10T00:00:00Z
---

# Data model — the shapes that cross every boundary

The [thesis](../vision/thesis.md) is that **the Go struct is the single source of truth** — it drives
HTTP validation, the OpenAPI 3.1 schema, and the JSON contract enforced on the LLM at once. This node
collects those shapes in one place so an architect does not have to reassemble them from
`internal/*` and the ADRs. Every field below is verified against the committed code; the
[API reference](api-reference.md) documents the *endpoints* that carry them, and the
[reality ledger](reality-ledger.md) says which tiers are live vs dormant.

## 1. Identity & scope (the isolation key)

Every stored shape is partitioned by identity. There is exactly one identity model, propagated via
`context.Context` (never a body field as the source of truth — see the [threat model](threat-model.md)).

| Component | Where it enters | Constraint | Role |
|---|---|---|---|
| `tenant_id` | path param | `^[a-zA-Z0-9_-]{1,64}$` (Huma 422 on violation) | tenant isolation key |
| `user_id` | path param | `^[a-zA-Z0-9_-]{1,64}$` | per-user isolation key |
| `session_id` | body (`ChatRequest`) | none — carries **no** authorization meaning | working-memory conversation key |
| `scopes[]` | body (`ChatRequest`) | — | permission stand-in gating tool access (⚠️ not yet from a verified JWT — [threat model](threat-model.md) T3) |

- **Reserved key characters** (`internal/memory/key.go`): `:`, `\x00`, `\n` — rejected in any
  identity component, so composite keys can never be forged by injection.
- **Working-memory composite key**: `tenant:{tenant}:user:{user}:session:{session}`; a chat message
  key appends a zero-padded sequence (`…%020d`) for lexical ordering.
- **`facts.Scope`** / **`semantic.Scope`**: `{TenantID, UserID}` — the value every store method
  requires; a facts statement built without it is a **compile error** (ADR-0011).

## 2. HTTP contract shapes (`internal/api`)

### Chat — `ChatRequest` (shared body for all three `/chat*` routes)

| Field | JSON | Type | Notes |
|---|---|---|---|
| Prompt | `prompt` | string | `maxLength:2000` |
| ProductID | `product_id` | string | selects the base persona/system prompt |
| SessionID | `session_id` | string | working-memory session key |
| Scopes | `scopes` | `[]string` | omitempty; gates tool access, e.g. `["write:tasks"]` |

### Chat responses

- **`ChatOutput.Body`** (non-streaming `/chat`): `reply` (schema-enforced via the reflection loop),
  `tenant_id` (isolation echo), `stub` (bool — true when the credential-less stub LLM answered).
- **SSE events** (`/chat/stream`): `token{ text }` · `done{ tenant_id }` · `error{ message }`.
- **`AgentOutput.Body`** (`/chat/agent`): `tenant_id`, `steps[]` where each **`AgentStep`** is
  `{ tool, args: map[string]string (omitempty), result (omitempty) }`. `tool:""` means "answered
  directly, no tool."

### Knowledge ingestion (`/knowledge`, ADR-0018/0019)

- **`KnowledgeDocument`**: `{ id (re-ingesting the same id idempotently replaces its chunks),
  content, source? (provenance for citations) }`.
- **`KnowledgeRequest`**: `{ documents: []KnowledgeDocument }` → **`KnowledgeOutput.Body`**:
  `{ chunks_ingested: int }`.
- **Delete**: path `doc_id` (`^[a-zA-Z0-9_.-]{1,128}$`) → `{ deleted: int }`.
- **Tenant-wide variants** (`TenantKnowledgeInput` / `…DeleteInput`) drop `user_id`: a document
  ingested at tenant scope is retrievable by every user in the tenant (ADR-0019), stored as a
  tenant-shared `KindDocument`.

## 3. Storage shapes per tier

### Facts (`internal/facts`, SQLite+FTS5 or rqlite)

`Fact`: `{ ID, TenantID, UserID, Content, Importance float64, CreatedAt, UpdatedAt,
SupersededBy string, Score float64 }`. Persisted with an FTS5 virtual table; `Search` ranks by
**bm25 × Importance × Recency**. `SupersededBy` implements Engram-style contradiction handling (an
old fact is archived, not deleted). `Score` is a query-time output, not stored.

### Semantic (`internal/semantic`, Qdrant or in-process cosine) — multi-kind (ADR-0017)

`Point`: `{ ID, TenantID, UserID, Kind, Content, Source, Citation, Vector []float32, CreatedAt,
Score float32 }`.

| `Kind` | value | Origin | Lifecycle |
|---|---|---|---|
| `KindDocument` | `"document"` | knowledge ingestion (user- or tenant-scoped) | explicit ingest/delete by `doc_id` |
| `KindFact` | `"fact"` | distilled facts, dual-indexed via the `engram` facade | tied to the fact (supersede/delete removes the vector) |
| `KindTurn` | `"turn"` | raw turns embedded by consolidation | **bounded** — TTL/cap pruner (`PruneTurns`) evicts old turns |

**`PointID(scope, kind, sourceID)`** is a deterministic UUIDv5 — it encodes the kind so a fact's and
a turn's vector for the same source can never collide, and re-consolidating the same source upserts
in place (idempotent redelivery). Retrieval merges user-scoped `KindDocument`+`KindFact` with the
tenant-shared `KindDocument` bucket; `KindTurn` is excluded from primary RAG.

### Working memory (`internal/memory`, BadgerDB)

Keyed by the composite key of §1. Holds: the live chat messages (sequence-ordered), the condensed
summary buffer, the per-tenant quota buckets, and the outbox queue — all in one embedded store.

### Outbox event (`internal/memory/outbox.go`) — the consistency record

`OutboxEvent` (JSON): `{ id, tenant, user, session, content, created_at }`. Written **in the same
BadgerDB transaction** as the chat message (transactional outbox, [consistency](consistency.md)).
Key: `outbox:pending:{tenant}:{ts}:{id}`. The `id` is a stable sha256-derived ref of `(key, prompt)`
so re-sending an identical prompt consolidates onto the same fact/vector; the key is deleted only
after both the facts and semantic sinks succeed (at-least-once, idempotent).

## References

- [API reference](api-reference.md) — the endpoints these shapes travel through.
- [runtime as built](runtime-as-built.md) — where each shape is produced/consumed in the request path.
- [tiered memory](memory-tiers.md) · [consistency](consistency.md) · [multi-tenancy](multi-tenancy.md)
- Source of truth: `internal/api/{chat,chat_stream,chat_agent,knowledge}.go`, `internal/facts/facts.go`, `internal/semantic/semantic.go`, `internal/memory/{key,outbox}.go`.
