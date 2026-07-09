package routing

// Policy decides which TenantTier a given tenant belongs to. It is the
// seam ADR-0007's "Open questions" #1 (routing mechanism) and #2 (silo
// promotion threshold) point at: today it is answered by a fixed,
// operator-configured list (StaticPolicy); tomorrow it can be answered by a
// dynamic source (a control-plane table keyed by write volume, a compliance
// flag on the tenant's plan, ...) without Router or its callers changing,
// since they only ever depend on this interface.
type Policy interface {
	// Tier returns the TenantTier the given tenant is assigned to. An empty
	// tenant string has no identity to assign a tier to and must resolve to
	// Shared — the safe default — never Dedicated.
	Tier(tenant string) TenantTier
}

// StaticPolicy is a Policy backed by a fixed, explicitly configured set of
// dedicated tenant IDs. Every tenant not in that set is Shared. This is
// intentionally the simplest possible Policy: ADR-0007 defers the actual
// promotion criteria (write volume, DB size, a compliance flag) to a later
// decision, and a hand-maintained allowlist is the honest implementation of
// "we haven't built that yet" rather than a heuristic that would pretend
// otherwise.
type StaticPolicy struct {
	dedicated map[string]struct{}
}

// NewStaticPolicy builds a StaticPolicy whose Dedicated tier is exactly the
// given tenant IDs. An empty tenant ID in dedicatedTenants is ignored: the
// empty string never identifies a real tenant, and Tier already guarantees
// it resolves to Shared regardless of configuration.
func NewStaticPolicy(dedicatedTenants ...string) StaticPolicy {
	set := make(map[string]struct{}, len(dedicatedTenants))
	for _, tenant := range dedicatedTenants {
		if tenant == "" {
			continue
		}
		set[tenant] = struct{}{}
	}
	return StaticPolicy{dedicated: set}
}

// Tier implements Policy: a tenant configured via NewStaticPolicy is
// Dedicated, every other tenant (including the empty string, and including
// any tenant when the StaticPolicy is the zero value) is Shared.
func (p StaticPolicy) Tier(tenant string) TenantTier {
	if tenant == "" {
		return Shared
	}
	if _, ok := p.dedicated[tenant]; ok {
		return Dedicated
	}
	return Shared
}
