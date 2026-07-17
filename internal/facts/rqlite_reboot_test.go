//go:build manual

package facts

import (
	"os"
	"testing"
)

// TestRqliteRebootIdempotent is the regression for the non-idempotent-migration
// bug: rqlite bundles a request's statements into ONE transaction, so the
// canonical schema's `ALTER TABLE facts ADD COLUMN` — expected to error
// "duplicate column name" on every boot after the first — used to abort the
// whole migration at the REQUEST level, where the per-statement duplicate-column
// guard never saw it. Result: the first boot against a networked rqlite
// succeeded, every subsequent boot failed. See rqlite.go's migrate /
// execToleratingDupColumn.
//
// This bug only manifests against a real rqlite (the httptest fake acknowledges
// DDL with an empty result and does not emulate the transaction-abort), so it is
// gated behind `-tags manual` + RQLITE_URL like the other live rqlite checks:
//
//	RQLITE_URL=http://localhost:4001 go test -tags manual -run TestRqliteRebootIdempotent -v ./internal/facts
//
// It boots the store three times against the SAME rqlite; every migrate must
// succeed. Before the fix, boot #2 failed.
func TestRqliteRebootIdempotent(t *testing.T) {
	url := os.Getenv("RQLITE_URL")
	if url == "" {
		t.Skip("RQLITE_URL not set; start an rqlite node and set it to run this live regression")
	}
	for i := 1; i <= 3; i++ {
		st, err := OpenRqlite(url)
		if err != nil {
			t.Fatalf("boot #%d: OpenRqlite (migrate) must be idempotent, got: %v", i, err)
		}
		st.conn.Close()
	}
}
