// Package a is a consumer package (i.e. not the defining package of either
// fixture Scope type) used to exercise scopecheck's diagnostics.
package a

import "facts"

// Scope is a local, unrelated type that also happens to be named Scope. It
// must never be flagged: scopecheck matches by fully-qualified type
// (pkgpath.TypeName) via the type checker, not by identifier name, so this
// package's own Scope{...} literal is never confused with facts.Scope.
type Scope struct {
	TenantID string
	UserID   string
}

// buildLocalScope constructs the local, unrelated Scope type directly. Not
// flagged: it isn't facts.Scope or semantic.Scope.
func buildLocalScope(tenant, user string) Scope {
	return Scope{TenantID: tenant, UserID: user}
}

// buildBadDirect constructs facts.Scope via a bare composite literal from
// outside the defining package — the exact ADR-0011 residual gap.
func buildBadDirect(tenant, user string) facts.Scope {
	return facts.Scope{TenantID: tenant, UserID: user} // want `bare facts\.Scope\{\.\.\.\} composite literal is prohibited outside its defining package and tests; use facts\.NewScope\(\.\.\.\) or facts\.ScopeFromContext\(ctx\) instead \(ADR-0011\)`
}

// buildBadPointer constructs a *facts.Scope via &facts.Scope{...}: the
// composite literal itself (wrapped by the address-of) is still the flagged
// node.
func buildBadPointer(tenant, user string) *facts.Scope {
	return &facts.Scope{TenantID: tenant, UserID: user} // want `bare facts\.Scope\{\.\.\.\} composite literal is prohibited outside its defining package and tests; use facts\.NewScope\(\.\.\.\) or facts\.ScopeFromContext\(ctx\) instead \(ADR-0011\)`
}

// buildBadSlice constructs facts.Scope literals with an elided type inside a
// slice literal — the type checker still resolves the element type, so this
// must be flagged too.
func buildBadSlice(tenant, user string) []facts.Scope {
	return []facts.Scope{
		{TenantID: tenant, UserID: user}, // want `bare facts\.Scope\{\.\.\.\} composite literal is prohibited outside its defining package and tests; use facts\.NewScope\(\.\.\.\) or facts\.ScopeFromContext\(ctx\) instead \(ADR-0011\)`
	}
}
