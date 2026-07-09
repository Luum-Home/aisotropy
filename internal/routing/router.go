package routing

// dedicatedSeparator joins a base resource name with a tenant ID to form a
// Dedicated-tier resource identifier. Underscore is chosen because it is
// valid in both an rqlite/SQLite table name and a Qdrant collection name
// without quoting.
const dedicatedSeparator = "_"

// Router resolves the physical resource identifier a tenant's data lives
// in, given a logical base resource name (an rqlite facts table, a Qdrant
// collection, ...) and the tenant. It is the single seam ADR-0007 requires:
// internal/facts and internal/semantic call Router.Resource instead of
// hardcoding a resource name, so promoting a tenant between tiers, or
// changing how Dedicated resource names are derived, only ever requires
// changing the Policy or this method — never every call site.
//
// Router.Resource decides *which* resource a tenant's queries target. It
// does not replace the tenant_id filter the constitutional isolation gate
// requires on every Shared-tier query (see internal/tenancy for the
// facts-tier statement builder that enforces that filter) — a Dedicated
// tenant's isolation is physical *in addition to*, not instead of, that
// discipline.
type Router struct {
	// Policy decides each tenant's TenantTier. A nil Policy is treated as
	// "every tenant is Shared" (the safe, zero-value default) rather than
	// panicking, so a zero-value Router is usable.
	Policy Policy
}

// NewRouter builds a Router backed by the given Policy.
func NewRouter(policy Policy) Router {
	return Router{Policy: policy}
}

// Resource returns the physical resource identifier for tenant's data
// under the logical resource base.
//
//   - Shared tenants (the default; also the result for an empty tenant, and
//     for a Router with a nil Policy) get base back unchanged: they share
//     the pooled resource, isolated by the caller's own tenant_id filter,
//     not by resource name.
//   - Dedicated tenants get "base_tenant": a resource name distinct from
//     both the shared pool and every other Dedicated tenant's resource.
//
// base is returned unchanged if it is empty — there is no meaningful
// resource name to derive from an empty base regardless of tier.
//
// Injectivity constraint (see the ADR-0007 adversarial review): the
// "base_tenant" encoding is only collision-free when, within a single physical
// namespace (all Qdrant collections, or all rqlite tables), the set of base
// names contains no base that equals another base plus the separator plus a
// valid tenant. In practice each namespace uses ONE fixed base (e.g. the
// "aisotropy_knowledge" collection, the "facts" table), so this holds. Callers
// introducing multiple bases per namespace must keep them free of that prefix
// relationship, and tenant ids are charset-validated at the API boundary
// (ChatInput) so they cannot smuggle the separator or identifier metacharacters.
func (r Router) Resource(base, tenant string) string {
	if base == "" || tenant == "" {
		return base
	}
	if r.Policy == nil {
		return base
	}
	if r.Policy.Tier(tenant) == Dedicated {
		return base + dedicatedSeparator + tenant
	}
	return base
}
