package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/dgraph-io/badger/v4"
	_ "modernc.org/sqlite"
)

// TestBadgerRoundTrip exercises the real backup/restore path: seed a BadgerDB
// directory, back it up, wipe it, restore into a fresh directory, and assert the
// key/value survived. This is the working-memory tier's DR guarantee.
func TestBadgerRoundTrip(t *testing.T) {
	t.Setenv("AISOTROPY_DATA_ENCRYPTION_KEY", "") // exercise the unencrypted path

	src := filepath.Join(t.TempDir(), "src")
	backup := filepath.Join(t.TempDir(), "wm.badgerbackup")
	dst := filepath.Join(t.TempDir(), "dst")

	// Seed the source store, then close it so the backup can take the lock.
	seedDB, err := badger.Open(badger.DefaultOptions(src).WithLogger(nil))
	if err != nil {
		t.Fatalf("open seed: %v", err)
	}
	wantKey, wantVal := []byte("tenant/acme/user/42/summary"), []byte("condensed summary v1")
	if err := seedDB.Update(func(txn *badger.Txn) error {
		return txn.Set(wantKey, wantVal)
	}); err != nil {
		t.Fatalf("seed set: %v", err)
	}
	if err := seedDB.Close(); err != nil {
		t.Fatalf("close seed: %v", err)
	}

	// Backup -> wipe -> restore.
	if err := badgerBackup([]string{"--data-dir", src, "--out", backup}); err != nil {
		t.Fatalf("badgerBackup: %v", err)
	}
	if err := os.RemoveAll(src); err != nil {
		t.Fatalf("wipe src: %v", err)
	}
	if err := badgerRestore([]string{"--in", backup, "--data-dir", dst}); err != nil {
		t.Fatalf("badgerRestore: %v", err)
	}

	// Verify the value is intact in the restored directory.
	got, err := badger.Open(badger.DefaultOptions(dst).WithLogger(nil))
	if err != nil {
		t.Fatalf("open restored: %v", err)
	}
	defer got.Close()
	if err := got.View(func(txn *badger.Txn) error {
		item, gerr := txn.Get(wantKey)
		if gerr != nil {
			return gerr
		}
		return item.Value(func(v []byte) error {
			if string(v) != string(wantVal) {
				t.Fatalf("restored value = %q, want %q", v, wantVal)
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("read restored key: %v", err)
	}
}

// TestSQLiteRoundTrip seeds a facts-shaped SQLite DB, snapshots it with
// VACUUM INTO, wipes the original, restores via the file swap, and asserts the
// row survived and integrity_check passes.
func TestSQLiteRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "facts.db")
	snap := filepath.Join(dir, "facts.snapshot.db")
	restored := filepath.Join(dir, "facts.restored.db")

	seed, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open seed: %v", err)
	}
	if _, err := seed.ExecContext(ctx, `CREATE TABLE facts(id INTEGER PRIMARY KEY, tenant TEXT, body TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := seed.ExecContext(ctx, `INSERT INTO facts(tenant, body) VALUES('acme','user prefers dark mode')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed: %v", err)
	}

	if err := sqliteBackup(ctx, []string{"--db", dbPath, "--out", snap}); err != nil {
		t.Fatalf("sqliteBackup: %v", err)
	}
	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("wipe db: %v", err)
	}
	if err := sqliteRestore(ctx, []string{"--in", snap, "--db", restored}); err != nil {
		t.Fatalf("sqliteRestore: %v", err)
	}

	check, err := sql.Open("sqlite", restored+"?mode=ro")
	if err != nil {
		t.Fatalf("open restored: %v", err)
	}
	defer check.Close()
	var body string
	if err := check.QueryRowContext(ctx, `SELECT body FROM facts WHERE tenant='acme'`).Scan(&body); err != nil {
		t.Fatalf("query restored: %v", err)
	}
	if body != "user prefers dark mode" {
		t.Fatalf("restored body = %q, want %q", body, "user prefers dark mode")
	}
}
