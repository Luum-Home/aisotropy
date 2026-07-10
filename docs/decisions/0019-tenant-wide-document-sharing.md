---
type: ADR
title: "ADR-0019 — Tenant-wide document sharing (shared KindDocument bucket)"
description: A tenant's knowledge base is shared across its users, not re-ingested per user. Documents may be ingested at tenant scope (user-less); retrieval for a (tenant,user) merges the user's own vectors with the tenant-shared Documents. Deliberately relaxes the USER boundary within a tenant for KindDocument only — never the tenant boundary, never for Fact/Turn.
tags: [decisions, adr, semantic, isolation, multi-tenancy, rag, ingestion]
timestamp: 2026-07-10T00:00:00Z
---

# ADR-0019 — Tenant-wide document sharing

## Status

Accepted — 2026-07-10. Resolves [ADR-0018](0018-knowledge-ingestion-surface.md)'s open question #1
(Alternative C). Builds on [ADR-0017](0017-semantic-tier-multi-kind-memory.md) (the Document kind).

## Context

ADR-0018 ingests Documents at `(tenant, user)` scope. But a tenant's knowledge base — product docs,
FAQs, policies — is almost always **shared across the tenant's users**, not owned per user. Under
the per-user model every user must re-ingest the same corpus, wasting embeds and storage and drifting
out of sync. The framework needs a **tenant-shared** Document scope.

The subtlety: aisotropy's constitutional gate is per-`(tenant, user)` isolation, applied *during* the
scan (never a post-filter). Tenant-wide sharing deliberately relaxes the **user** boundary *within a
single tenant* — it must never touch the **tenant** boundary (no cross-tenant leak), and must apply
**only to KindDocument**, never to Fact or Turn (which stay strictly user-scoped). This is a
legitimate, explicit design choice, not a hole — hence an ADR rather than a silent widening.

## Goals / Non-goals

**Goals**
- Ingest a Document once at tenant scope; every user of that tenant retrieves it.
- Keep Fact and Turn strictly `(tenant, user)`-scoped.
- Keep cross-tenant isolation absolute and by-construction.
- No special OR-filter smuggled into the store's scan — each scan stays single-scope-bound.

**Non-goals**
- Per-document ACLs / user groups within a tenant (only "private to a user" vs "shared to the whole
  tenant" — two levels, not arbitrary sharing).
- Sharing Fact/Turn across users.

## Decision

**A tenant-shared Document lives under a tenant-only scope (no user). Retrieval merges two
single-scope searches: the user's own, and the tenant-shared one.**

1. **Shared scope** — add `semantic.NewTenantScope(tenant) (Scope, error)`: a valid `Scope` with a
   tenant and an empty user (`tenancy` already allows a user-less scope; this makes it explicit and
   validated). Its points carry `UserID == ""` — the tenant-shared bucket. A user-scoped scan
   (`UserID == Y`) never matches shared points, and vice-versa: **each scan is still strictly
   single-scope, isolation intact.**
2. **Ingestion surface** (both surfaces, mirroring ADR-0018):
   - SDK: `App.IngestTenantKnowledge(ctx, tenant string, docs ...Document) (int, error)` — ingests
     to the tenant-shared scope.
   - HTTP: `POST /v1/tenant/{tenant_id}/knowledge` (no `user` path segment) — tenant-shared; the
     existing `POST /v1/tenant/{tenant_id}/user/{user_id}/knowledge` stays user-private.
3. **Retrieval** — `hydrateSystemPrompt` performs TWO scope-bound Document searches and merges: the
   user scope (`KindDocument, KindFact`, as today) PLUS the tenant scope (`KindDocument` only),
   deduped, top-`semanticWindow` by score. Two clean single-scope searches — no widened store filter.
4. **Guardrail** — only `KindDocument` is ever written to, or read from, the tenant-shared bucket.
   Fact/Turn writes always use a user scope. A test asserts a tenant-shared search never returns
   Fact/Turn points.

## Alternatives considered

- **A — OR-filter in the store** (`tenant AND (user=Y OR user="")`). Rejected: smuggles a
  multi-scope predicate into the isolation scan, exactly the "post-filter / relaxed scan" the gate
  forbids as a pattern. The two-clean-searches approach keeps every scan single-scope.
- **B — A magic reserved username** (e.g. `"__tenant__"`). Rejected: collides with a real user of
  that name and needs charset carve-outs. An empty user (tenant-only scope) is the honest "no user"
  representation `tenancy` already supports.
- **C — Per-document user ACLs.** Rejected as over-scope: two levels (user-private / tenant-shared)
  cover the real need; arbitrary ACLs are a different product.
- **D — Keep per-user only (status quo).** Rejected: forces re-ingestion per user; not how a tenant
  knowledge base works.

## Consequences

**Positive**
- A tenant ingests its corpus once; all its users get RAG over it.
- Isolation stays by-construction: cross-tenant impossible; each scan single-scope; the user-boundary
  relaxation is explicit, KindDocument-only, and test-guarded.

**Negative / limits**
- Two Document searches per hydration instead of one (small, bounded by `semanticWindow`); dedup on
  merge.
- "Tenant-shared" is all-or-nothing within a tenant (no sub-groups) — deliberate.
- The empty-user scope is a new shape callers must get only via `NewTenantScope` (never a bare
  literal — `semantic.Scope` stays a VO, ADR-0011).

**Mandatory**
- Only KindDocument uses the tenant-shared bucket. Fact/Turn always user-scoped. Enforced by test.

## Open questions

1. Deletion of tenant-shared docs (`DELETE /v1/tenant/{tid}/knowledge/{docID}`) — same delete-by-
   source mechanism as the user-scoped DELETE (ADR-0018 open #3), just on the tenant scope.
2. Whether tenant-shared Fact-kind (curated tenant facts, dual-indexed and shared) is ever wanted —
   deferred; today only Documents share.

## References

- [ADR-0018](0018-knowledge-ingestion-surface.md) — the user-scoped ingestion this extends (its open
  question #1).
- [ADR-0017](0017-semantic-tier-multi-kind-memory.md) — the Document kind + hydration search
  (Document+Fact) this augments.
- [ADR-0011](0011-tenant-scoped-statement-builder.md) — `semantic.Scope` VO; `NewTenantScope` joins
  `NewScope`/`ScopeFromContext` as the only constructors.
- [multi-tenancy.md](../architecture/multi-tenancy.md) / [constitutional gates](../../.claude/rules/constitutional-gates.md)
  — the isolation gate this relaxes *only* along the within-tenant user axis, *only* for Documents.
