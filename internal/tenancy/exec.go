package tenancy

import "context"

// Execer runs a rendered write statement (INSERT/UPDATE/DELETE) and reports
// how many rows it affected. Implemented by whatever driver the caller
// plugs in — the future rqlite HTTP client (ADR-0004/0007) being the
// motivating one — never by this package itself. sql and args always come
// from this package's own Render(): no exported function in this package
// lets a caller hand it arbitrary SQL to execute.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (rowsAffected int64, err error)
}

// Queryer runs a rendered read statement (SELECT) and returns its rows.
// Same contract as Execer: sql/args are always this package's own rendered
// output.
type Queryer interface {
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
}

// Rows is a minimal, driver-agnostic cursor over a Queryer's result set —
// intentionally shaped like database/sql.Rows' most-used subset so a real
// driver can trivially adapt either database/sql or a bespoke HTTP client
// (rqlite speaks SQLite SQL over HTTP, not a database/sql driver) to it,
// without this package importing database/sql or any third-party client.
type Rows interface {
	// Next advances to the next row, returning false when there are no
	// more rows or an error occurred (check Err after the loop).
	Next() bool
	// Scan copies the current row's columns into dest, in Select's
	// requested column order.
	Scan(dest ...any) error
	// Err returns the first error encountered while iterating, if any.
	Err() error
	// Close releases the underlying result set. Safe to call multiple
	// times.
	Close() error
}
