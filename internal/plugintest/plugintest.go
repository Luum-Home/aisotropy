// Package plugintest is the public CONFORMANCE SUITE for Aisotropy's Tier-2
// open-core backends. A community author who implements a tenant-unaware driver
// — semantic.VectorBackend or facts.DocBackend — runs their implementation
// against these suites to prove it satisfies the contract the sealed scoping
// decorators (semantic.NewVectorBackendStore / facts.NewDocBackendStore) rely
// on.
//
// The design intent is "bring your own backend, NOT your isolation": a
// community backend never sees a tenant, and the decorator derives+enforces the
// per-(tenant,user) namespace on every call. The CENTRAL test in each suite
// proves exactly that seal — a marker written for tenant A through the decorator
// is invisible to tenant B through the decorator, while the RAW (unwrapped)
// backend still physically holds it. That contrast is the whole point: the
// decorator, not the backend, is what makes isolation hold, so a buggy or
// hostile backend cannot leak across tenants.
//
// Usage from a community backend's own test file:
//
//	func TestMyVectorBackend(t *testing.T) {
//	    plugintest.RunVectorBackendSuite(t, func() semantic.VectorBackend {
//	        return NewMyVectorBackend()
//	    })
//	}
//
//	func TestMyDocBackend(t *testing.T) {
//	    plugintest.RunDocBackendSuite(t, func() facts.DocBackend {
//	        return NewMyDocBackend()
//	    })
//	}
//
// The package also ships in-memory reference backends (RefVectorBackend,
// RefDocBackend) so an author has a correct, minimal implementation to read and
// so the suites themselves are exercised in CI.
package plugintest

// SuiteDim is the embedding dimension the vector conformance suite uses. A
// factory-produced VectorBackend need not know it — the decorator enforces it —
// but the suite builds its probe vectors at this width.
const SuiteDim = 8
