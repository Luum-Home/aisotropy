package main

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgraph-io/badger/v4"
)

// --- parseEncryptionKey ---------------------------------------------------

func TestParseEncryptionKeyUnset(t *testing.T) {
	t.Setenv(dataEncryptionKeyEnv, "")
	key, set, err := parseEncryptionKey()
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if set {
		t.Error("set = true, want false for unset env var")
	}
	if key != nil {
		t.Errorf("key = %v, want nil", key)
	}
}

func TestParseEncryptionKeyHex(t *testing.T) {
	raw := bytes.Repeat([]byte{0xAB}, 32) // AES-256
	t.Setenv(dataEncryptionKeyEnv, hex.EncodeToString(raw))
	key, set, err := parseEncryptionKey()
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !set {
		t.Fatal("set = false, want true")
	}
	if !bytes.Equal(key, raw) {
		t.Errorf("key = %x, want %x", key, raw)
	}
}

func TestParseEncryptionKeyBase64(t *testing.T) {
	// A 32-byte value whose base64 encoding is not also valid hex (base64's
	// alphabet -- including '+', '/', '=' padding -- reliably falls outside
	// hex's 0-9a-f charset), so this exercises the base64 fallback branch.
	raw := []byte("this-is-a-32-byte-secret-key!!!!")
	if len(raw) != 32 {
		t.Fatalf("test fixture bug: len(raw) = %d, want 32", len(raw))
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	if _, err := hex.DecodeString(encoded); err == nil {
		t.Fatalf("test fixture bug: %q decoded as valid hex too, need a base64-only string", encoded)
	}
	t.Setenv(dataEncryptionKeyEnv, encoded)

	key, set, err := parseEncryptionKey()
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !set {
		t.Fatal("set = false, want true")
	}
	if !bytes.Equal(key, raw) {
		t.Errorf("key = %q, want %q", key, raw)
	}
}

func TestParseEncryptionKeyInvalidEncoding(t *testing.T) {
	t.Setenv(dataEncryptionKeyEnv, "not-hex-and-not-base64-!@#$%^&*()")
	_, _, err := parseEncryptionKey()
	if err == nil {
		t.Fatal("err = nil, want error for invalid hex/base64")
	}
	if !strings.Contains(err.Error(), "not valid hex or base64") {
		t.Errorf("err = %q, want it to mention hex/base64", err.Error())
	}
}

func TestParseEncryptionKeyWrongLength(t *testing.T) {
	t.Setenv(dataEncryptionKeyEnv, hex.EncodeToString([]byte("too-short")))
	_, _, err := parseEncryptionKey()
	if err == nil {
		t.Fatal("err = nil, want error for a non-16/24/32 byte key")
	}
	if !strings.Contains(err.Error(), "16, 24, or 32 bytes") {
		t.Errorf("err = %q, want it to mention the allowed byte lengths", err.Error())
	}
}

// --- badgerOpts ------------------------------------------------------------

func TestBadgerOptsUnencrypted(t *testing.T) {
	t.Setenv(dataEncryptionKeyEnv, "")
	opts, err := badgerOpts(t.TempDir())
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(opts.EncryptionKey) != 0 {
		t.Errorf("EncryptionKey = %x, want empty for an unencrypted store", opts.EncryptionKey)
	}
}

func TestBadgerOptsEncrypted(t *testing.T) {
	raw := bytes.Repeat([]byte{0x11}, 32)
	t.Setenv(dataEncryptionKeyEnv, hex.EncodeToString(raw))
	opts, err := badgerOpts(t.TempDir())
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !bytes.Equal(opts.EncryptionKey, raw) {
		t.Errorf("EncryptionKey = %x, want %x", opts.EncryptionKey, raw)
	}
}

func TestBadgerOptsInvalidKeyPropagatesError(t *testing.T) {
	t.Setenv(dataEncryptionKeyEnv, "zz-not-valid")
	if _, err := badgerOpts(t.TempDir()); err == nil {
		t.Fatal("err = nil, want the parseEncryptionKey error to propagate")
	}
}

// --- badgerBackup ------------------------------------------------------------

func TestBadgerBackupMissingDataDir(t *testing.T) {
	t.Setenv("AISOTROPY_DATA_DIR", "")
	err := badgerBackup([]string{"--out", filepath.Join(t.TempDir(), "out.bak")})
	if err == nil || !strings.Contains(err.Error(), "--data-dir") {
		t.Errorf("err = %v, want a --data-dir required error", err)
	}
}

func TestBadgerBackupMissingOut(t *testing.T) {
	err := badgerBackup([]string{"--data-dir", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "--out") {
		t.Errorf("err = %v, want an --out required error", err)
	}
}

func TestBadgerBackupDataDirNotReadable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	err := badgerBackup([]string{"--data-dir", missing, "--out", filepath.Join(t.TempDir(), "out.bak")})
	if err == nil || !strings.Contains(err.Error(), "not a readable directory") {
		t.Errorf("err = %v, want a not-a-readable-directory error", err)
	}
}

func TestBadgerBackupDataDirIsAFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "im-a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := badgerBackup([]string{"--data-dir", file, "--out", filepath.Join(dir, "out.bak")})
	if err == nil || !strings.Contains(err.Error(), "not a readable directory") {
		t.Errorf("err = %v, want a not-a-readable-directory error for a file path", err)
	}
}

func TestBadgerBackupBadEncryptionKeyPropagates(t *testing.T) {
	t.Setenv(dataEncryptionKeyEnv, "not-hex-and-not-base64-!@#$%^&*()")
	err := badgerBackup([]string{"--data-dir", t.TempDir(), "--out", filepath.Join(t.TempDir(), "out.bak")})
	if err == nil {
		t.Fatal("err = nil, want the badgerOpts error to propagate")
	}
}

// TestBadgerBackupOpenFailure forces badger.Open to fail by seeding a
// directory with a MANIFEST file that is not a real Badger manifest, then
// asserts the "open ... read-only" error branch is hit.
func TestBadgerBackupOpenFailure(t *testing.T) {
	t.Setenv(dataEncryptionKeyEnv, "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "MANIFEST"), []byte("garbage-not-a-manifest"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := badgerBackup([]string{"--data-dir", dir, "--out", filepath.Join(t.TempDir(), "out.bak")})
	if err == nil || !strings.Contains(err.Error(), "open") {
		t.Errorf("err = %v, want an open-failure error", err)
	}
}

// TestBadgerBackupStdout exercises the "-" (stdout) output branch by pointing
// --out at "-" and confirming the backup still succeeds against a real store.
func TestBadgerBackupStdout(t *testing.T) {
	t.Setenv(dataEncryptionKeyEnv, "")
	dir := t.TempDir()
	seed, err := badger.Open(badger.DefaultOptions(dir).WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte("k"), []byte("v"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	// Redirect os.Stdout to a pipe so writing "-" doesn't spam the test
	// runner's real stdout, then restore it.
	origStdout := os.Stdout
	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	done := make(chan struct{})
	var captured bytes.Buffer
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := r.Read(buf)
			if n > 0 {
				captured.Write(buf[:n])
			}
			if rerr != nil {
				close(done)
				return
			}
		}
	}()

	err = badgerBackup([]string{"--data-dir", dir, "--out", "-"})
	w.Close()
	<-done
	os.Stdout = origStdout

	if err != nil {
		t.Fatalf("badgerBackup with --out - = %v, want nil", err)
	}
	if captured.Len() == 0 {
		t.Error("nothing was written to stdout, want a non-empty backup stream")
	}
}

// --- badgerRestore -----------------------------------------------------------

func TestBadgerRestoreMissingIn(t *testing.T) {
	err := badgerRestore([]string{"--data-dir", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "--in") {
		t.Errorf("err = %v, want an --in required error", err)
	}
}

func TestBadgerRestoreMissingDataDir(t *testing.T) {
	t.Setenv("AISOTROPY_DATA_DIR", "")
	in := filepath.Join(t.TempDir(), "in.bak")
	if err := os.WriteFile(in, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := badgerRestore([]string{"--in", in})
	if err == nil || !strings.Contains(err.Error(), "--data-dir") {
		t.Errorf("err = %v, want a --data-dir required error", err)
	}
}

func TestBadgerRestoreTargetNotEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "existing"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(t.TempDir(), "in.bak")
	if err := os.WriteFile(in, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := badgerRestore([]string{"--in", in, "--data-dir", dir})
	if err == nil || !strings.Contains(err.Error(), "is not empty") {
		t.Errorf("err = %v, want a not-empty error", err)
	}
}

func TestBadgerRestoreInputNotReadable(t *testing.T) {
	err := badgerRestore([]string{"--in", filepath.Join(t.TempDir(), "does-not-exist"), "--data-dir", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "open") {
		t.Errorf("err = %v, want an open error for a missing input file", err)
	}
}

func TestBadgerRestoreBadEncryptionKeyPropagates(t *testing.T) {
	in := filepath.Join(t.TempDir(), "in.bak")
	if err := os.WriteFile(in, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(dataEncryptionKeyEnv, "not-hex-and-not-base64-!@#$%^&*()")
	err := badgerRestore([]string{"--in", in, "--data-dir", t.TempDir()})
	if err == nil {
		t.Fatal("err = nil, want the badgerOpts error to propagate")
	}
}

// TestBadgerRestoreOpenTargetFailure forces the target badger.Open to fail
// (corrupt MANIFEST in an otherwise-empty-looking directory can't happen --
// ensureEmptyDir requires zero entries -- so instead this points --data-dir
// at a path that exists as a FILE, which os.MkdirAll/Stat plumbing surfaces
// as a non-directory error before Open is ever reached).
func TestBadgerRestoreDataDirIsAFile(t *testing.T) {
	t.Setenv(dataEncryptionKeyEnv, "")
	dir := t.TempDir()
	file := filepath.Join(dir, "im-a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(t.TempDir(), "in.bak")
	if err := os.WriteFile(in, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := badgerRestore([]string{"--in", in, "--data-dir", file})
	if err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Errorf("err = %v, want an is-not-a-directory error", err)
	}
}

// TestBadgerRestoreLoadFailure produces a real backup stream, corrupts its
// protobuf payload (past the fixed-size length header) so it decodes to an
// invalid wire-format message instead of panicking, and asserts db.Load's
// error is surfaced through the "load:" branch.
func TestBadgerRestoreLoadFailure(t *testing.T) {
	t.Setenv(dataEncryptionKeyEnv, "")
	srcDir := t.TempDir()
	seed, err := badger.Open(badger.DefaultOptions(srcDir).WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Update(func(txn *badger.Txn) error {
		for i := 0; i < 50; i++ {
			key := []byte(fmt.Sprintf("tenant/acme/user/%d/summary", i))
			val := bytes.Repeat([]byte("padding-to-make-the-backup-blob-big-enough-to-safely-corrupt "), 4)
			if err := txn.Set(key, val); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := seed.Backup(&buf, 0); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	raw := buf.Bytes()
	if len(raw) < 30 {
		t.Fatalf("test fixture bug: backup too small (%d bytes) to safely corrupt", len(raw))
	}
	corrupt := append([]byte(nil), raw...)
	for i := 8; i < 30; i++ {
		corrupt[i] ^= 0xFF
	}
	in := filepath.Join(t.TempDir(), "corrupt.bak")
	if err := os.WriteFile(in, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	err = badgerRestore([]string{"--in", in, "--data-dir", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "load:") {
		t.Errorf("err = %v, want a load: error for a corrupt backup stream", err)
	}
}

// TestBadgerRestoreStdin exercises the "-" (stdin) input branch.
func TestBadgerRestoreStdin(t *testing.T) {
	t.Setenv(dataEncryptionKeyEnv, "")
	srcDir := t.TempDir()
	seed, err := badger.Open(badger.DefaultOptions(srcDir).WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte("stdin-key"), []byte("stdin-val"))
	}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := seed.Backup(&buf, 0); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	origStdin := os.Stdin
	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	go func() {
		_, _ = w.Write(buf.Bytes())
		w.Close()
	}()

	dstDir := t.TempDir()
	err = badgerRestore([]string{"--in", "-", "--data-dir", dstDir})
	os.Stdin = origStdin
	if err != nil {
		t.Fatalf("badgerRestore with --in - = %v, want nil", err)
	}

	got, err := badger.Open(badger.DefaultOptions(dstDir).WithLogger(nil))
	if err != nil {
		t.Fatalf("open restored: %v", err)
	}
	defer got.Close()
	if err := got.View(func(txn *badger.Txn) error {
		item, gerr := txn.Get([]byte("stdin-key"))
		if gerr != nil {
			return gerr
		}
		return item.Value(func(v []byte) error {
			if string(v) != "stdin-val" {
				t.Fatalf("restored value = %q, want %q", v, "stdin-val")
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("read restored key: %v", err)
	}
}

// --- ensureEmptyDir ----------------------------------------------------------

func TestEnsureEmptyDirCreatesMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh", "nested")
	if err := ensureEmptyDir(dir); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Errorf("dir %q was not created", dir)
	}
}

func TestEnsureEmptyDirAcceptsExistingEmpty(t *testing.T) {
	if err := ensureEmptyDir(t.TempDir()); err != nil {
		t.Errorf("err = %v, want nil for an existing empty directory", err)
	}
}

func TestEnsureEmptyDirRejectsNonEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "entry"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := ensureEmptyDir(dir)
	if err == nil || !strings.Contains(err.Error(), "is not empty") {
		t.Errorf("err = %v, want a not-empty error", err)
	}
}

func TestEnsureEmptyDirRejectsFilePath(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "im-a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := ensureEmptyDir(file)
	if err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Errorf("err = %v, want an is-not-a-directory error", err)
	}
}

// TestEnsureEmptyDirStatErrorNotNotExist covers the os.Stat error branch that
// is NOT os.IsNotExist: a path with a non-directory component in the middle
// (ENOTDIR), which is a distinct failure mode from "does not exist".
func TestEnsureEmptyDirStatErrorNotNotExist(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "im-a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	bogus := filepath.Join(file, "sub") // "im-a-file/sub" -- ENOTDIR on Stat
	err := ensureEmptyDir(bogus)
	if err == nil {
		t.Fatal("err = nil, want an error")
	}
	if os.IsNotExist(err) {
		t.Errorf("err = %v classified as IsNotExist, want the distinct non-IsNotExist Stat error branch", err)
	}
}

func TestEnsureEmptyDirReadDirPermissionDenied(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "restricted")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o311); err != nil { // execute-only, no read
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o750)

	err := ensureEmptyDir(dir)
	if err == nil {
		t.Fatal("err = nil, want a permission-denied error from ReadDir")
	}
}

// --- additional flag/error-path coverage -------------------------------------

func TestBadgerBackupFlagParseError(t *testing.T) {
	err := badgerBackup([]string{"--not-a-real-flag=1"})
	if err == nil {
		t.Fatal("err = nil, want a flag-parse error")
	}
}

func TestBadgerRestoreFlagParseError(t *testing.T) {
	err := badgerRestore([]string{"--not-a-real-flag=1"})
	if err == nil {
		t.Fatal("err = nil, want a flag-parse error")
	}
}

// seedValidBadgerDir opens dir, writes one key, and closes it -- producing an
// on-disk directory a subsequent read-only Open can succeed against.
func seedValidBadgerDir(t *testing.T, dir string) {
	t.Helper()
	db, err := badger.Open(badger.DefaultOptions(dir).WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte("k"), []byte("v"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestBadgerBackupOutCreateFailure points --out at a path whose parent
// directory does not exist, forcing os.Create to fail.
func TestBadgerBackupOutCreateFailure(t *testing.T) {
	t.Setenv(dataEncryptionKeyEnv, "")
	dir := t.TempDir()
	seedValidBadgerDir(t, dir)

	err := badgerBackup([]string{"--data-dir", dir, "--out", filepath.Join(dir, "no-such-subdir", "out.bak")})
	if err == nil || !strings.Contains(err.Error(), "create") {
		t.Errorf("err = %v, want a create-file error", err)
	}
}

// TestBadgerBackupStreamWriteFailure forces db.Backup's write to fail by
// closing the read end of a pipe before the write side is used as stdout, so
// the stream write hits a broken-pipe error.
func TestBadgerBackupStreamWriteFailure(t *testing.T) {
	t.Setenv(dataEncryptionKeyEnv, "")
	dir := t.TempDir()
	seedValidBadgerDir(t, dir)

	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	origStdout := os.Stdout
	os.Stdout = w
	err := badgerBackup([]string{"--data-dir", dir, "--out", "-"})
	os.Stdout = origStdout
	w.Close()

	if err == nil || !strings.Contains(err.Error(), "stream") {
		t.Errorf("err = %v, want a stream-write error", err)
	}
}

// TestBadgerRestoreOpenTargetPermissionDenied makes the (already-empty)
// target directory unwritable so badger.Open fails to create its LOCK file,
// covering the "open target" error branch distinctly from ensureEmptyDir's
// own checks.
func TestBadgerRestoreOpenTargetPermissionDenied(t *testing.T) {
	t.Setenv(dataEncryptionKeyEnv, "")
	parent := t.TempDir()
	dir := filepath.Join(parent, "restricted")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { // read+execute, no write
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o750)

	in := filepath.Join(t.TempDir(), "in.bak")
	if err := os.WriteFile(in, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := badgerRestore([]string{"--in", in, "--data-dir", dir})
	if err == nil || !strings.Contains(err.Error(), "open target") {
		t.Errorf("err = %v, want an open-target error", err)
	}
}
