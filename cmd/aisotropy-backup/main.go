// Command aisotropy-backup is the disaster-recovery tool for the three
// persistent Aisotropy memory tiers. It is deliberately a SEPARATE binary from
// aisotropyd: backup/restore is an operator action, not a request-path concern,
// and it must be runnable against an offline data directory (or a cold copy)
// while the server itself is stopped.
//
// Each tier is backed up with its store's NATIVE mechanism — no bespoke
// serialization that could drift from the on-disk format:
//
//	badger  — BadgerDB working memory (AISOTROPY_DATA_DIR).
//	          backup:  DB.Backup(w, 0) streams a consistent key/value dump.
//	          restore: DB.Load(r) replays it into a fresh directory.
//	sqlite  — Facts tier (embedded modernc.org/sqlite, one <resource>.db file).
//	          backup:  `VACUUM INTO 'out.db'` — an online, consistent, single-file
//	                   snapshot that is safe while the DB is open in WAL mode.
//	          restore: file swap (documented; the tool validates the snapshot).
//	qdrant  — Semantic tier when networked (QDRANT_HOST). Uses the Qdrant REST
//	          snapshot API (create -> download -> upload/recover). The in-process
//	          default index (no QDRANT_HOST) has NO on-disk footprint and is NOT
//	          backed up here — see docs/operations/disaster-recovery.md.
//
// This tool USES the tier stores' native surfaces; it does not import or modify
// the internal tier packages. See docs/operations/backup-restore.md for the
// per-tier procedures and docs/operations/disaster-recovery.md for the runbook,
// RPO/RTO framing, restore order, and the honest DR gaps (no PITR, no
// cross-region replication today).
package main

import (
	"context"
	"fmt"
	"os"
)

// usage is the top-level help text. Kept in one place so `-h`, an unknown
// subcommand, and a bare invocation all print the same thing.
const usage = `aisotropy-backup — disaster-recovery for the Aisotropy memory tiers

Usage:
  aisotropy-backup <tier> <action> [flags]

Tiers and actions:
  badger backup    Stream a BadgerDB working-memory backup to a file.
  badger restore   Load a BadgerDB backup into a (fresh) data directory.
  sqlite backup    VACUUM INTO a consistent single-file snapshot of a facts DB.
  sqlite restore   Validate + copy a facts snapshot into place (file swap).
  qdrant snapshot  Create and download a Qdrant collection snapshot (REST API).
  qdrant restore   Upload/recover a Qdrant collection from a snapshot file.

Run "aisotropy-backup <tier> <action> -h" for the flags of each action.

Notes:
  - BadgerDB holds a directory lock while aisotropyd runs. Back up against a
    STOPPED server or a cold copy of AISOTROPY_DATA_DIR (see the docs).
  - The in-process semantic index (no QDRANT_HOST) is ephemeral and is NOT
    backed up by this tool; it is rebuilt by re-embedding. See the DR runbook.
`

// osExit is a shim over os.Exit so tests can observe the process exit code
// instead of terminating the test binary. Behavior-preserving: production
// wiring is still exactly os.Exit.
var osExit = os.Exit

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "aisotropy-backup: %v\n", err)
		osExit(1)
	}
}

// run dispatches on <tier> <action>. It is separated from main so tests can
// drive the CLI surface directly and assert on returned errors instead of
// process exit codes.
func run(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(usage)
		return nil
	}
	if len(args) < 2 {
		return fmt.Errorf("expected \"<tier> <action>\"; run -h for usage")
	}

	ctx := context.Background()
	tier, action, rest := args[0], args[1], args[2:]

	switch tier {
	case "badger":
		switch action {
		case "backup":
			return badgerBackup(rest)
		case "restore":
			return badgerRestore(rest)
		}
	case "sqlite":
		switch action {
		case "backup":
			return sqliteBackup(ctx, rest)
		case "restore":
			return sqliteRestore(ctx, rest)
		}
	case "qdrant":
		switch action {
		case "snapshot":
			return qdrantSnapshot(ctx, rest)
		case "restore":
			return qdrantRestore(ctx, rest)
		}
	}
	return fmt.Errorf("unknown command %q %q; run -h for usage", tier, action)
}
