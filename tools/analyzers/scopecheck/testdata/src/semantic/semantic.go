// Package semantic is a minimal, hermetic stand-in for
// github.com/Luum-Home/aisotropy/internal/semantic, providing just a
// second Scope-shaped type distinct from the facts package's — so
// scopecheck's tests can confirm the type resolution never conflates two
// same-named types from different packages.
package semantic

// Scope mirrors the real semantic.Scope shape.
type Scope struct {
	TenantID string
	UserID   string
}

// NewScope is the (hypothetical, for test purposes) blessed constructor.
func NewScope(tenant, user string) Scope {
	return Scope{TenantID: tenant, UserID: user}
}
