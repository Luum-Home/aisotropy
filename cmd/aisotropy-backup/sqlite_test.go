package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// --- sqliteBackup ------------------------------------------------------------

func TestSQLiteBackupFlagParseError(t *testing.T) {
	err := sqliteBackup(context.Background(), []string{"--not-a-real-flag=1"})
	if err == nil {
		t.Fatal("err = nil, want a flag-parse error")
	}
}

func TestSQLiteBackupMissingFlags(t *testing.T) {
	ctx := context.Background()
	cases := [][]string{
		{"--out", "x"},
		{"--db", "x"},
		{},
	}
	for _, args := range cases {
		err := sqliteBackup(ctx, args)
		if err == nil || !strings.Contains(err.Error(), "--db and --out are required") {
			t.Errorf("sqliteBackup(%v) err = %v, want a required-flags error", args, err)
		}
	}
}

func TestSQLiteBackupSourceNotReadable(t *testing.T) {
	ctx := context.Background()
	err := sqliteBackup(ctx, []string{
		"--db", filepath.Join(t.TempDir(), "does-not-exist.db"),
		"--out", filepath.Join(t.TempDir(), "out.db"),
	})
	if err == nil || !strings.Contains(err.Error(), "not readable") {
		t.Errorf("err = %v, want a not-readable error", err)
	}
}

func TestSQLiteBackupOutAlreadyExists(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "facts.db")
	if err := os.WriteFile(dbPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.db")
	if err := os.WriteFile(out, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := sqliteBackup(ctx, []string{"--db", dbPath, "--out", out})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("err = %v, want an already-exists error", err)
	}
}

// TestSQLiteBackupVacuumFailsOnGarbageInput points --db at a file that is not
// a SQLite database at all, forcing VACUUM INTO to fail so the "VACUUM INTO"
// error-wrapping branch is exercised.
func TestSQLiteBackupVacuumFailsOnGarbageInput(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	badDB := filepath.Join(dir, "notasqlitefile.db")
	if err := os.WriteFile(badDB, []byte(strings.Repeat("not a real sqlite file ", 10)), 0o600); err != nil {
		t.Fatal(err)
	}
	err := sqliteBackup(ctx, []string{"--db", badDB, "--out", filepath.Join(dir, "out.db")})
	if err == nil || !strings.Contains(err.Error(), "VACUUM INTO") {
		t.Errorf("err = %v, want a VACUUM INTO error", err)
	}
}

// --- sqliteRestore -----------------------------------------------------------

func TestSQLiteRestoreFlagParseError(t *testing.T) {
	err := sqliteRestore(context.Background(), []string{"--not-a-real-flag=1"})
	if err == nil {
		t.Fatal("err = nil, want a flag-parse error")
	}
}

func TestSQLiteRestoreMissingFlags(t *testing.T) {
	ctx := context.Background()
	cases := [][]string{
		{"--db", "x"},
		{"--in", "x"},
		{},
	}
	for _, args := range cases {
		err := sqliteRestore(ctx, args)
		if err == nil || !strings.Contains(err.Error(), "--in and --db are required") {
			t.Errorf("sqliteRestore(%v) err = %v, want a required-flags error", args, err)
		}
	}
}

func TestSQLiteRestoreInputNotReadable(t *testing.T) {
	ctx := context.Background()
	err := sqliteRestore(ctx, []string{
		"--in", filepath.Join(t.TempDir(), "does-not-exist.db"),
		"--db", filepath.Join(t.TempDir(), "target.db"),
	})
	if err == nil || !strings.Contains(err.Error(), "not readable") {
		t.Errorf("err = %v, want a not-readable error", err)
	}
}

func TestSQLiteRestoreTargetExistsWithoutForce(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	in := filepath.Join(dir, "snap.db")
	if err := os.WriteFile(in, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "facts.db")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := sqliteRestore(ctx, []string{"--in", in, "--db", target})
	if err == nil || !strings.Contains(err.Error(), "pass --force") {
		t.Errorf("err = %v, want a --force-required error", err)
	}
}

func TestSQLiteRestoreRefusesCorruptSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	badSnap := filepath.Join(dir, "corrupt-snap.db")
	if err := os.WriteFile(badSnap, []byte(strings.Repeat("garbage ", 20)), 0o600); err != nil {
		t.Fatal(err)
	}
	err := sqliteRestore(ctx, []string{"--in", badSnap, "--db", filepath.Join(dir, "target.db")})
	if err == nil || !strings.Contains(err.Error(), "refusing to install a corrupt snapshot") {
		t.Errorf("err = %v, want a refusing-corrupt-snapshot error", err)
	}
}

// TestSQLiteRestoreRemovesStaleSidecarsAndForceOverwrites seeds a target DB
// plus stale -wal/-shm sidecars, then restores over it with --force and
// asserts the sidecars are gone and the new content won.
func TestSQLiteRestoreRemovesStaleSidecarsAndForceOverwrites(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Build a valid snapshot to restore FROM.
	snapPath := filepath.Join(dir, "snap.db")
	seed, err := sql.Open("sqlite", snapPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.ExecContext(ctx, `CREATE TABLE facts(id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.ExecContext(ctx, `INSERT INTO facts(body) VALUES('new content')`); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	// Existing target plus stale sidecars.
	target := filepath.Join(dir, "target.db")
	if err := os.WriteFile(target, []byte("old content"), 0o600); err != nil {
		t.Fatal(err)
	}
	walSide := target + "-wal"
	shmSide := target + "-shm"
	if err := os.WriteFile(walSide, []byte("stale wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shmSide, []byte("stale shm"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := sqliteRestore(ctx, []string{"--in", snapPath, "--db", target, "--force"}); err != nil {
		t.Fatalf("sqliteRestore: %v", err)
	}

	if _, err := os.Stat(walSide); !os.IsNotExist(err) {
		t.Errorf("stale -wal sidecar still present (err=%v), want removed", err)
	}
	if _, err := os.Stat(shmSide); !os.IsNotExist(err) {
		t.Errorf("stale -shm sidecar still present (err=%v), want removed", err)
	}

	check, err := sql.Open("sqlite", target+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var body string
	if err := check.QueryRowContext(ctx, `SELECT body FROM facts`).Scan(&body); err != nil {
		t.Fatalf("query restored: %v", err)
	}
	if body != "new content" {
		t.Errorf("restored body = %q, want %q", body, "new content")
	}
}

// buildValidSnapshot creates a minimal-but-real, integrity-check-passing
// SQLite file at path.
func buildValidSnapshot(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	seed, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.ExecContext(ctx, `CREATE TABLE facts(id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.ExecContext(ctx, `INSERT INTO facts(body) VALUES('x')`); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestSQLiteRestoreSidecarRemovalPermissionDenied makes the target's
// containing directory unwritable (but still readable) so unlinking a stale
// -wal sidecar fails with a permission error distinct from IsNotExist.
func TestSQLiteRestoreSidecarRemovalPermissionDenied(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	in := filepath.Join(dir, "snap.db")
	buildValidSnapshot(t, in)

	protected := filepath.Join(dir, "protected")
	if err := os.MkdirAll(protected, 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(protected, "target.db")
	walSide := target + "-wal"
	if err := os.WriteFile(walSide, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(protected, 0o500); err != nil { // read+execute, no write
		t.Fatal(err)
	}
	defer os.Chmod(protected, 0o750)

	err := sqliteRestore(ctx, []string{"--in", in, "--db", target})
	if err == nil || !strings.Contains(err.Error(), "remove stale") {
		t.Errorf("err = %v, want a remove-stale-sidecar error", err)
	}
}

// TestSQLiteRestoreCopyFileDstIsDirectory drives sqliteRestore's final
// install step (copyFile) into failure by pointing --db at an existing
// directory, so os.OpenFile(dst) fails with "is a directory".
func TestSQLiteRestoreCopyFileDstIsDirectory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	in := filepath.Join(dir, "snap.db")
	buildValidSnapshot(t, in)

	dstDir := filepath.Join(dir, "target-is-a-dir")
	if err := os.MkdirAll(dstDir, 0o750); err != nil {
		t.Fatal(err)
	}

	err := sqliteRestore(ctx, []string{"--in", in, "--db", dstDir, "--force"})
	if err == nil || !strings.Contains(err.Error(), "install snapshot") {
		t.Errorf("err = %v, want an install-snapshot error", err)
	}
}

// --- sqliteIntegrityCheck -----------------------------------------------------

func TestSQLiteIntegrityCheckOpenFailure(t *testing.T) {
	// sql.Open with the pure-Go sqlite driver is lazy, so the failure surfaces
	// on the first query (QueryRowContext), not at Open -- exercised here via
	// a path in a directory that does not exist, which the driver rejects when
	// it tries to actually touch the file.
	err := sqliteIntegrityCheck(context.Background(), filepath.Join(t.TempDir(), "no-such-dir", "x.db")+"?mode=ro")
	if err == nil {
		t.Fatal("err = nil, want an error for an unreadable path")
	}
}

func TestSQLiteIntegrityCheckHardFailure(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "corrupt.db")
	seed, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.ExecContext(ctx, `CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.ExecContext(ctx, `INSERT INTO t(v) VALUES('hello')`); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), raw...)
	corrupt[100] ^= 0xFF // flips a byte inside the sqlite_master schema page
	if err := os.WriteFile(dbPath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	err = sqliteIntegrityCheck(ctx, dbPath)
	if err == nil || !strings.Contains(err.Error(), "integrity_check on") {
		t.Errorf("err = %v, want an integrity_check error", err)
	}
}

// TestSQLiteIntegrityCheckNotOkResult corrupts a single byte in a b-tree cell
// offset table (found empirically to keep the page walkable but produce a
// non-"ok" integrity_check report row, as opposed to a hard query error) so
// the `result != "ok"` branch is covered distinctly from the hard-failure
// branch above.
func TestSQLiteIntegrityCheckNotOkResult(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "corrupt.db")
	seed, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.ExecContext(ctx, `CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.ExecContext(ctx, `INSERT INTO t(v) VALUES('hello')`); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 4100 {
		t.Fatalf("test fixture bug: db too small (%d bytes) for the known-good corruption offset", len(raw))
	}
	corrupt := append([]byte(nil), raw...)
	corrupt[4100] ^= 0xFF
	if err := os.WriteFile(dbPath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	err = sqliteIntegrityCheck(ctx, dbPath)
	if err == nil {
		t.Fatal("err = nil, want a non-ok integrity_check error")
	}
	if !strings.Contains(err.Error(), `want "ok"`) {
		t.Errorf("err = %v, want it to be the result != \"ok\" branch specifically (got a hard query error instead)", err)
	}
}

// --- copyFile ------------------------------------------------------------------

func TestCopyFileSuccess(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("hello copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello copy" {
		t.Errorf("dst content = %q, want %q", got, "hello copy")
	}
}

func TestCopyFileSourceMissing(t *testing.T) {
	err := copyFile(filepath.Join(t.TempDir(), "no-such-file"), filepath.Join(t.TempDir(), "dst"))
	if err == nil {
		t.Fatal("err = nil, want error for a missing source file")
	}
}

// TestCopyFileReadFailureMidCopy forces io.Copy to fail by passing a
// directory as src: os.Open succeeds for a directory on Unix, but reading
// from it fails with "is a directory" once io.Copy attempts the read.
func TestCopyFileReadFailureMidCopy(t *testing.T) {
	dir := t.TempDir()
	err := copyFile(dir, filepath.Join(t.TempDir(), "dst"))
	if err == nil {
		t.Fatal("err = nil, want an io.Copy read error for a directory source")
	}
}

func TestCopyFileDestUnwritable(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A destination inside a path component that is actually a file (not a
	// directory) makes OpenFile fail.
	notADir := filepath.Join(dir, "im-a-file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(notADir, "dst")
	err := copyFile(src, dst)
	if err == nil {
		t.Fatal("err = nil, want error for an unwritable destination")
	}
}
