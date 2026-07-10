package routing

import "testing"

func TestTenantTierString(t *testing.T) {
	cases := []struct {
		name string
		tier TenantTier
		want string
	}{
		{sharedName, Shared, sharedName},
		{dedicatedName, Dedicated, dedicatedName},
		{"invalid", TenantTier(99), "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tier.String(); got != tc.want {
				t.Errorf("TenantTier(%d).String() = %q, want %q", tc.tier, got, tc.want)
			}
		})
	}
}

func TestStaticPolicyTier(t *testing.T) {
	policy := NewStaticPolicy("tenant-large", "tenant-regulated")

	cases := []struct {
		name   string
		tenant string
		want   TenantTier
	}{
		{"configured dedicated tenant", "tenant-large", Dedicated},
		{"another configured dedicated tenant", "tenant-regulated", Dedicated},
		{"unconfigured tenant defaults to shared", "tenant-smb", Shared},
		{"empty tenant is always shared", "", Shared},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := policy.Tier(tc.tenant); got != tc.want {
				t.Errorf("StaticPolicy.Tier(%q) = %v, want %v", tc.tenant, got, tc.want)
			}
		})
	}
}

func TestStaticPolicyIgnoresEmptyConfiguredTenant(t *testing.T) {
	// An empty string slipped into the dedicated set (e.g. from an
	// upstream config bug) must never let Tier("") return Dedicated.
	policy := NewStaticPolicy("", "tenant-large")

	if got := policy.Tier(""); got != Shared {
		t.Errorf("StaticPolicy.Tier(\"\") = %v, want %v even when \"\" was passed to NewStaticPolicy", got, Shared)
	}
	if got := policy.Tier("tenant-large"); got != Dedicated {
		t.Errorf("StaticPolicy.Tier(%q) = %v, want %v", "tenant-large", got, Dedicated)
	}
}

func TestStaticPolicyZeroValue(t *testing.T) {
	var policy StaticPolicy // zero value: no NewStaticPolicy call

	if got := policy.Tier("any-tenant"); got != Shared {
		t.Errorf("zero-value StaticPolicy.Tier(%q) = %v, want %v", "any-tenant", got, Shared)
	}
}

func TestRouterResource(t *testing.T) {
	policy := NewStaticPolicy("tenant-large")
	router := NewRouter(policy)

	cases := []struct {
		name   string
		base   string
		tenant string
		want   string
	}{
		{"shared tenant keeps base name", "facts", "tenant-smb", "facts"},
		{"dedicated tenant gets suffixed name", "facts", "tenant-large", "facts_tenant-large"},
		{"different base, same dedicated tenant", "embeddings", "tenant-large", "embeddings_tenant-large"},
		{"empty tenant keeps base name", "facts", "", "facts"},
		{"empty base stays empty regardless of tier", "", "tenant-large", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := router.Resource(tc.base, tc.tenant); got != tc.want {
				t.Errorf("Router.Resource(%q, %q) = %q, want %q", tc.base, tc.tenant, got, tc.want)
			}
		})
	}
}

func TestRouterResourceNilPolicy(t *testing.T) {
	// A zero-value Router (nil Policy) must resolve every tenant as Shared
	// rather than panicking — it is the safe default for a Router built
	// without an explicit Policy.
	var router Router

	if got := router.Resource("facts", "tenant-large"); got != "facts" {
		t.Errorf("Router{}.Resource(%q, %q) = %q, want %q (nil Policy defaults to Shared)", "facts", "tenant-large", got, "facts")
	}
}

func TestRouterResourceEmptyTenantIsSharedEvenIfConfiguredDedicated(t *testing.T) {
	// Defense in depth: even if a Policy implementation were misconfigured
	// to treat "" as Dedicated, Router.Resource must still refuse to derive
	// a resource name from an empty tenant, since "base_" is not a
	// meaningful physical resource identifier.
	router := NewRouter(alwaysDedicated{})

	if got := router.Resource("facts", ""); got != "facts" {
		t.Errorf("Router.Resource(%q, \"\") = %q, want %q", "facts", got, "facts")
	}
}

// alwaysDedicated is a test-only Policy that reports every tenant,
// including the empty string, as Dedicated — used to prove Router.Resource
// still guards the empty-tenant edge case independently of the Policy.
type alwaysDedicated struct{}

func (alwaysDedicated) Tier(string) TenantTier { return Dedicated }
