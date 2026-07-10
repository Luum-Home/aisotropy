package a

import "facts"

// scopeForTest builds facts.Scope directly, from a _test.go file outside the
// defining package. Not flagged: ADR-0011 explicitly carves tests out —
// tests legitimately build scopes directly instead of threading a validated
// context through.
func scopeForTest() facts.Scope {
	return facts.Scope{TenantID: "acme", UserID: "alice"}
}
