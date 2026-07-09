package tenancy

import "fmt"

// Table declares a SQL table this package is allowed to build statements
// against: its name, which column holds the tenant identity (mandatory),
// and, optionally, which column holds the user identity. Registering the
// tenant column here — once, at table-declaration time — is what lets every
// statement built against this Table automatically carry
// `WHERE {tenantCol} = ?` without any call site having to remember to add
// it (see the package doc).
//
// Table values are immutable and cheap to declare as package-level vars,
// mirroring how a real caller would register the pooled facts tier's tables
// once (e.g. `var factsTable = tenancy.NewTable("facts", "tenant_id").ScopedByUser("user_id")`).
type Table struct {
	name      string
	tenantCol string
	userCol   string // empty => table is tenant-scoped only, not user-scoped
}

// NewTable declares a tenant-scoped table. It panics if name or
// tenantColumn is not a safe SQL identifier — this is a programmer error at
// table-registration time (a hardcoded, code-reviewed constant), not
// something that can be reached from request input, so failing loudly and
// immediately (construction time) is preferable to threading an error
// through every subsequent call.
func NewTable(name, tenantColumn string) Table {
	if !isValidIdentifier(name) {
		panic(fmt.Sprintf("tenancy: invalid table name %q", name))
	}
	if !isValidIdentifier(tenantColumn) {
		panic(fmt.Sprintf("tenancy: invalid tenant column %q", tenantColumn))
	}
	return Table{name: name, tenantCol: tenantColumn}
}

// ScopedByUser returns a copy of t that also requires a user predicate on
// every statement: `WHERE {tenantCol} = ? AND {userColumn} = ?`. Use this
// for facts-tier tables keyed per-user (e.g. tasks, preferences); omit it
// for tables that are tenant-wide only. Panics on an invalid identifier, for
// the same reason as NewTable.
func (t Table) ScopedByUser(userColumn string) Table {
	if !isValidIdentifier(userColumn) {
		panic(fmt.Sprintf("tenancy: invalid user column %q", userColumn))
	}
	t.userCol = userColumn
	return t
}

// Name returns the table's SQL name.
func (t Table) Name() string { return t.name }

// isValidIdentifier reports whether s is safe to interpolate directly into
// rendered SQL as a table/column name: a leading letter or underscore,
// followed by letters, digits, or underscores. Table/column names come from
// hardcoded Go source (Table declarations, map keys in Insert/Update, column
// names in Select/Where), never from request input, but validating them
// anyway is cheap defense in depth against a typo or a future call site that
// forwards a less-trusted string.
func isValidIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
