// datadir_isolation.go closes the F1 cross-run contamination defect found by
// the forensic BadgerDB investigation (docs/architecture/arena-memory-forensics.md):
// run.sh pins AISOTROPY_DATA_DIR to a STABLE path (${SCRIPT_DIR}/.aisotropy-data)
// that nothing ever clears, so a stale summary or a refused-turn record from a
// PAST benchmark run can silently leak into a later run that happens to reuse
// the same (deterministic) session_id — arenas/live/runner/tasks.go's
// buildMemory names memory sessions "arena-mem-<framework>-<task id>", the
// SAME string on every run, precisely so the two invocations of ONE task agree
// on it. That determinism is also exactly why a naive per-session-hash data
// dir would NOT fix cross-run contamination: hashing session_id would just
// move the identical bug to a different path.
//
// What this DOES need to preserve, deliberately: `memory` mode's persist->
// recall pair and `crash_recovery` mode's producer->resumer pair (see
// mode_crash.go) BOTH depend on two SEPARATE OS processes reopening the SAME
// on-disk BadgerDB store and finding each other's state — wiping on every
// process boot would break both by construction. See mode_crash.go's
// runCrashResumer doc comment for why crash_recovery's proof of durability
// specifically requires the second process to see the first process's writes.
//
// Note test-crash.sh (crash_recovery's actual harness, see dimensions.go) does
// NOT go through this path in practice: it exports its OWN private
// AISOTROPY_DATA_DIR — a fresh `mktemp -d` — before either of its two
// processes starts, bypassing run.sh's stable default entirely. So
// crash_recovery was already correctly isolated per scenario run; this file's
// job is exclusively the STABLE path run.sh hands every OTHER mode (including
// `memory`, and including crash_recovery IF it were ever driven through
// run.sh directly instead of test-crash.sh). The TTL mechanism below is inert
// on test-crash.sh's path: a brand-new empty mktemp dir with no marker is
// simply treated as "fresh" (nothing to wipe) on the producer's first boot,
// exactly like any other never-before-seen directory.
package main

import (
	"os"
	"time"
)

// dataDirMarkerSuffix names the sentinel file this package writes ALONGSIDE
// (not inside) AISOTROPY_DATA_DIR — deliberately outside the directory Badger
// itself manages, so it can never be mistaken for one of Badger's own files
// (MANIFEST/*.sst/*.vlog/LOCK) and BadgerDB's Open never has to see it.
const dataDirMarkerSuffix = ".arena-run-marker"

// dataDirTTL bounds how long a data dir is treated as "still part of the same
// run" with no fresh invocation touching it. It must comfortably exceed:
//   - the gap between a `memory` task's persist and recall invocations
//     (normally seconds — two sequential subprocess spawns from the same
//     runner goroutine, see runner/tasks.go's buildMemory doc comment), and
//   - a rate-limit backoff retry gap (runner/main.go's adaptive limiter can
//     back off for a while under sustained 429s).
//
// ...while staying much shorter than the gap between two independent
// benchmark runs (typically re-invoked manually, hours or days apart). 30
// minutes is a deliberately generous, round number satisfying both; it is not
// tuned against a measured worst-case backoff duration.
const dataDirTTL = 30 * time.Minute

// isolateWorkingMemoryDataDir isolates AISOTROPY_DATA_DIR across DIFFERENT
// arena runs while preserving it across the SAME run's related process
// invocations (see the package doc comment above for the two cases this must
// not break). Called once per process, at the top of dispatch(), before any
// mode constructs an App (which is what actually opens BadgerDB at dataDir).
//
// Mechanism: a marker file "<AISOTROPY_DATA_DIR><dataDirMarkerSuffix>" (a
// SIBLING of the data dir, not a file inside it) records the wall time of the
// last invocation that touched this data dir.
//
//   - AISOTROPY_DATA_DIR unset -> no-op; working memory is in-memory
//     (newWorkingMemory in aisotropy/app.go), nothing on disk to isolate.
//   - ARENA_STUB=1 -> no-op; the stub path (newAgnesClient in
//     agnes_client.go) unsets AISOTROPY_DATA_DIR itself before any App is
//     built (clearBackendTierEnv, app.go), so a stub run never touches this
//     directory regardless — isolating it here would be pure wasted disk I/O.
//   - marker absent (a directory nothing here has ever marked — either
//     brand-new, or left over from a run that predates this mechanism) ->
//     WIPE the directory, then write a fresh marker. This is the actual fix:
//     an old run's leftover state, unmarked, is always cleared before a new
//     run's first invocation reuses the stable path.
//   - marker present and younger than dataDirTTL -> LEAVE the directory
//     alone (this invocation is part of the same run/session window as
//     whatever last touched it) and refresh the marker's timestamp, so the
//     window keeps extending as long as invocations keep arriving.
//   - marker present but older than dataDirTTL -> treat as a stale/abandoned
//     run and WIPE, same as the absent case.
//
// Best-effort throughout: a filesystem error here is logged and swallowed —
// worst case the isolation is skipped for this invocation and aisotropy's own
// runtime boot surfaces any real problem with the directory on its own.
func isolateWorkingMemoryDataDir() {
	dir := os.Getenv("AISOTROPY_DATA_DIR")
	if dir == "" {
		return
	}
	if stubMode() {
		return
	}

	marker := dir + dataDirMarkerSuffix
	info, statErr := os.Stat(marker)
	fresh := statErr == nil && time.Since(info.ModTime()) < dataDirTTL

	if !fresh {
		if err := os.RemoveAll(dir); err != nil {
			logf("data-dir isolation: could not wipe stale %s (%v) — leaving it as-is; a leftover run's state may leak in", dir, err)
		} else {
			logf("data-dir isolation: wiped %s (no run marker within %s — treating as a new/abandoned run)", dir, dataDirTTL)
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		logf("data-dir isolation: could not create %s (%v) — aisotropy.New will surface this itself", dir, err)
		return
	}
	// Touch the marker (create it fresh, or refresh its mtime) so this
	// invocation extends the "same run" window for whatever process comes
	// next on the same session/task.
	if err := os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339Nano)+"\n"), 0o644); err != nil {
		logf("data-dir isolation: could not write run marker %s (%v)", marker, err)
	}
}
