package llm

import (
	"context"
	"testing"
)

// TestWithTenantEmptyIsNoOp proves WithTenant("") returns ctx unchanged (the
// documented no-op), so a caller without a tenant to propagate can call it
// unconditionally without an extra branch, and TenantFromContext correctly
// reports "not set" afterward.
func TestWithTenantEmptyIsNoOp(t *testing.T) {
	base := context.Background()
	got := WithTenant(base, "")
	if got != base {
		t.Fatalf("WithTenant(ctx, \"\") returned a different context; want ctx unchanged")
	}
	if v, ok := TenantFromContext(got); ok || v != "" {
		t.Fatalf("TenantFromContext after empty WithTenant = (%q,%v), want (\"\",false)", v, ok)
	}
}

// TestWithTenantSetAndRead proves the non-empty path stores tenant and
// TenantFromContext reads it back.
func TestWithTenantSetAndRead(t *testing.T) {
	ctx := WithTenant(context.Background(), "acme")
	v, ok := TenantFromContext(ctx)
	if !ok || v != "acme" {
		t.Fatalf("TenantFromContext = (%q,%v), want (\"acme\",true)", v, ok)
	}
}

// TestWithUserEmptyIsNoOp mirrors TestWithTenantEmptyIsNoOp for WithUser.
func TestWithUserEmptyIsNoOp(t *testing.T) {
	base := context.Background()
	got := WithUser(base, "")
	if got != base {
		t.Fatalf("WithUser(ctx, \"\") returned a different context; want ctx unchanged")
	}
	if v, ok := UserFromContext(got); ok || v != "" {
		t.Fatalf("UserFromContext after empty WithUser = (%q,%v), want (\"\",false)", v, ok)
	}
}

// TestWithUserSetAndRead proves the non-empty path stores user and
// UserFromContext reads it back.
func TestWithUserSetAndRead(t *testing.T) {
	ctx := WithUser(context.Background(), "alice")
	v, ok := UserFromContext(ctx)
	if !ok || v != "alice" {
		t.Fatalf("UserFromContext = (%q,%v), want (\"alice\",true)", v, ok)
	}
}

// TestTenantUserFromContextAbsent proves both accessors report "not set" on
// a context.Background() that never had WithTenant/WithUser applied.
func TestTenantUserFromContextAbsent(t *testing.T) {
	ctx := context.Background()
	if v, ok := TenantFromContext(ctx); ok || v != "" {
		t.Fatalf("TenantFromContext on bare ctx = (%q,%v), want (\"\",false)", v, ok)
	}
	if v, ok := UserFromContext(ctx); ok || v != "" {
		t.Fatalf("UserFromContext on bare ctx = (%q,%v), want (\"\",false)", v, ok)
	}
}
