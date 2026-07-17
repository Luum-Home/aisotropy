package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, same driver the facts tier uses
)

// sqliteBackup produces a consistent single-file snapshot of a facts DB using
// SQLite's native `VACUUM INTO`. Unlike a naive file copy, VACUUM INTO is safe
// while the source is open in WAL mode: it takes a read transaction and writes a
// fresh, defragmented, self-contained database (no separate -wal/-shm to carry
// along). The output must not already exist — SQLite refuses to overwrite.
func sqliteBackup(ctx context.Context, args []string) error {
	fs := newFlagSet("sqlite backup")
	db := fs.String("db", "", "source facts DB file, e.g. $AISOTROPY_DATA_DIR/facts.db (required)")
	out := fs.String("out", "", "output snapshot file path (required); must not already exist")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" || *out == "" {
		return fmt.Errorf("sqlite backup: --db and --out are required")
	}
	if _, err := os.Stat(*db); err != nil {
		return fmt.Errorf("sqlite backup: source %q not readable: %w", *db, err)
	}
	if _, err := os.Stat(*out); err == nil {
		return fmt.Errorf("sqlite backup: --out %q already exists (VACUUM INTO refuses to overwrite); remove it first", *out)
	}

	// busy_timeout so a concurrent writer (async fact extraction, if the server
	// is up) does not fail the snapshot immediately with SQLITE_BUSY.
	conn, err := sql.Open("sqlite", *db+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("sqlite backup: open %q: %w", *db, err)
	}
	defer conn.Close()

	// VACUUM INTO does not accept bound parameters (it is not DML), so the path
	// is inlined as a single-quoted SQL string literal with quotes doubled.
	stmt := fmt.Sprintf("VACUUM INTO '%s'", strings.ReplaceAll(*out, "'", "''"))
	if _, err := conn.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("sqlite backup: VACUUM INTO %q: %w", *out, err)
	}
	fmt.Fprintf(os.Stderr, "sqlite backup: wrote consistent snapshot %q\n", *out)
	return nil
}

// sqliteRestore validates a snapshot and swaps it into place. It runs
// `PRAGMA integrity_check` on the input before touching the target, then copies
// the file over --db and removes any stale -wal/-shm sidecars so an old WAL can
// never shadow the restored data. The server MUST be stopped during restore
// (an open handle to the old file makes the swap unsafe).
func sqliteRestore(ctx context.Context, args []string) error {
	fs := newFlagSet("sqlite restore")
	in := fs.String("in", "", "input snapshot file (required)")
	db := fs.String("db", "", "target facts DB path to overwrite, e.g. $AISOTROPY_DATA_DIR/facts.db (required)")
	force := fs.Bool("force", false, "overwrite --db if it already exists")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *db == "" {
		return fmt.Errorf("sqlite restore: --in and --db are required")
	}
	if _, err := os.Stat(*in); err != nil {
		return fmt.Errorf("sqlite restore: input %q not readable: %w", *in, err)
	}
	if _, err := os.Stat(*db); err == nil && !*force {
		return fmt.Errorf("sqlite restore: target %q exists; stop the server and pass --force to overwrite", *db)
	}

	if err := sqliteIntegrityCheck(ctx, *in); err != nil {
		return fmt.Errorf("sqlite restore: refusing to install a corrupt snapshot: %w", err)
	}

	// Remove stale WAL/SHM sidecars of the OLD target so the freshly swapped-in
	// file is not partially masked by a leftover write-ahead log.
	for _, side := range []string{*db + "-wal", *db + "-shm"} {
		if err := os.Remove(side); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("sqlite restore: remove stale %q: %w", side, err)
		}
	}
	if err := copyFile(*in, *db); err != nil {
		return fmt.Errorf("sqlite restore: install snapshot: %w", err)
	}
	fmt.Fprintf(os.Stderr, "sqlite restore: installed %q -> %q (integrity-checked)\n", *in, *db)
	return nil
}

// sqliteIntegrityCheck opens path read-only and asserts PRAGMA integrity_check
// returns the single row "ok".
func sqliteIntegrityCheck(ctx context.Context, path string) error {
	conn, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return fmt.Errorf("open %q read-only: %w", path, err)
	}
	defer conn.Close()
	var result string
	if err := conn.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("integrity_check on %q: %w", path, err)
	}
	if result != "ok" {
		return fmt.Errorf("integrity_check on %q returned %q, want \"ok\"", path, result)
	}
	return nil
}

// copyFile copies src to dst, creating dst (0600) and truncating if present.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
