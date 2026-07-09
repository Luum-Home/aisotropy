package tenancy

import (
	"context"
	"testing"
)

// TestFromContext_MissingTenant verifies (b): a ctx that never had
// WithTenant applied cannot produce a Scope.
func TestFromContext_MissingTenant(t *testing.T) {
	_, err := FromContext(context.Background())
	if err == nil {
		t.Fatal("FromContext(bare ctx) = nil error, want an error")
	}
}

// TestFromContext_EmptyTenantIsANoOp verifies WithTenant("") behaves like
// never calling it — an empty tenant can never be smuggled into ctx as if
// it were valid.
func TestFromContext_EmptyTenantIsANoOp(t *testing.T) {
	ctx := WithTenant(context.Background(), "")
	_, err := FromContext(ctx)
	if err == nil {
		t.Fatal("FromContext(ctx with empty tenant) = nil error, want an error")
	}
}

// TestFromContext_ReservedCharacterRejected verifies (b): a Scope cannot be
// forged with a tenant/user string that would let it forge a collision at
// the storage layer — the same reserved-character rule
// internal/memory/key.go enforces.
func TestFromContext_ReservedCharacterRejected(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(ctx context.Context) context.Context
	}{
		{"tenant with colon", func(ctx context.Context) context.Context {
			return WithTenant(ctx, "acme:evil")
		}},
		{"user with colon", func(ctx context.Context) context.Context {
			ctx = WithTenant(ctx, "acme")
			return WithUser(ctx, "alice:evil")
		}},
		{"tenant with null byte", func(ctx context.Context) context.Context {
			return WithTenant(ctx, "acme\x00")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := c.mutate(context.Background())
			if _, err := FromContext(ctx); err == nil {
				t.Errorf("FromContext(%s) = nil error, want an error", c.name)
			}
		})
	}
}

// TestFromContext_ValidTenantOnly verifies the happy path for a tenant-only
// scope (no user identity set).
func TestFromContext_ValidTenantOnly(t *testing.T) {
	ctx := WithTenant(context.Background(), "acme")
	scope, err := FromContext(ctx)
	if err != nil {
		t.Fatalf("FromContext: %v", err)
	}
	if scope.Tenant() != "acme" {
		t.Errorf("Tenant() = %q, want %q", scope.Tenant(), "acme")
	}
	if _, ok := scope.User(); ok {
		t.Error("User() ok = true, want false (no WithUser was applied)")
	}
}

// TestFromContext_ValidTenantAndUser verifies the happy path for a
// tenant+user scope.
func TestFromContext_ValidTenantAndUser(t *testing.T) {
	ctx := WithTenant(context.Background(), "acme")
	ctx = WithUser(ctx, "alice")
	scope, err := FromContext(ctx)
	if err != nil {
		t.Fatalf("FromContext: %v", err)
	}
	if scope.Tenant() != "acme" {
		t.Errorf("Tenant() = %q, want %q", scope.Tenant(), "acme")
	}
	user, ok := scope.User()
	if !ok || user != "alice" {
		t.Errorf("User() = (%q, %v), want (%q, true)", user, ok, "alice")
	}
}

// TestZeroValueScopeCannotRenderAQuery verifies (d)'s runtime analog: Go
// cannot prevent `var s tenancy.Scope` (or tenancy.Scope{}) from compiling —
// unexported fields still zero-initialize from another package — but the
// package doc states, and this test proves, that doing so never yields a
// usable statement: Scope.Query taints the Builder immediately, and every
// statement's Render surfaces that taint instead of emitting SQL. This is
// the closest a normal test can get to demonstrating the "construction
// without a Scope does not compile" claim for the parts that genuinely
// don't compile (Builder/SelectStatement/etc. have no exported
// constructor at all — there is no code path to even attempt to build one
// without a Scope in the first place); see the package doc for the
// non-compiling half of the claim.
func TestZeroValueScopeCannotRenderAQuery(t *testing.T) {
	var zero Scope // deliberately NOT obtained via FromContext
	table := NewTable("facts", "tenant_id")

	if _, _, err := zero.Query(table).Select().Render(); err == nil {
		t.Error("zero-value Scope Select().Render() = nil error, want an error")
	}
	if _, _, err := zero.Query(table).Insert(map[string]any{"body": "x"}).Render(); err == nil {
		t.Error("zero-value Scope Insert().Render() = nil error, want an error")
	}
	if _, _, err := zero.Query(table).Update(map[string]any{"body": "x"}).Render(); err == nil {
		t.Error("zero-value Scope Update().Render() = nil error, want an error")
	}
	if _, _, err := zero.Query(table).Delete().Render(); err == nil {
		t.Error("zero-value Scope Delete().Render() = nil error, want an error")
	}
}
