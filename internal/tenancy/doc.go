// Package tenancy is the tenant-enforced data-access primitive for
// Aisotropy's pooled facts tier (rqlite, see docs/decisions/0011-tenant-scoped-statement-builder.md
// and docs/decisions/0007-multitenancy-fisico-db-unica-vs-por-tenant.md,
// which ratifies "single filtered DB" as the default pooled topology).
//
// # Why this package exists
//
// docs/architecture/multi-tenancy.md and docs/architecture/request-lifecycle.md
// state the pooled facts tier's isolation invariant as "every FTS5 read or
// write query carries the mandatory WHERE tenant_id=? AND user_id=?" — but a
// hand-written invariant is exactly the kind of "coding discipline someone
// has to remember" the project's isolation-via-ctx thesis otherwise avoids.
// This package turns that discipline into an API shape: there is no way to
// obtain a statement builder without first going through a Scope, and there
// is no way to obtain a Scope except by reading tenant/user identity back out
// of context.Context (mirroring the ctx-injection pattern
// internal/api/chat.go and internal/llm/context.go already establish).
// Every statement the builder renders therefore carries the tenant predicate
// (and the user predicate, for user-scoped tables) by construction — it is
// not something a caller can forget, and there is no raw-SQL entry point to
// route around it.
//
// # Construction-time (not review-time) enforcement
//
// The enforcement is API-shaped, verifiable by reading the exported surface
// of this package rather than by code review:
//
//   - Scope's fields are unexported. The only way to obtain a non-empty
//     Scope is FromContext, which re-validates the tenant/user strings via
//     memory.ValidateIdentityComponent — the exact rule
//     internal/memory/key.go already enforces for the working-memory tier,
//     reused here rather than re-implemented.
//   - Builder, SelectStatement, InsertStatement, UpdateStatement, and
//     DeleteStatement all have unexported fields and no exported
//     constructor other than Scope.Query(Table). A caller cannot build a
//     Builder from a zero-value or forged Scope and get a usable statement:
//     Scope.Query taints the Builder with an error the moment the Scope's
//     tenant is empty, and every statement's Render/Exec/Query method
//     surfaces that error instead of emitting SQL.
//   - No function anywhere in this package accepts a caller-supplied raw
//     SQL string. Table registers a table name and its tenant (and,
//     optionally, user) column once; Select/Insert/Update/Delete only ever
//     accept column names and values, and Where only ever composes
//     additional `column = ?` predicates — never a raw fragment.
//   - The Execer/Queryer interfaces are the only place a `sql string`
//     parameter appears in this package's surface, and that string always
//     flows in one direction: from this package's own Render() output into
//     a caller-supplied driver implementation (the future rqlite HTTP
//     client, ADR-0004/0007). No exported function accepts SQL text from a
//     caller and hands it to a driver unscoped.
//
// This is deliberately not an ORM: it covers exactly SELECT/INSERT/UPDATE/
// DELETE with equality WHERE composition, the minimum needed to make an
// unscoped facts-tier query a construction-time impossibility instead of a
// code-review catch, ahead of Phase 3 writing any rqlite-facing SQL.
package tenancy
