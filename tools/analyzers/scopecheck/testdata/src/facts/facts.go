// Package facts is a minimal, hermetic stand-in for
// github.com/Luum-Home/aisotropy/internal/facts: just enough to exercise
// scopecheck against a type shaped like the real facts.Scope (exported
// fields, a blessed constructor) without depending on the real module.
package facts

// Scope mirrors the real facts.Scope: its fields are exported, so a bare
// composite literal still compiles — exactly the gap scopecheck exists to
// flag outside this package and outside tests.
type Scope struct {
	TenantID string
	UserID   string
}

// NewScope is the blessed constructor. Real callers must use this (or
// ScopeFromContext) instead of a bare Scope{...} literal.
func NewScope(tenant, user string) Scope {
	return Scope{TenantID: tenant, UserID: user}
}

// ScopeFromContext is the blessed constructor for identity already carried
// on a context. Internally it is allowed to build Scope directly: this file
// is part of the defining package.
func ScopeFromContext(tenant, user string) Scope {
	return Scope{TenantID: tenant, UserID: user}
}
