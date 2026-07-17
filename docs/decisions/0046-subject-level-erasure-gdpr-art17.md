---
type: ADR
title: "ADR-0046 — Subject-level erasure (GDPR Art.17): App.EraseSubject fans out across every memory tier"
description: A new public facade method aisotropy.App.EraseSubject(ctx, tenantID, userID) erases ALL data for one (tenant,user) across every memory tier — facts rows (current + superseded), semantic points of every kind (KindFact, KindTurn, user-scoped KindDocument), and working-memory chat buffers/summaries — returning a structured EraseReport with per-tier counts as an audit record. Each tier gains an optional ScopeEraser capability (implemented by every production store) so the core Store interfaces and their many test doubles are untouched. Scoped, idempotent, isolation-preserving; a blank identity errors rather than wildcard-deleting. Tenant-shared documents are deliberately preserved.
tags: [decisions, adr, security, gdpr, erasure, privacy, multi-tenancy, memory-tiers]
timestamp: 2026-07-17T00:00:00Z
---

# ADR-0046 — Subject-level erasure (GDPR Art.17): `App.EraseSubject` fans out across every memory tier

## Status

✅ **Accepted (2026-07-17).** Security/privacy fix from a data-governance audit (Gap 6). Adds a public
facade method + an optional per-tier capability; no existing behavior changes.

## Context

Before this decision, each memory tier had only **per-item** deletion (delete a fact by id, a vector
by id/source, prune turns by age/count). There was **no "erase everything for a (tenant,user)"**
across all tiers — so a data-subject erasure request (GDPR Art.17, "right to be forgotten") could not
be honoured completely: working-memory chat history and `KindTurn` vectors had no erase-by-subject
path at all.

Two shapes were possible for the primitive:

1. Add an `EraseScope` method to the `facts.Store` and `semantic.Store` **interfaces**. Correct, but
   ripples to ~10 in-repo test doubles that implement those interfaces, inflating blast radius on a
   security fix that must keep the tree green.
2. Add `EraseScope` as an **optional capability interface** (`ScopeEraser`) that every *production*
   store implements, with the facade asserting it and erroring loudly if a configured store lacks it.

## Decision

- **Capability interfaces**, not core-interface changes: `facts.ScopeEraser` and
  `semantic.ScopeEraser`, each `EraseScope(ctx, sc) (int, error)`. Implemented by **every** production
  store: facts `SQLiteStore` / `RqliteStore` / `RoutingStore` / sealed `scopedDocStore`; semantic
  `InMemoryStore` / `QdrantStore` / `RoutingStore` / sealed `scopedVectorStore`. Test doubles that only
  implement the core `Store` are unaffected.
- **Working memory** gains `memory.WorkingMemory.EraseSubject(ctx, tenant, user)` deleting every key
  under the validated `tenant:{t}:user:{u}:` prefix (`memory.SubjectPrefix`), across all of the
  subject's sessions, in batched transactions.
- **Public facade:** `aisotropy.App.EraseSubject(ctx, tenantID, userID) (EraseReport, error)`
  (`aisotropy/erase.go`) validates identity through the blessed path
  (`memory.ValidateIdentityComponent` → `facts.NewScope` / `semantic.NewScope`), boots the shared
  runtime (like `Turn`), and fans out to facts + semantic + working memory. It returns an
  `EraseReport{FactsDeleted, SemanticDeleted, WorkingMemoryDeleted}` — counts and identity only, never
  erased content — as the caller's **audit record**, and logs the same.
- **Hard invariants:**
  - **Scoped, never wildcard.** A blank or malformed tenant/user returns an error and erases NOTHING.
    Every tier confines its delete to the exact (tenant,user); another tenant's or user's data is
    structurally unreachable.
  - **Idempotent.** Erasing a subject with no data returns a zero-count report; erasing twice is safe.
  - **Required, never silently skipped.** A configured store that does not implement its `ScopeEraser`
    is a hard error, so a tier can never be silently left un-erased.
  - **Engram pairing covered.** The fact+vector aggregate (ADR-0017) is fully removed: fact rows go via
    the facts erase, their `KindFact` vectors via the semantic erase (which sweeps *all* kinds).

## Boundary: tenant-shared documents are preserved

Semantic `EraseScope` is scoped to `(tenant, user)`. Tenant-**shared** `KindDocument` points carry an
empty `UserID` (`NewTenantScope`, ADR-0019) and therefore live under a **different** scope — they are
NOT one user's personal data and are left intact by a per-user erase. Removing tenant-shared knowledge
is a separate **tenant-level erase**, a documented follow-on (see below), not part of subject erasure.

## Consequences

- **Positive:** GDPR Art.17 is honourable end-to-end at the runtime level with a small, well-tested
  surface; the audit trail (`EraseReport` + structured log) demonstrates the erasure. Every production
  path is covered; the isolation-preserving erase is proven in tests
  (`aisotropy/erase_test.go`, plus per-tier tests in `internal/{memory,facts,semantic}`).
- **Negative / honest deltas / follow-ons:**
  - **No HTTP route shipped.** The facade method is the deliverable; a `DELETE /v1/tenant/{t}/user/{u}`
    route behind the must-agree auth bind is a clean follow-on but was left out to keep this change
    scoped to the memory tiers + facade.
  - **Tenant-wide erase** (remove a whole tenant including shared documents) is a documented follow-on.
  - Erasing a **live** subject that is concurrently receiving turns can race a re-write; callers should
    stop the subject's traffic first (documented on `EraseSubject`).

## Alternatives considered

- **Core-interface method** (option 1 above). Rejected for blast radius on a green-tree security fix;
  the capability interface gives the same production coverage with an assert-or-error safety net.
- **Cross-tenant `ExportAll` + filter** to find a subject's ids to delete. Rejected: it reaches across
  the isolation gate to do a scoped delete — the wrong shape for a per-subject operation.
