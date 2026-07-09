package tenancy

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Builder is the tenant-scoped entry point into SELECT/INSERT/UPDATE/DELETE
// statement construction for one Table. It has no exported constructor: the
// only way to obtain one is Scope.Query (see scope.go). Its fields are
// unexported so a caller cannot assemble one field-by-field and skip the
// Scope that produced it.
type Builder struct {
	scope Scope
	table Table
	// err carries a tainted-at-construction-time error (an invalid/zero
	// Scope, see Scope.valid) through to every statement's Render, so an
	// unscoped Builder can never produce SQL.
	err error
}

// condition is one `column = ?` predicate appended via Where.
type condition struct {
	column string
	value  any
}

// scopeColumns returns, in order, the columns and bound values every
// statement built from b must carry: the tenant predicate always, the user
// predicate only if b.table is user-scoped. Returns an error if the table
// is user-scoped but b.scope carries no user identity — a user-scoped table
// accessed from a tenant-only Scope is exactly the kind of gap this package
// exists to close before it reaches rendered SQL.
func (b Builder) scopeColumns() ([]string, []any, error) {
	if b.err != nil {
		return nil, nil, b.err
	}
	cols := []string{b.table.tenantCol}
	args := []any{b.scope.tenant}
	if b.table.userCol != "" {
		user, ok := b.scope.User()
		if !ok {
			return nil, nil, fmt.Errorf(
				"tenancy: table %q is user-scoped (column %q) but the Scope carries no user identity",
				b.table.name, b.table.userCol,
			)
		}
		cols = append(cols, b.table.userCol)
		args = append(args, user)
	}
	return cols, args, nil
}

// isScopeColumn reports whether col is the tenant or (if set) user column
// this Builder's Table already binds automatically — those must never be
// passed to Where/Select/Insert/Update by a caller, since that would let a
// caller's own value silently override (or duplicate) the scope predicate.
func (b Builder) isScopeColumn(col string) bool {
	return col == b.table.tenantCol || (b.table.userCol != "" && col == b.table.userCol)
}

// Select starts a SELECT statement. columns lists which columns to select;
// empty means `SELECT *`.
func (b Builder) Select(columns ...string) *SelectStatement {
	st := &SelectStatement{builder: b, columns: columns, err: b.err}
	for _, c := range columns {
		st.setErr(validateColumnName(c))
	}
	return st
}

// Insert starts an INSERT statement. values maps column name to bound
// value; the tenant (and, if applicable, user) column/value pair is added
// automatically and must not appear in values.
func (b Builder) Insert(values map[string]any) *InsertStatement {
	st := &InsertStatement{builder: b, values: values, err: b.err}
	for col := range values {
		st.setErr(validateColumnName(col))
		if b.isScopeColumn(col) {
			st.setErr(fmt.Errorf(
				"tenancy: column %q is the tenant/user scope column and is bound automatically; do not pass it to Insert",
				col,
			))
		}
	}
	return st
}

// Update starts an UPDATE statement. values maps column name to its new
// bound value; the tenant/user columns must not appear in values (they are
// used in the WHERE clause, never as SET targets, to prevent a write from
// re-parenting a row into another tenant).
func (b Builder) Update(values map[string]any) *UpdateStatement {
	st := &UpdateStatement{builder: b, values: values, err: b.err}
	for col := range values {
		st.setErr(validateColumnName(col))
		if b.isScopeColumn(col) {
			st.setErr(fmt.Errorf(
				"tenancy: column %q is the tenant/user scope column and cannot be a SET target",
				col,
			))
		}
	}
	return st
}

// Delete starts a DELETE statement.
func (b Builder) Delete() *DeleteStatement {
	return &DeleteStatement{builder: b, err: b.err}
}

// validateColumnName is the shared identifier check every Where/Select
// column and Insert/Update map key goes through.
func validateColumnName(col string) error {
	if !isValidIdentifier(col) {
		return fmt.Errorf("tenancy: invalid column identifier %q", col)
	}
	return nil
}

// --- SELECT ---------------------------------------------------------------

// SelectStatement builds a tenant-scoped SELECT. Obtain one via
// Builder.Select (which itself is only reachable via Scope.Query).
type SelectStatement struct {
	builder Builder
	columns []string
	wheres  []condition
	err     error
}

func (s *SelectStatement) setErr(err error) {
	if s.err == nil {
		s.err = err
	}
}

// Where adds an additional `column = value` predicate, ANDed with the
// mandatory tenant/user predicate. Rejects column names equal to the
// table's own tenant/user column — those are already bound and cannot be
// overridden.
func (s *SelectStatement) Where(column string, value any) *SelectStatement {
	if err := validateColumnName(column); err != nil {
		s.setErr(err)
		return s
	}
	if s.builder.isScopeColumn(column) {
		s.setErr(fmt.Errorf("tenancy: column %q is the tenant/user scope column and is already bound; do not pass it to Where", column))
		return s
	}
	s.wheres = append(s.wheres, condition{column: column, value: value})
	return s
}

// Render produces the final (sql, args) pair, or the first error
// encountered while building the statement (including a tainted/unscoped
// Builder — see Scope.valid).
func (s *SelectStatement) Render() (string, []any, error) {
	if s.err != nil {
		return "", nil, s.err
	}
	scopeCols, args, err := s.builder.scopeColumns()
	if err != nil {
		return "", nil, err
	}

	cols := "*"
	if len(s.columns) > 0 {
		cols = strings.Join(s.columns, ", ")
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "SELECT %s FROM %s WHERE ", cols, s.builder.table.name)
	sb.WriteString(whereClause(scopeCols))
	for _, c := range s.wheres {
		sb.WriteString(" AND ")
		sb.WriteString(c.column)
		sb.WriteString(" = ?")
		args = append(args, c.value)
	}
	return sb.String(), args, nil
}

// Query renders the statement and runs it against q.
func (s *SelectStatement) Query(ctx context.Context, q Queryer) (Rows, error) {
	sqlStr, args, err := s.Render()
	if err != nil {
		return nil, err
	}
	return q.Query(ctx, sqlStr, args...)
}

// --- INSERT -----------------------------------------------------------

// InsertStatement builds a tenant-scoped INSERT. Obtain one via
// Builder.Insert.
type InsertStatement struct {
	builder Builder
	values  map[string]any
	err     error
}

func (s *InsertStatement) setErr(err error) {
	if s.err == nil {
		s.err = err
	}
}

// Render produces the final (sql, args) pair, or the first error
// encountered while building the statement.
func (s *InsertStatement) Render() (string, []any, error) {
	if s.err != nil {
		return "", nil, s.err
	}
	cols, args, err := s.builder.scopeColumns()
	if err != nil {
		return "", nil, err
	}

	keys := sortedKeys(s.values)
	for _, k := range keys {
		cols = append(cols, k)
		args = append(args, s.values[k])
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")
	sqlStr := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", s.builder.table.name, strings.Join(cols, ", "), placeholders)
	return sqlStr, args, nil
}

// Exec renders the statement and runs it against e.
func (s *InsertStatement) Exec(ctx context.Context, e Execer) (int64, error) {
	sqlStr, args, err := s.Render()
	if err != nil {
		return 0, err
	}
	return e.Exec(ctx, sqlStr, args...)
}

// --- UPDATE -----------------------------------------------------------

// UpdateStatement builds a tenant-scoped UPDATE. Obtain one via
// Builder.Update.
type UpdateStatement struct {
	builder Builder
	values  map[string]any
	wheres  []condition
	err     error
}

func (s *UpdateStatement) setErr(err error) {
	if s.err == nil {
		s.err = err
	}
}

// Where adds an additional `column = value` predicate, ANDed with the
// mandatory tenant/user predicate. Same rules as SelectStatement.Where.
func (s *UpdateStatement) Where(column string, value any) *UpdateStatement {
	if err := validateColumnName(column); err != nil {
		s.setErr(err)
		return s
	}
	if s.builder.isScopeColumn(column) {
		s.setErr(fmt.Errorf("tenancy: column %q is the tenant/user scope column and is already bound; do not pass it to Where", column))
		return s
	}
	s.wheres = append(s.wheres, condition{column: column, value: value})
	return s
}

// Render produces the final (sql, args) pair, or the first error
// encountered while building the statement.
func (s *UpdateStatement) Render() (string, []any, error) {
	if s.err != nil {
		return "", nil, s.err
	}
	scopeCols, scopeArgs, err := s.builder.scopeColumns()
	if err != nil {
		return "", nil, err
	}
	if len(s.values) == 0 {
		return "", nil, fmt.Errorf("tenancy: Update requires at least one column to set")
	}

	keys := sortedKeys(s.values)
	setParts := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys)+len(scopeArgs)+len(s.wheres))
	for _, k := range keys {
		setParts = append(setParts, k+" = ?")
		args = append(args, s.values[k])
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "UPDATE %s SET %s WHERE ", s.builder.table.name, strings.Join(setParts, ", "))
	sb.WriteString(whereClause(scopeCols))
	args = append(args, scopeArgs...)
	for _, c := range s.wheres {
		sb.WriteString(" AND ")
		sb.WriteString(c.column)
		sb.WriteString(" = ?")
		args = append(args, c.value)
	}
	return sb.String(), args, nil
}

// Exec renders the statement and runs it against e.
func (s *UpdateStatement) Exec(ctx context.Context, e Execer) (int64, error) {
	sqlStr, args, err := s.Render()
	if err != nil {
		return 0, err
	}
	return e.Exec(ctx, sqlStr, args...)
}

// --- DELETE -----------------------------------------------------------

// DeleteStatement builds a tenant-scoped DELETE. Obtain one via
// Builder.Delete.
type DeleteStatement struct {
	builder Builder
	wheres  []condition
	err     error
}

func (s *DeleteStatement) setErr(err error) {
	if s.err == nil {
		s.err = err
	}
}

// Where adds an additional `column = value` predicate, ANDed with the
// mandatory tenant/user predicate. Same rules as SelectStatement.Where.
func (s *DeleteStatement) Where(column string, value any) *DeleteStatement {
	if err := validateColumnName(column); err != nil {
		s.setErr(err)
		return s
	}
	if s.builder.isScopeColumn(column) {
		s.setErr(fmt.Errorf("tenancy: column %q is the tenant/user scope column and is already bound; do not pass it to Where", column))
		return s
	}
	s.wheres = append(s.wheres, condition{column: column, value: value})
	return s
}

// Render produces the final (sql, args) pair, or the first error
// encountered while building the statement.
func (s *DeleteStatement) Render() (string, []any, error) {
	if s.err != nil {
		return "", nil, s.err
	}
	scopeCols, args, err := s.builder.scopeColumns()
	if err != nil {
		return "", nil, err
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "DELETE FROM %s WHERE ", s.builder.table.name)
	sb.WriteString(whereClause(scopeCols))
	for _, c := range s.wheres {
		sb.WriteString(" AND ")
		sb.WriteString(c.column)
		sb.WriteString(" = ?")
		args = append(args, c.value)
	}
	return sb.String(), args, nil
}

// Exec renders the statement and runs it against e.
func (s *DeleteStatement) Exec(ctx context.Context, e Execer) (int64, error) {
	sqlStr, args, err := s.Render()
	if err != nil {
		return 0, err
	}
	return e.Exec(ctx, sqlStr, args...)
}

// --- shared helpers ---------------------------------------------------

// whereClause joins scope columns into "col1 = ? AND col2 = ? ...".
func whereClause(cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = c + " = ?"
	}
	return strings.Join(parts, " AND ")
}

// sortedKeys returns m's keys in sorted order, so rendered SQL (and its
// bound args) is deterministic across runs instead of depending on Go's
// randomized map iteration order.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
