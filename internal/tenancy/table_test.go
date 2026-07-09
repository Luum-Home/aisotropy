package tenancy

import "testing"

// TestNewTablePanicsOnInvalidIdentifier verifies the construction-time
// enforcement for Table: a table/tenant-column name that isn't a safe SQL
// identifier is a programmer error caught immediately, not something that
// can silently produce broken SQL later.
func TestNewTablePanicsOnInvalidIdentifier(t *testing.T) {
	cases := []struct {
		name   string
		fn     func()
		expect string
	}{
		{"empty table name", func() { NewTable("", "tenant_id") }, "invalid table name"},
		{"empty tenant column", func() { NewTable("facts", "") }, "invalid tenant column"},
		{"table name with space", func() { NewTable("facts table", "tenant_id") }, "invalid table name"},
		{"tenant column with quote", func() { NewTable("facts", "tenant_id\"") }, "invalid tenant column"},
		{"table name starting with digit", func() { NewTable("1facts", "tenant_id") }, "invalid table name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("NewTable did not panic, want a panic")
				}
			}()
			c.fn()
		})
	}
}

// TestScopedByUserPanicsOnInvalidIdentifier mirrors
// TestNewTablePanicsOnInvalidIdentifier for the user column.
func TestScopedByUserPanicsOnInvalidIdentifier(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("ScopedByUser did not panic, want a panic")
		}
	}()
	NewTable("facts", "tenant_id").ScopedByUser("user id")
}

// TestNewTableValidIdentifiersDoNotPanic is the inverse check: well-formed
// identifiers must not panic.
func TestNewTableValidIdentifiersDoNotPanic(t *testing.T) {
	tbl := NewTable("facts", "tenant_id").ScopedByUser("user_id")
	if tbl.Name() != "facts" {
		t.Errorf("Name() = %q, want %q", tbl.Name(), "facts")
	}
}
