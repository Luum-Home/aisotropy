---
type: ADR
title: "ADR-0011 — Tenant-scoped statement builder: isolation by construction for the pooled facts tier"
description: internal/tenancy replaces the "always remember WHERE tenant_id" discipline for the pooled rqlite facts tier (ADR-0007) with an API shape where an unscoped statement is a construction-time error, not a code-review catch — built before any Phase 3 rqlite SQL exists.
tags: [decisions, adr, multi-tenancy, rqlite, isolation, tenancy]
timestamp: 2026-07-08T00:00:00Z
---

# ADR-0011 — Tenant-scoped statement builder: isolation by construction for the pooled facts tier

## Status

**✅ Accepted (2026-07-08).** `internal/tenancy` is the mandatory access path for the facts tier
(rqlite) once Phase 3 lands. This ADR does not implement Phase 3 itself — there is still zero
Phase 3 Go code — it establishes the primitive Phase 3 must be built on top of, ahead of any
rqlite-facing SQL being written.

## Context

[Multi-tenancy — isolation via ctx](../architecture/multi-tenancy.md) states the project's
central isolation claim: "the guarantee comes from the language and the concurrency pattern, not
from a coding discipline someone has to remember to apply at every layer." For BadgerDB (working
memory) that claim holds structurally — there is no query language, so the composite key
`tenant:user:session` *is* the only way to address data, and `internal/memory/key.go`'s `Key`
type already makes an invalid or reserved-character key impossible to construct (see
`Key.Validate`, reused by this ADR's design — see Decision below).

For the pooled facts tier (rqlite, [ADR-0007](0007-multitenancy-fisico-db-unica-vs-por-tenant.md),
Option C hybrid/tiered, pooled as the default), the same claim currently degrades to discipline.
[Request lifecycle — step 3](../architecture/request-lifecycle.md#step-3--parallel-memory-hydration)
says it plainly: *"A badly built filter in Qdrant (missing `tenant_id`) is a silent isolation
leak."* The same is true, worse, for rqlite: unlike Qdrant's payload filter (a single call site in
the HNSW scan), every facts-tier query anyone ever writes — hydration reads, sleep-cycle writes,
future tools, future maintenance patches — is a fresh opportunity to forget `WHERE tenant_id=?`.
[ADR-0007](0007-multitenancy-fisico-db-unica-vs-por-tenant.md) says the same thing from the
topology side: *"within a pool, isolation remains logical [...] the risk isn't zero (unlike a
silo)"* — and lists "the WHERE bug still lurks in the pools" as accepted negative/technical debt.

This is the moment to close that gap: **the facts tier has zero Go code today.** No rqlite client,
no query call sites, nothing to migrate or refactor around. Whatever access primitive Phase 3 uses
will be copied by every subsequent call site. Building the enforcement primitive now, before the
first line of Phase 3 SQL exists, means there is no unscoped precedent to accidentally follow.

## Goals / Non-goals

**Goals**

- Make emitting a facts-tier query without a tenant predicate a construction-time impossibility
  (or as close to compile-time as Go's type system allows), not something a reviewer has to catch.
- Stay driver-agnostic: the rqlite HTTP client (ADR-0004) plugs in later without this package
  changing.
- Stay minimal: cover exactly SELECT/INSERT/UPDATE/DELETE with equality WHERE composition — the
  facts tier's actual query shape (FTS5 `MATCH` composition can layer on top later; this ADR does
  not attempt to model it).
- Reuse the existing ctx-injection pattern and identity-validation rule instead of inventing a
  parallel one.
- Add zero new dependencies. stdlib only.

**Non-goals**

- Not implementing Phase 3 itself (the rqlite HTTP client, the routing table between pools/silos
  from ADR-0007, FTS5 `MATCH` query support). Those are follow-up work built *on* this primitive.
- Not an ORM: no relationships, no migrations, no schema management, no query planner.
- Not re-deciding the pooled-vs-siloed topology (ADR-0007) or the engine (ADR-0004) — this ADR
  is orthogonal to both: it hardens the pooled path's `WHERE tenant_id`, and a silo's connection
  can use the exact same builder with a degenerate (but still explicit) Scope, since defense in
  depth costs nothing there (see Consequences).

## Decision

Add `internal/tenancy`, a driver-agnostic, tenant-enforced statement builder:

- **`Scope`** carries tenant (and, optionally, user) identity. Its fields are unexported; the only
  constructor is `FromContext(ctx) (Scope, error)`, which reads identity from `context.Context`
  via a package-local `WithTenant`/`WithUser` carrier — the same ctx-injection *pattern*
  `internal/api/chat.go` and `internal/llm/context.go` already use (each owns its own unexported
  ctx keys; `internal/tenancy` is the third instance of that pattern, not a fourth, different one).
  `FromContext` validates the tenant/user strings via the newly-exported
  `memory.ValidateIdentityComponent` — the exact non-empty / no-reserved-character rule
  `internal/memory/key.go`'s `Key.Validate` already enforces for working memory, refactored out of
  `Key.Validate` and reused here rather than re-implemented, so the two tiers can never silently
  drift on what counts as a valid tenant/user string.
- **`Table`** registers a table's name plus which column holds tenant identity (mandatory) and,
  optionally, which column holds user identity (`Table.ScopedByUser`). Declared once per table,
  panics at construction time on an unsafe identifier — a programmer error in hardcoded source,
  not request input.
- **`Scope.Query(Table) Builder`** is the only entry point into statement construction.
  `Builder`/`SelectStatement`/`InsertStatement`/`UpdateStatement`/`DeleteStatement` all have
  unexported fields and no exported constructor other than that one call chain — there is no
  package-level function that builds a statement from a bare `Table`. A forged/zero-value `Scope`
  (Go cannot stop `tenancy.Scope{}` from compiling — unexported fields still zero-initialize) taints
  the `Builder` immediately; every statement's `Render`/`Exec`/`Query` surfaces that taint as an
  error instead of emitting SQL.
- Every rendered statement carries the tenant predicate (`WHERE tenant_id = ?` bound to the
  Scope's tenant), and the user predicate too when the table is user-scoped. `Where`,
  `Insert`'s values map, and `Update`'s values map all reject the table's own tenant/user column
  outright — a caller cannot smuggle an override through a "regular" column.
- **No raw-SQL entry point.** No exported function in the package accepts a caller-supplied full
  SQL string. The only place a `sql string` parameter appears is the `Execer`/`Queryer` interface
  the *driver* implements — `sql`/`args` there always flow from this package's own `Render()`
  output into the driver, never the reverse.
- **Driver-agnostic execution boundary**: `Execer.Exec(ctx, sql, args...) (int64, error)` and
  `Queryer.Query(ctx, sql, args...) (Rows, error)`, with a minimal `Rows` cursor
  (`Next`/`Scan`/`Err`/`Close`), shaped like `database/sql`'s most-used subset without importing
  it. Phase 3 implements these against the rqlite HTTP client without this package changing.

## Alternatives considered

### A — Discipline + code review (status quo)

Keep hand-writing `WHERE tenant_id=? AND user_id=?` on every facts-tier query and catch omissions
in review, exactly as `request-lifecycle.md` already describes for Qdrant's filter.
**Rejected**: this is precisely the pattern the project's own thesis rejects for every other layer
("not a coding discipline someone has to remember"). It also does not scale: a reviewer must catch
every future call site, forever, across every contributor and every future refactor — one missed
review is one leaked tenant.

### B — sqlc / ORM codegen

Generate typed query methods from `.sql` files (sqlc) or adopt a full ORM (e.g. `ent`, `gorm`)
with row-level tenant scoping baked into the generated layer.
**Rejected**: sqlc requires a build-time codegen step and a schema source of truth this project
does not have yet (Phase 3 hasn't even chosen its schema); a full ORM is exactly the kind of
"framework magic" [ADR-0002](0002-anti-framework-magic-struct-as-contract.md) already rejected —
hidden query generation, non-obvious N+1 risk, and a much larger dependency surface than the
minimal SELECT/INSERT/UPDATE/DELETE the facts tier actually needs today. Both are heavier than the
problem: this ADR needs one predicate injected everywhere, not a query DSL.

### C — Row-level security (RLS) at the database

Push tenant filtering into the database engine itself (Postgres RLS-style), so even a raw query
without a `WHERE` clause is filtered server-side.
**Rejected, and not available**: rqlite is SQLite-over-Raft; SQLite has no native row-level
security mechanism (no `CREATE POLICY`, no session-scoped predicate injection). This is a
Postgres-specific feature with no SQLite equivalent — there is nothing to adopt.

### D — Trust the pooled-vs-siloed topology to make this moot

Wait for [ADR-0007](0007-multitenancy-fisico-db-unica-vs-por-tenant.md)'s silo tier to cover
enough tenants that the pooled `WHERE`-bug surface stops mattering.
**Rejected**: ADR-0007 explicitly keeps pooled as the *default* for the large majority of tenants
("95% of tenants... at minimal cost"); the silo is the exception, not the norm. The pooled path's
isolation gap is the common case this ADR must close, not an edge case that shrinks on its own.

## Consequences

**Positive**

- **Closes the admitted gap ahead of code, not after an incident.** ADR-0007's "the WHERE bug
  still lurks in the pools" negative consequence and `request-lifecycle.md`'s "silent isolation
  leak" admission are both addressed by construction rather than by reviewer vigilance.
- **Zero new dependencies.** stdlib only — no license review, no supply-chain surface added
  ([license policy](../../.claude/rules/constitutional-gates.md)).
- **Driver-agnostic.** Phase 3's rqlite HTTP client plugs in via `Execer`/`Queryer` without this
  package changing; a future silo connection uses the identical builder.
- **No precedent to unlearn.** Because Phase 3 has zero existing call sites, every future
  facts-tier query starts from `Scope.Query(...)` — there is no legacy raw-SQL pattern to migrate.
- **Reuses, not duplicates, validation.** `memory.ValidateIdentityComponent` is now the single
  source of truth for "what is a valid tenant/user identity string" across both the working-memory
  and facts tiers.

**Negative / technical debt**

- **Not a full query language.** FTS5 `MATCH` composition (the facts tier's actual hot-path query
  shape, per [tiered memory](../architecture/memory-tiers.md)) is not modeled yet — `Where` only
  composes equality predicates. Phase 3 will need to extend the builder (or add a narrow,
  reviewed `MatchClause` method) rather than reach for a raw-SQL escape hatch; that extension is
  future work, not a gap in this ADR's scope.
- **Column/table identifiers are still hardcoded Go, not schema-verified.** `Table` declarations
  are asserted correct by convention (panic on an invalid identifier), not checked against an
  actual rqlite schema — a typo in a column name fails at first `Render()` call in a test, not at
  `go build`. Full compile-time guarantees would require codegen (Alternative B), rejected above
  as disproportionate for the current scope.
- **The zero-value-`Scope` gap is a runtime check, not a true compile error.** Go cannot prevent
  `var s tenancy.Scope` from compiling (unexported fields still zero-initialize). This ADR
  mitigates it by tainting the `Builder` and failing at `Render`/`Exec`/`Query`, covered by tests,
  but it is not literally a compiler error the way an unconstructable type would be.
- **One more package to keep in sync with `internal/memory`'s validation rule.** If
  `reservedKeyChars` or the validation contract in `internal/memory/key.go` changes,
  `internal/tenancy` picks it up automatically (it calls the exported helper), but a reviewer must
  still notice the coupling exists.

**Mandatory going forward**

- Facts-tier **identity** is obtained exclusively through `internal/tenancy` — via
  `facts.NewScope` / `facts.ScopeFromContext`, which delegate to `tenancy.FromContext`. A bare
  `facts.Scope{...}` literal in production code is prohibited (see the reconciliation note below).
- Facts-tier **SQL scoping** is guarded by `TestEverySQLStatementIsTenantScoped`
  (`internal/facts/scope_isolation_test.go`): every statement must carry `tenant_id`/`user_id`
  predicates. This is the compensating control for the statements the builder cannot express.
- **Dedicated-tier tenants (ADR-0007's silo) use the same scoped API with a degenerate scope**: the
  `WHERE tenant_id`/`WHERE user_id` predicates remain present (redundant-but-harmless, exactly as
  ADR-0007 already calls out) even though the silo's physical isolation makes them non-load-bearing
  there. One access path for both topologies, no siloed escape hatch.

## Reconciliation with the facts tier (2026-07-09)

The facts tier (`internal/facts`) shipped alongside this ADR built its own hand-written SQL rather
than routing through the builder, which contradicted the original "Phase 3 MUST access rqlite
exclusively through `internal/tenancy`" mandate. Investigating the contradiction surfaced a real
design limit this ADR had only half-anticipated: **the builder cannot express the facts tier's
core queries.** `searchSQL` is an FTS5 `MATCH` + `bm25()` relevance query with a `JOIN`, `ORDER
BY`, and `LIMIT`; `upsertSQL` is an `INSERT … ON CONFLICT(id) DO UPDATE`. The builder models only
equality-predicate SELECT/INSERT/UPDATE/DELETE. Extending it to cover FTS5/`bm25`/`ON CONFLICT`
would (a) turn the deliberately-minimal foundation into an ORM (Alternative B, already rejected as
disproportionate under [ADR-0002](0002-anti-framework-magic-struct-as-contract.md)) and (b) break
its **driver-agnostic** goal, since `MATCH`/`bm25` are SQLite-specific and meaningless to the
semantic tier's vector store.

**Resolution (a two-part enforcement, not "all SQL through the builder"):**

1. **Identity by construction, through `internal/tenancy`.** `facts.NewScope` / `ScopeFromContext`
   validate tenant/user via `tenancy.FromContext` (the same `memory.ValidateIdentityComponent`
   rule), so the facts tier can no longer drift on what a valid identity is, and every production
   call site (`chat.go`, `chat_stream.go`, `consolidation/sink.go`) funnels identity through the
   tenancy package. This is the concrete "facts uses `internal/tenancy`" the mandate wanted.
2. **SQL scoping by test.** Because the builder cannot own the FTS5 SQL, an isolation test
   (`TestEverySQLStatementIsTenantScoped`) is the compensating control: it fails the build if any
   facts statement stops carrying the `tenant_id`/`user_id` predicate — catching the exact "silent
   isolation leak" the builder would prevent by construction elsewhere.

**Known residual gap (candidate follow-up):** `facts.Scope`'s fields are still exported, so a
bare `facts.Scope{...}` literal remains *compilable* (the constructor is a convention backed by the
mandate above, not a compile-time barrier). Hardening it to `tenancy.Scope`'s unexported-fields
model would make the bypass impossible, at the cost of churning ~20 in-package test call sites; it
is deferred, not silently dropped. The semantic tier (`internal/semantic.Scope`) has the identical
shape and warrants the same reconciliation when it is wired in.

## Open questions

1. **FTS5 `MATCH` support** — how does a `MATCH`-based predicate compose with the existing
   equality-`Where` shape without becoming a raw-SQL escape hatch? Needs its own narrow, typed
   method (e.g. `WhereMatch(column, query string)`) when Phase 3 starts writing real facts-tier
   queries.
2. **Routing to pool vs. silo** — this ADR assumes a single `Execer`/`Queryer` per call; ADR-0007's
   open question 1 (routing mechanism) still determines which physical cluster that driver
   instance talks to. Out of scope here.
3. **Batch/transactional statements** — the sleep cycle's Outbox-drain workers
   ([consistency](../architecture/consistency.md)) will likely need multi-statement transactions;
   `Execer`/`Queryer` as defined are single-statement. Whether that's a new interface or an
   extension of these two is deferred to Phase 3.

## References

- [Multi-tenancy — isolation via ctx](../architecture/multi-tenancy.md) — the isolation claim this
  ADR extends to the facts tier.
- [Request lifecycle — step 3](../architecture/request-lifecycle.md#step-3--parallel-memory-hydration) —
  the "silent isolation leak" admission this ADR closes.
- [ADR-0007 — physical multi-tenancy](0007-multitenancy-fisico-db-unica-vs-por-tenant.md) — the
  pooled/siloed topology this primitive enforces isolation within.
- [ADR-0004 — facts memory: rqlite vs LiteFS](0004-facts-memory-rqlite-vs-litefs.md) — the engine
  the future `Execer`/`Queryer` driver implementation targets.
- [ADR-0002 — anti-framework-magic](0002-anti-framework-magic-struct-as-contract.md) — why an ORM
  (Alternative B) was rejected as disproportionate.
- `internal/memory/key.go` — the working-memory tier's `Key.Validate`/`ValidateIdentityComponent`,
  reused (not duplicated) by `internal/tenancy.FromContext`.
- `internal/tenancy/` — this ADR's implementation: `doc.go` (package doc, full enforcement
  rationale), `scope.go`, `table.go`, `builder.go`, `exec.go`.

---

Cross-links: [ADR-0007 — physical multi-tenancy](0007-multitenancy-fisico-db-unica-vs-por-tenant.md) ·
[ADR-0004 — rqlite vs LiteFS](0004-facts-memory-rqlite-vs-litefs.md) ·
[multi-tenancy — ctx-based isolation](../architecture/multi-tenancy.md) ·
[request lifecycle](../architecture/request-lifecycle.md) ·
[rqlite — facts memory](../components/rqlite-facts-memory.md)
