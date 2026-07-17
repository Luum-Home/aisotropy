package facts

import "testing"

// TestRqlite_MigrateToleratesDuplicateColumn is the default-lane (fake-backed)
// regression for the non-idempotent-migration bug (see rqlite.go migrate /
// execToleratingDupColumn). On a re-boot, a real rqlite aborts the request's
// transaction on the idempotent `ALTER TABLE facts ADD COLUMN`'s expected
// "duplicate column name" error and surfaces it at the REQUEST level, not
// per-statement. The store must tolerate that and still open successfully. The
// httptest fake reproduces that exact shape via ddlError. (The real transaction
// semantics are also covered by the live TestRqliteRebootIdempotent, -tags manual.)
func TestRqlite_MigrateToleratesDuplicateColumn(t *testing.T) {
	f := newFakeRqlite(t)
	f.ddlError = `near "fts_content": duplicate column name: fts_content`
	st, err := OpenRqlite(f.url())
	if err != nil {
		t.Fatalf("migrate must tolerate a request-level duplicate-column error on re-boot, got: %v", err)
	}
	st.conn.Close()
}

// TestRqlite_MigratePropagatesNonDuplicateDDLError proves the tolerance is
// narrow — it is NOT a blanket swallow of migration failures. A request-level
// DDL error that is NOT a duplicate-column error must still fail the open. (This
// also confirms the fake's request-level error path is actually surfaced by the
// driver, so the sibling tolerance test above is not passing vacuously.)
func TestRqlite_MigratePropagatesNonDuplicateDDLError(t *testing.T) {
	f := newFakeRqlite(t)
	f.ddlError = "no such table: facts"
	if _, err := OpenRqlite(f.url()); err == nil {
		t.Fatal("migrate must propagate a non-duplicate-column DDL error, got nil")
	}
}
