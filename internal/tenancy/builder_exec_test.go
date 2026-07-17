package tenancy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// recExecer records the SQL/args it was handed and returns a canned result,
// so the Exec path of every write statement can be verified without a driver.
type recExecer struct {
	sql  string
	args []any
	rows int64
	err  error
}

func (r *recExecer) Exec(_ context.Context, sql string, args ...any) (int64, error) {
	r.sql = sql
	r.args = args
	return r.rows, r.err
}

// recQueryer records the SQL/args and returns a canned Rows/err.
type recQueryer struct {
	sql  string
	args []any
	rows Rows
	err  error
}

func (r *recQueryer) Query(_ context.Context, sql string, args ...any) (Rows, error) {
	r.sql = sql
	r.args = args
	return r.rows, r.err
}

// emptyRows is a trivial Rows: no data, no error.
type emptyRows struct{ closed bool }

func (f *emptyRows) Next() bool        { return false }
func (f *emptyRows) Scan(...any) error { return nil }
func (f *emptyRows) Err() error        { return nil }
func (f *emptyRows) Close() error      { f.closed = true; return nil }

func TestSelectQueryRunsRenderedSQL(t *testing.T) {
	table := NewTable("facts", "tenant_id")
	scope := mustScope(t, "acme", "")
	q := &recQueryer{rows: &emptyRows{}}

	rows, err := scope.Query(table).Select("body").Where("kind", "note").Query(context.Background(), q)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if rows == nil {
		t.Fatal("Query returned nil rows")
	}
	if !strings.Contains(q.sql, "SELECT body FROM facts WHERE tenant_id = ?") {
		t.Fatalf("queryer got SQL %q", q.sql)
	}
	if !argsContain(q.args, "acme") || !argsContain(q.args, "note") {
		t.Fatalf("queryer got args %v, want tenant + where value", q.args)
	}
}

func TestSelectQueryPropagatesRenderError(t *testing.T) {
	table := NewTable("facts", "tenant_id")
	scope := mustScope(t, "acme", "")
	q := &recQueryer{rows: &emptyRows{}}

	// An injected column taints the statement; Query must surface the render
	// error and never call the Queryer.
	_, err := scope.Query(table).Select("bad; DROP").Query(context.Background(), q)
	if err == nil {
		t.Fatal("expected render error to propagate through Query")
	}
	if q.sql != "" {
		t.Fatal("Queryer was called despite a render error")
	}
}

func TestSelectQueryPropagatesQueryerError(t *testing.T) {
	table := NewTable("facts", "tenant_id")
	scope := mustScope(t, "acme", "")
	want := errors.New("connection reset")
	q := &recQueryer{err: want}

	if _, err := scope.Query(table).Select().Query(context.Background(), q); !errors.Is(err, want) {
		t.Fatalf("Query error = %v, want %v", err, want)
	}
}

func TestInsertExecRunsRenderedSQL(t *testing.T) {
	table := NewTable("facts", "tenant_id")
	scope := mustScope(t, "acme", "")
	e := &recExecer{rows: 1}

	n, err := scope.Query(table).Insert(map[string]any{"body": "hello"}).Exec(context.Background(), e)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows affected = %d, want 1", n)
	}
	if !strings.HasPrefix(e.sql, "INSERT INTO facts") {
		t.Fatalf("execer got SQL %q", e.sql)
	}
}

func TestInsertExecPropagatesRenderError(t *testing.T) {
	table := NewTable("facts", "tenant_id")
	scope := mustScope(t, "acme", "")
	e := &recExecer{}

	// tenant_id in the values map taints the statement.
	if _, err := scope.Query(table).Insert(map[string]any{"tenant_id": "other"}).Exec(context.Background(), e); err == nil {
		t.Fatal("expected render error")
	}
	if e.sql != "" {
		t.Fatal("Execer was called despite a render error")
	}
}

func TestUpdateWhereAndExec(t *testing.T) {
	table := NewTable("facts", "tenant_id")
	scope := mustScope(t, "acme", "")
	e := &recExecer{rows: 2}

	n, err := scope.Query(table).
		Update(map[string]any{"body": "new"}).
		Where("kind", "note").
		Exec(context.Background(), e)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if n != 2 {
		t.Fatalf("rows affected = %d, want 2", n)
	}
	if !strings.Contains(e.sql, "UPDATE facts SET body = ? WHERE tenant_id = ? AND kind = ?") {
		t.Fatalf("execer got SQL %q", e.sql)
	}
	// Arg order: SET value, scope tenant, then WHERE value.
	if len(e.args) != 3 || e.args[0] != "new" || e.args[1] != "acme" || e.args[2] != "note" {
		t.Fatalf("execer got args %v, want [new acme note]", e.args)
	}
}

func TestUpdateWhereRejectsInvalidAndScopeColumn(t *testing.T) {
	table := NewTable("tasks", "tenant_id").ScopedByUser("user_id")
	scope := mustScope(t, "acme", "alice")

	if _, _, err := scope.Query(table).Update(map[string]any{"body": "x"}).Where("bad; DROP", "y").Render(); err == nil {
		t.Fatal("Update.Where with an injected identifier = nil error, want error")
	}
	if _, _, err := scope.Query(table).Update(map[string]any{"body": "x"}).Where("user_id", "other").Render(); err == nil {
		t.Fatal("Update.Where on the scope column = nil error, want error")
	}
}

func TestUpdateExecPropagatesRenderError(t *testing.T) {
	table := NewTable("facts", "tenant_id")
	scope := mustScope(t, "acme", "")
	e := &recExecer{}

	// Empty values map => "Update requires at least one column" render error.
	if _, err := scope.Query(table).Update(map[string]any{}).Exec(context.Background(), e); err == nil {
		t.Fatal("expected render error for empty Update")
	}
	if e.sql != "" {
		t.Fatal("Execer was called despite a render error")
	}
}

func TestDeleteWhereAndExec(t *testing.T) {
	table := NewTable("facts", "tenant_id")
	scope := mustScope(t, "acme", "")
	e := &recExecer{rows: 3}

	n, err := scope.Query(table).Delete().Where("kind", "note").Exec(context.Background(), e)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if n != 3 {
		t.Fatalf("rows affected = %d, want 3", n)
	}
	if !strings.Contains(e.sql, "DELETE FROM facts WHERE tenant_id = ? AND kind = ?") {
		t.Fatalf("execer got SQL %q", e.sql)
	}
	if len(e.args) != 2 || e.args[0] != "acme" || e.args[1] != "note" {
		t.Fatalf("execer got args %v, want [acme note]", e.args)
	}
}

func TestDeleteWhereRejectsInvalidAndScopeColumn(t *testing.T) {
	table := NewTable("tasks", "tenant_id").ScopedByUser("user_id")
	scope := mustScope(t, "acme", "alice")

	if _, _, err := scope.Query(table).Delete().Where("bad; DROP", "y").Render(); err == nil {
		t.Fatal("Delete.Where with an injected identifier = nil error, want error")
	}
	if _, _, err := scope.Query(table).Delete().Where("tenant_id", "other").Render(); err == nil {
		t.Fatal("Delete.Where on the scope column = nil error, want error")
	}
}

func TestDeleteExecPropagatesRenderError(t *testing.T) {
	table := NewTable("tasks", "tenant_id").ScopedByUser("user_id")
	// A tenant-only scope against a user-scoped table fails at Render.
	scope := mustScope(t, "acme", "")
	e := &recExecer{}

	if _, err := scope.Query(table).Delete().Exec(context.Background(), e); err == nil {
		t.Fatal("expected render error for user-scoped table with tenant-only scope")
	}
	if e.sql != "" {
		t.Fatal("Execer was called despite a render error")
	}
}

// TestWithUserEmptyIsNoOp covers the empty-string short-circuit of WithUser:
// the returned context carries no user identity.
func TestWithUserEmptyIsNoOp(t *testing.T) {
	ctx := WithUser(context.Background(), "")
	if _, ok := UserFromContext(ctx); ok {
		t.Fatal("WithUser(\"\") should not set a user in context")
	}
}
