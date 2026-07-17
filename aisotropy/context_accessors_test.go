package aisotropy

import (
	"context"
	"testing"
)

// TestIdentityFromContext_AbsentIsNotFound verifies the read-only identity
// accessors' documented ("", false) contract on a context that carries no
// runtime-injected identity — the only branch reachable without the private
// request pipeline (the runtime derives identity from the validated path; a
// consumer can never construct it). The true branch is exercised end-to-end by
// the internal/api middleware tests that populate the carrier the runtime uses.
func TestIdentityFromContext_AbsentIsNotFound(t *testing.T) {
	ctx := context.Background()

	if got, ok := TenantFromContext(ctx); ok || got != "" {
		t.Errorf("TenantFromContext(bare ctx) = (%q, %v), want (\"\", false)", got, ok)
	}
	if got, ok := UserFromContext(ctx); ok || got != "" {
		t.Errorf("UserFromContext(bare ctx) = (%q, %v), want (\"\", false)", got, ok)
	}
}
