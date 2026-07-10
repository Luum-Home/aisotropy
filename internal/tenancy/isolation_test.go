package tenancy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// fakeRow is one stored row in fakeStore, keyed by the column values it was
// inserted with (including the tenant/user columns this package bound
// automatically).
type fakeRow struct {
	cols   []string
	values []any
}

// fakeStore is a minimal in-memory Execer+Queryer that actually applies the
// WHERE-equality semantics a real SQL engine would, so isolation tests
// exercise real filtering behavior instead of just inspecting rendered SQL
// text. It supports exactly the statement shapes this package renders
// (INSERT ... VALUES (...) and SELECT ... FROM t WHERE col = ? [AND col =
// ?]...), which is sufficient since every statement under test comes from
// this package's own Render().
type fakeStore struct {
	mu    sync.Mutex
	table map[string][]fakeRow // table name -> rows
}

func newFakeStore() *fakeStore {
	return &fakeStore{table: make(map[string][]fakeRow)}
}

// Exec implements Execer for INSERT statements shaped like
// "INSERT INTO name (c1, c2, ...) VALUES (?, ?, ...)".
func (f *fakeStore) Exec(_ context.Context, sqlStr string, args ...any) (int64, error) {
	if !strings.HasPrefix(sqlStr, "INSERT INTO ") {
		return 0, fmt.Errorf("fakeStore: unsupported statement: %s", sqlStr)
	}
	rest := strings.TrimPrefix(sqlStr, "INSERT INTO ")
	tableName := rest[:strings.IndexByte(rest, ' ')]
	open := strings.IndexByte(rest, '(')
	closeParen := strings.IndexByte(rest, ')')
	cols := strings.Split(rest[open+1:closeParen], ", ")

	f.mu.Lock()
	defer f.mu.Unlock()
	f.table[tableName] = append(f.table[tableName], fakeRow{cols: cols, values: args})
	return 1, nil
}

// Query implements Queryer for SELECT statements shaped like
// "SELECT cols FROM name WHERE c1 = ? [AND c2 = ?]...". It matches stored
// rows by requiring every bound WHERE column to equal the corresponding
// arg — the same equality-AND semantics Render produces.
func (f *fakeStore) Query(_ context.Context, sqlStr string, args ...any) (Rows, error) {
	if !strings.HasPrefix(sqlStr, "SELECT ") {
		return nil, fmt.Errorf("fakeStore: unsupported statement: %s", sqlStr)
	}
	fromIdx := strings.Index(sqlStr, " FROM ")
	whereIdx := strings.Index(sqlStr, " WHERE ")
	tableName := strings.TrimSpace(sqlStr[fromIdx+len(" FROM ") : whereIdx])
	wherePart := sqlStr[whereIdx+len(" WHERE "):]
	predicates := strings.Split(wherePart, " AND ")

	f.mu.Lock()
	defer f.mu.Unlock()

	var matched []fakeRow
	for _, row := range f.table[tableName] {
		if rowMatches(row, predicates, args) {
			matched = append(matched, row)
		}
	}
	return &fakeRows{rows: matched, idx: -1}, nil
}

// rowMatches checks whether row satisfies every "col = ?" predicate, using
// args in the same order Render bound them.
func rowMatches(row fakeRow, predicates []string, args []any) bool {
	for i, pred := range predicates {
		col := strings.TrimSuffix(strings.TrimSpace(pred), " = ?")
		want := args[i]
		got, ok := rowValue(row, col)
		if !ok || got != want {
			return false
		}
	}
	return true
}

func rowValue(row fakeRow, col string) (any, bool) {
	for i, c := range row.cols {
		if c == col {
			return row.values[i], true
		}
	}
	return nil, false
}

type fakeRows struct {
	rows []fakeRow
	idx  int
}

func (r *fakeRows) Next() bool {
	r.idx++
	return r.idx < len(r.rows)
}

func (r *fakeRows) Scan(dest ...any) error {
	row := r.rows[r.idx]
	bodyVal, ok := rowValue(row, "body")
	if !ok {
		return errors.New("fakeRows: row has no body column")
	}
	ptr, ok := dest[0].(*string)
	if !ok {
		return errors.New("fakeRows: dest[0] must be *string")
	}
	*ptr = fmt.Sprintf("%v", bodyVal)
	return nil
}

func (r *fakeRows) Err() error   { return nil }
func (r *fakeRows) Close() error { return nil }

// TestTwoScopesNeverSeeEachOthersRows is acceptance test (c): tenant A
// inserts a row, tenant B inserts a row, into the same fakeStore/table.
// Selecting through tenant A's Scope must return only tenant A's row, never
// tenant B's — and vice versa — proving isolation holds through the full
// Scope -> Builder -> Render -> Execer/Queryer path, not just at the SQL
// text level.
func TestTwoScopesNeverSeeEachOthersRows(t *testing.T) {
	table := NewTable("facts", "tenant_id").ScopedByUser("user_id")
	store := newFakeStore()

	scopeA := mustScope(t, "tenant-a", "alice")
	scopeB := mustScope(t, "tenant-b", "alice") // same user id, different tenant

	ctx := context.Background()
	if _, err := scopeA.Query(table).Insert(map[string]any{"body": "A's secret"}).Exec(ctx, store); err != nil {
		t.Fatalf("insert as tenant A: %v", err)
	}
	if _, err := scopeB.Query(table).Insert(map[string]any{"body": "B's secret"}).Exec(ctx, store); err != nil {
		t.Fatalf("insert as tenant B: %v", err)
	}

	gotA := readAllBodies(t, scopeA.Query(table).Select("body"), store)
	if len(gotA) != 1 || gotA[0] != "A's secret" {
		t.Fatalf("tenant A saw %v, want exactly [\"A's secret\"] (isolation leak if B's row is visible)", gotA)
	}

	gotB := readAllBodies(t, scopeB.Query(table).Select("body"), store)
	if len(gotB) != 1 || gotB[0] != "B's secret" {
		t.Fatalf("tenant B saw %v, want exactly [\"B's secret\"] (isolation leak if A's row is visible)", gotB)
	}
}

// TestSameTenantDifferentUsersIsolated is the user-level analog of
// TestTwoScopesNeverSeeEachOthersRows: two users of the SAME tenant must
// not see each other's rows on a user-scoped table.
func TestSameTenantDifferentUsersIsolated(t *testing.T) {
	table := NewTable("facts", "tenant_id").ScopedByUser("user_id")
	store := newFakeStore()

	scopeAlice := mustScope(t, "acme", "alice")
	scopeBob := mustScope(t, "acme", "bob")

	ctx := context.Background()
	if _, err := scopeAlice.Query(table).Insert(map[string]any{"body": "alice's note"}).Exec(ctx, store); err != nil {
		t.Fatalf("insert as alice: %v", err)
	}
	if _, err := scopeBob.Query(table).Insert(map[string]any{"body": "bob's note"}).Exec(ctx, store); err != nil {
		t.Fatalf("insert as bob: %v", err)
	}

	gotAlice := readAllBodies(t, scopeAlice.Query(table).Select("body"), store)
	if len(gotAlice) != 1 || gotAlice[0] != "alice's note" {
		t.Fatalf("alice saw %v, want exactly [\"alice's note\"]", gotAlice)
	}
}

// TestConcurrentTenantsDoNotRace exercises the fakeStore under `go test
// -race` with many goroutines writing/reading different tenants
// concurrently through the same *fakeStore, backing acceptance criterion 3
// (go test -race must pass) with an isolation-relevant concurrent workload,
// not just a single-goroutine happy path.
func TestConcurrentTenantsDoNotRace(t *testing.T) {
	table := NewTable("facts", "tenant_id")
	store := newFakeStore()

	const tenants = 8
	var wg sync.WaitGroup
	for i := range tenants {
		tenant := fmt.Sprintf("tenant-%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			scope := mustScope(t, tenant, "")
			ctx := context.Background()
			if _, err := scope.Query(table).Insert(map[string]any{"body": tenant}).Exec(ctx, store); err != nil {
				t.Errorf("insert for %s: %v", tenant, err)
				return
			}
			got := readAllBodies(t, scope.Query(table).Select("body"), store)
			for _, b := range got {
				if b != tenant {
					t.Errorf("%s saw a foreign row %q", tenant, b)
				}
			}
		}()
	}
	wg.Wait()
}

func readAllBodies(t *testing.T, st *SelectStatement, store *fakeStore) []string {
	t.Helper()
	rows, err := st.Query(context.Background(), store)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		out = append(out, body)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return out
}
