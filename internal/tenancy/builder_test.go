package tenancy

import (
	"context"
	"strings"
	"testing"
)

// mustScope builds a valid Scope for tests, failing the test immediately if
// FromContext ever returns an error for these deliberately valid inputs.
func mustScope(t *testing.T, tenant, user string) Scope {
	t.Helper()
	ctx := WithTenant(context.Background(), tenant)
	if user != "" {
		ctx = WithUser(ctx, user)
	}
	scope, err := FromContext(ctx)
	if err != nil {
		t.Fatalf("FromContext(tenant=%q, user=%q): %v", tenant, user, err)
	}
	return scope
}

// TestRenderedSQLAlwaysCarriesTenantPredicate is acceptance test (a): for
// every verb (SELECT/INSERT/UPDATE/DELETE), against both a tenant-only table
// and a user-scoped table, the rendered SQL always contains the tenant
// predicate bound to the Scope's own tenant value — never a caller-supplied
// or omitted one.
func TestRenderedSQLAlwaysCarriesTenantPredicate(t *testing.T) {
	tenantOnly := NewTable("facts", "tenant_id")
	userScoped := NewTable("tasks", "tenant_id").ScopedByUser("user_id")

	// requireTenantAndUserBound asserts the verb-appropriate shape of the
	// tenant (and, for a user-scoped table, user) predicate: an equality
	// WHERE predicate ("col = ?") for SELECT/UPDATE/DELETE, or a bound
	// column in the INSERT column list, for isInsert=true. In both shapes
	// the scope's actual tenant/user value must be present among the
	// bound args — never omitted, never a caller-suppliable value.
	requireTenantAndUserBound := func(t *testing.T, verb, sqlStr string, args []any, scope Scope, table Table, isInsert bool) {
		t.Helper()
		tenant := scope.Tenant()
		if !argsContain(args, tenant) {
			t.Errorf("%s: rendered args %v do not contain the scope's tenant %q", verb, args, tenant)
		}
		wantTenantShape := "tenant_id = ?"
		if isInsert {
			wantTenantShape = "(tenant_id"
		}
		if !strings.Contains(sqlStr, wantTenantShape) {
			t.Errorf("%s: rendered SQL %q does not contain %q", verb, sqlStr, wantTenantShape)
		}
		if table.userCol == "" {
			return
		}
		user, ok := scope.User()
		if !ok {
			t.Fatalf("%s: test table is user-scoped but scope has no user", verb)
		}
		if !argsContain(args, user) {
			t.Errorf("%s: rendered args %v do not contain the scope's user %q", verb, args, user)
		}
		wantUserShape := "user_id = ?"
		if isInsert {
			wantUserShape = ", user_id"
		}
		if !strings.Contains(sqlStr, wantUserShape) {
			t.Errorf("%s: rendered SQL %q does not contain %q", verb, sqlStr, wantUserShape)
		}
	}

	type renderCase struct {
		name     string
		table    Table
		scope    Scope
		isInsert bool
		render   func(b Builder) (string, []any, error)
	}

	cases := []renderCase{
		{
			name:  "SELECT tenant-only",
			table: tenantOnly,
			scope: mustScope(t, "acme", ""),
			render: func(b Builder) (string, []any, error) {
				return b.Select("body").Render()
			},
		},
		{
			name:  "SELECT user-scoped",
			table: userScoped,
			scope: mustScope(t, "acme", "alice"),
			render: func(b Builder) (string, []any, error) {
				return b.Select("body").Render()
			},
		},
		{
			name:     "INSERT tenant-only",
			table:    tenantOnly,
			scope:    mustScope(t, "acme", ""),
			isInsert: true,
			render: func(b Builder) (string, []any, error) {
				return b.Insert(map[string]any{"body": "hello"}).Render()
			},
		},
		{
			name:     "INSERT user-scoped",
			table:    userScoped,
			scope:    mustScope(t, "acme", "alice"),
			isInsert: true,
			render: func(b Builder) (string, []any, error) {
				return b.Insert(map[string]any{"body": "hello"}).Render()
			},
		},
		{
			name:  "UPDATE tenant-only",
			table: tenantOnly,
			scope: mustScope(t, "acme", ""),
			render: func(b Builder) (string, []any, error) {
				return b.Update(map[string]any{"body": "hello"}).Render()
			},
		},
		{
			name:  "UPDATE user-scoped",
			table: userScoped,
			scope: mustScope(t, "acme", "alice"),
			render: func(b Builder) (string, []any, error) {
				return b.Update(map[string]any{"body": "hello"}).Render()
			},
		},
		{
			name:  "DELETE tenant-only",
			table: tenantOnly,
			scope: mustScope(t, "acme", ""),
			render: func(b Builder) (string, []any, error) {
				return b.Delete().Render()
			},
		},
		{
			name:  "DELETE user-scoped",
			table: userScoped,
			scope: mustScope(t, "acme", "alice"),
			render: func(b Builder) (string, []any, error) {
				return b.Delete().Render()
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sqlStr, args, err := c.render(c.scope.Query(c.table))
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			requireTenantAndUserBound(t, c.name, sqlStr, args, c.scope, c.table, c.isInsert)
		})
	}
}

// argsContain reports whether want appears anywhere in args (order-agnostic
// containment check — arg position varies legitimately by verb: SELECT/
// DELETE put the WHERE predicate first, UPDATE puts SET values first, and
// INSERT puts the scope columns first among the column list, not
// necessarily first among all bound args).
func argsContain(args []any, want any) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// TestUserScopedTableRequiresUserIdentity verifies that a user-scoped table
// accessed from a tenant-only Scope fails at Render — never silently drops
// the user predicate.
func TestUserScopedTableRequiresUserIdentity(t *testing.T) {
	table := NewTable("tasks", "tenant_id").ScopedByUser("user_id")
	scope := mustScope(t, "acme", "")

	if _, _, err := scope.Query(table).Select().Render(); err == nil {
		t.Error("Select().Render() on a user-scoped table with a tenant-only Scope = nil error, want an error")
	}
}

// TestWhereRejectsScopeColumnOverride verifies a caller cannot pass the
// table's own tenant/user column to Where and have it silently coexist with
// (or override) the automatically-bound predicate.
func TestWhereRejectsScopeColumnOverride(t *testing.T) {
	table := NewTable("tasks", "tenant_id").ScopedByUser("user_id")
	scope := mustScope(t, "acme", "alice")

	if _, _, err := scope.Query(table).Select().Where("tenant_id", "other-tenant").Render(); err == nil {
		t.Error("Where(\"tenant_id\", ...).Render() = nil error, want an error")
	}
	if _, _, err := scope.Query(table).Select().Where("user_id", "other-user").Render(); err == nil {
		t.Error("Where(\"user_id\", ...).Render() = nil error, want an error")
	}
}

// TestInsertRejectsScopeColumnInValues verifies a caller cannot smuggle a
// tenant/user override through Insert's values map.
func TestInsertRejectsScopeColumnInValues(t *testing.T) {
	table := NewTable("tasks", "tenant_id").ScopedByUser("user_id")
	scope := mustScope(t, "acme", "alice")

	if _, _, err := scope.Query(table).Insert(map[string]any{"tenant_id": "other-tenant", "body": "x"}).Render(); err == nil {
		t.Error("Insert with tenant_id in values = nil error, want an error")
	}
}

// TestUpdateRejectsScopeColumnAsSetTarget mirrors
// TestInsertRejectsScopeColumnInValues for Update.
func TestUpdateRejectsScopeColumnAsSetTarget(t *testing.T) {
	table := NewTable("tasks", "tenant_id").ScopedByUser("user_id")
	scope := mustScope(t, "acme", "alice")

	if _, _, err := scope.Query(table).Update(map[string]any{"user_id": "other-user"}).Render(); err == nil {
		t.Error("Update with user_id as a SET target = nil error, want an error")
	}
}

// TestSelectDefaultsToStar verifies Select() with no columns renders
// `SELECT *`.
func TestSelectDefaultsToStar(t *testing.T) {
	table := NewTable("facts", "tenant_id")
	scope := mustScope(t, "acme", "")

	sqlStr, _, err := scope.Query(table).Select().Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.HasPrefix(sqlStr, "SELECT * FROM facts") {
		t.Errorf("Render() = %q, want a SELECT * statement", sqlStr)
	}
}

// TestUpdateRequiresAtLeastOneColumn verifies Update([]) fails instead of
// rendering a syntactically invalid `SET ` with nothing after it.
func TestUpdateRequiresAtLeastOneColumn(t *testing.T) {
	table := NewTable("facts", "tenant_id")
	scope := mustScope(t, "acme", "")

	if _, _, err := scope.Query(table).Update(map[string]any{}).Render(); err == nil {
		t.Error("Update({}).Render() = nil error, want an error")
	}
}

// TestInvalidColumnIdentifierRejected verifies Select/Where reject a column
// name that is not a safe SQL identifier, closing off a caller trying to
// smuggle a raw SQL fragment through a "column name".
func TestInvalidColumnIdentifierRejected(t *testing.T) {
	table := NewTable("facts", "tenant_id")
	scope := mustScope(t, "acme", "")

	if _, _, err := scope.Query(table).Select("body; DROP TABLE facts;").Render(); err == nil {
		t.Error("Select with an injected column identifier = nil error, want an error")
	}
	if _, _, err := scope.Query(table).Select().Where("1=1 OR ", "x").Render(); err == nil {
		t.Error("Where with an injected column identifier = nil error, want an error")
	}
}
