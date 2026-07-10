package routing

// TenantTier identifies which physical routing tier a tenant belongs to,
// per ADR-0007's hybrid/tiered decision (docs/decisions/0007-multitenancy-
// fisico-db-unica-vs-por-tenant.md, "Recommended decision", Option C).
type TenantTier int

const (
	// Shared is the default tier: the tenant's data lives in a pooled
	// resource (an rqlite table, a Qdrant collection, ...) alongside other
	// Shared tenants, isolated logically by a tenant_id filter rather than
	// by physical separation. This is ADR-0007's Option A economics,
	// applied to the majority of tenants.
	Shared TenantTier = iota

	// Dedicated is the tier reserved for large or compliance-critical
	// tenants (ADR-0007's Option B isolation, applied selectively): the
	// tenant gets its own physical resource, so isolation no longer
	// depends solely on a query-level filter.
	Dedicated
)

// Lowercase tier names used in resource identifiers and log output, and
// shared with the test suite so the string form has a single source of
// truth.
const (
	sharedName    = "shared"
	dedicatedName = "dedicated"
)

// String renders the tier as the lowercase name used in resource
// identifiers and log output ("shared", "dedicated"). Any value outside the
// two declared constants (which cannot occur through this package's own
// API, but can through an explicit invalid conversion) renders as "unknown"
// rather than panicking.
func (t TenantTier) String() string {
	switch t {
	case Shared:
		return sharedName
	case Dedicated:
		return dedicatedName
	default:
		return "unknown"
	}
}
