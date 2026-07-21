package facts

import (
	"context"
	"testing"
)

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

// TestRqlite_DoubleMigrateSucceeds pins migrate()'s idempotency directly and
// literally: calling it a SECOND time on an already-open store — exactly what
// a re-boot against an already-migrated rqlite does — must succeed. This is
// the regression the docs/architecture/arena-memory-forensics.md §3.3
// investigation asked for: arenas/live/aisotropy/rqlite_reset.go's original
// justifying comment claimed this was still broken, but commit 64531e0
// ("fix(facts): idempotent rqlite migration...", 2026-07-17 20:18:14 -0300)
// had already fixed it 57 minutes before that comment was written (commit
// 4bf2c80, 21:15:47 -0300). Together with
// TestRqlite_MigrateToleratesDuplicateColumn (which reproduces the harder,
// real-rqlite request-level-abort error shape) this pins the guarantee in the
// default (non-manual) test lane, so the bug this file describes can never
// again be silently reintroduced. The live, real-rqlite equivalent is
// TestRqliteRebootIdempotent (-tags manual, rqlite_reboot_test.go).
func TestRqlite_DoubleMigrateSucceeds(t *testing.T) {
	f := newFakeRqlite(t)
	st, err := OpenRqlite(f.url())
	if err != nil {
		t.Fatalf("first OpenRqlite (initial migrate): %v", err)
	}
	defer st.conn.Close()

	if err := st.migrate(context.Background()); err != nil {
		t.Fatalf("second migrate() call on an already-migrated store must succeed, got: %v", err)
	}
	if err := st.migrate(context.Background()); err != nil {
		t.Fatalf("third migrate() call must also succeed, got: %v", err)
	}
}
