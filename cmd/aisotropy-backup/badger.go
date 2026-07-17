package main

import (
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/dgraph-io/badger/v4"
)

// dataEncryptionKeyEnv mirrors aisotropy.dataEncryptionKeyEnv (that constant is
// unexported and lives in a package this tool must not modify, so the name and
// hex-or-base64 / 16-24-32-byte contract are reproduced here). A backup of an
// encrypted store MUST supply the same key, and the restore target may re-apply
// it. See aisotropy/encryption.go for the authoritative parser.
const dataEncryptionKeyEnv = "AISOTROPY_DATA_ENCRYPTION_KEY"

// loadPendingWrites is the DB.Load concurrency used on restore. BadgerDB's own
// tooling defaults to 256; the working-memory dataset is tiny, so this is a
// throughput knob, not a correctness one.
const loadPendingWrites = 256

// parseEncryptionKey decodes AISOTROPY_DATA_ENCRYPTION_KEY (hex first, then
// standard base64) into AES key material, requiring 16/24/32 bytes. It returns
// (nil,false,nil) when unset so the caller opens an unencrypted store. The raw
// value is never echoed into an error — it is secret material.
func parseEncryptionKey() (key []byte, set bool, err error) {
	raw := strings.TrimSpace(os.Getenv(dataEncryptionKeyEnv))
	if raw == "" {
		return nil, false, nil
	}
	var decoded []byte
	if b, herr := hex.DecodeString(raw); herr == nil {
		decoded = b
	} else if b, berr := base64.StdEncoding.DecodeString(raw); berr == nil {
		decoded = b
	} else {
		return nil, false, fmt.Errorf("%s is not valid hex or base64", dataEncryptionKeyEnv)
	}
	switch len(decoded) {
	case 16, 24, 32:
		return decoded, true, nil
	default:
		return nil, false, fmt.Errorf("%s must decode to 16, 24, or 32 bytes (AES-128/192/256), got %d", dataEncryptionKeyEnv, len(decoded))
	}
}

// badgerOpts builds Options for the working-memory directory. Encryption
// requires a non-zero index cache (BadgerDB enforces this), so one is always
// configured. The value-log/table tuning in internal/memory is irrelevant to
// Backup/Load — those operate on the logical key/value stream, not the physical
// layout — so defaults are fine here.
func badgerOpts(dir string) (badger.Options, error) {
	opts := badger.DefaultOptions(dir).
		WithLogger(nil).
		WithIndexCacheSize(16 << 20)
	key, encrypted, err := parseEncryptionKey()
	if err != nil {
		return badger.Options{}, err
	}
	if encrypted {
		opts = opts.WithEncryptionKey(key)
	}
	return opts, nil
}

// badgerBackup opens AISOTROPY_DATA_DIR and streams a native BadgerDB backup to
// a file. It opens READ-ONLY by default so it can never mutate the source. The
// server holds a directory lock while running, so this must target a stopped
// server or a cold copy of the directory; --bypass-lock is offered ONLY for the
// cold-copy case and is dangerous against a live directory (it can read torn
// state). See docs/operations/backup-restore.md.
func badgerBackup(args []string) error {
	fs := flag.NewFlagSet("badger backup", flag.ContinueOnError)
	dir := fs.String("data-dir", os.Getenv("AISOTROPY_DATA_DIR"), "BadgerDB working-memory directory (default $AISOTROPY_DATA_DIR)")
	out := fs.String("out", "", "output backup file path (required); use - for stdout")
	bypassLock := fs.Bool("bypass-lock", false, "skip the directory lock — ONLY safe on an offline/copied directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("badger backup: --data-dir (or $AISOTROPY_DATA_DIR) is required")
	}
	if *out == "" {
		return fmt.Errorf("badger backup: --out is required")
	}
	if info, err := os.Stat(*dir); err != nil || !info.IsDir() {
		return fmt.Errorf("badger backup: --data-dir %q is not a readable directory: %w", *dir, err)
	}

	opts, err := badgerOpts(*dir)
	if err != nil {
		return err
	}
	opts = opts.WithReadOnly(true).WithBypassLockGuard(*bypassLock)

	db, err := badger.Open(opts)
	if err != nil {
		return fmt.Errorf("badger backup: open %q read-only (is aisotropyd still running? stop it or back up a cold copy): %w", *dir, err)
	}
	defer db.Close()

	w := os.Stdout
	if *out != "-" {
		f, ferr := os.Create(*out)
		if ferr != nil {
			return fmt.Errorf("badger backup: create %q: %w", *out, ferr)
		}
		defer f.Close()
		w = f
	}

	// since=0 => a full backup of every version kept. WorkingMemory keeps a
	// single version per key, so this is the whole store.
	ts, err := db.Backup(w, 0)
	if err != nil {
		return fmt.Errorf("badger backup: stream: %w", err)
	}
	fmt.Fprintf(os.Stderr, "badger backup: wrote %q (source version watermark %d)\n", *out, ts)
	return nil
}

// badgerRestore loads a native BadgerDB backup into a data directory. The
// target directory MUST be empty (or non-existent) — Load replays into a fresh
// store, and refusing to write over existing data prevents a half-merged store.
// If the backup came from an encrypted source, set AISOTROPY_DATA_ENCRYPTION_KEY
// so the restored store is written back encrypted.
func badgerRestore(args []string) error {
	fs := flag.NewFlagSet("badger restore", flag.ContinueOnError)
	in := fs.String("in", "", "input backup file path (required); use - for stdin")
	dir := fs.String("data-dir", os.Getenv("AISOTROPY_DATA_DIR"), "target BadgerDB directory (default $AISOTROPY_DATA_DIR); must be empty")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		return fmt.Errorf("badger restore: --in is required")
	}
	if *dir == "" {
		return fmt.Errorf("badger restore: --data-dir (or $AISOTROPY_DATA_DIR) is required")
	}
	if err := ensureEmptyDir(*dir); err != nil {
		return fmt.Errorf("badger restore: %w", err)
	}

	r := os.Stdin
	if *in != "-" {
		f, ferr := os.Open(*in)
		if ferr != nil {
			return fmt.Errorf("badger restore: open %q: %w", *in, ferr)
		}
		defer f.Close()
		r = f
	}

	opts, err := badgerOpts(*dir)
	if err != nil {
		return err
	}
	db, err := badger.Open(opts)
	if err != nil {
		return fmt.Errorf("badger restore: open target %q (is aisotropyd running against it? stop it first): %w", *dir, err)
	}
	defer db.Close()

	if err := db.Load(r, loadPendingWrites); err != nil {
		return fmt.Errorf("badger restore: load: %w", err)
	}
	fmt.Fprintf(os.Stderr, "badger restore: loaded %q into %q\n", *in, *dir)
	return nil
}

// ensureEmptyDir makes dir exist and confirms it holds no entries, so a restore
// never silently merges into a populated store.
func ensureEmptyDir(dir string) error {
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		return os.MkdirAll(dir, 0o750)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%q exists and is not a directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("target %q is not empty (%d entries); restore into a fresh directory to avoid a half-merged store", dir, len(entries))
	}
	return nil
}
