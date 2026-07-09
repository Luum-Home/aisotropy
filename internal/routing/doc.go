// Package routing implements the tenant-routing seam that makes the
// hybrid/tiered physical multi-tenancy model ratified in
// docs/decisions/0007-multitenancy-fisico-db-unica-vs-por-tenant.md real at
// the code level.
//
// # What ADR-0007 decided and what this package is
//
// ADR-0007 resolves how logical isolation (every tenant's data is scoped by
// tenant_id via context.Context, see docs/architecture/multi-tenancy.md) is
// physically materialized in the facts layer (rqlite): most tenants share a
// single, filtered pool (Option A economics) and a minority of large or
// compliance-critical tenants get a dedicated cluster (Option B isolation),
// selected per tenant rather than globally (Option C, "hybrid/tiered" —
// ADR-0007's "Recommended decision").
//
// ADR-0007 explicitly leaves the tenant-to-tier assignment mechanism and the
// tenant-to-resource-name routing as open implementation questions ("Open
// questions" #1 and #3). This package answers the code-level half of that:
// given a tenant identity, which tier does it belong to (Policy), and given
// a logical resource name (a facts table, a Qdrant collection, ...) and a
// tenant, what is the physical resource identifier that tenant's data
// actually lives in (Router)?
//
// # Relationship to the constitutional isolation gate
//
// docs/.claude/rules/constitutional-gates.md's "Multi-tenant data isolation"
// gate requires every read/write to BadgerDB, rqlite, or Qdrant to
// filter/scope by tenant, and for Qdrant specifically requires payload
// filtering during the HNSW scan rather than a post-filter. Router.Resource
// does not replace that filter — a Shared-tier tenant's queries must still
// carry the tenant_id predicate (see internal/tenancy for the facts-tier
// statement builder that enforces exactly that). What Router adds is the
// other half of the hybrid model: choosing which physical collection/table a
// tenant's filtered queries run against in the first place, so a Dedicated
// tenant's isolation is physical (a different resource) on top of, not
// instead of, the logical filter.
//
// # Scope of this package today
//
// This package does not provision real per-tenant rqlite clusters or Qdrant
// collections — that is genuine infrastructure work tracked by ADR-0007's
// open questions (promotion threshold, zero-downtime migration between
// tiers, pool sharding). What it provides now is the stable seam: the facts
// and semantic-memory layers can call Router.Resource to compute the
// collection/table name to use for a tenant, and swap the underlying Policy
// (currently StaticPolicy, a fixed configured set of dedicated tenants) for
// a dynamic one later without any caller needing to change.
package routing
