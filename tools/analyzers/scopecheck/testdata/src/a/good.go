package a

import (
	"facts"
	"semantic"
)

// buildGoodFacts routes through the blessed constructor. Not flagged.
func buildGoodFacts(tenant, user string) facts.Scope {
	return facts.NewScope(tenant, user)
}

// buildGoodSemantic routes through the blessed constructor of the second
// fixture type, proving multiple configured target types are each resolved
// and allowed independently. Not flagged.
func buildGoodSemantic(tenant, user string) semantic.Scope {
	return semantic.NewScope(tenant, user)
}
