package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// sentinelInDir writes a marker file INSIDE dir that stands in for BadgerDB's
// own on-disk state, so a test can assert whether isolateWorkingMemoryDataDir
// wiped it or left it alone.
func sentinelInDir(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", p, err)
	}
	return p
}

// TestIsolateWorkingMemoryDataDir_UnsetOrStub is the no-op guard: neither an
// unset AISOTROPY_DATA_DIR nor ARENA_STUB=1 should touch the filesystem at
// all (see the doc comment on isolateWorkingMemoryDataDir for why the stub
// path is a deliberate no-op — clearBackendTierEnv unsets AISOTROPY_DATA_DIR
// itself before any App is built, so isolating it here would be wasted I/O).
func TestIsolateWorkingMemoryDataDir_UnsetOrStub(t *testing.T) {
	t.Run("unset AISOTROPY_DATA_DIR", func(t *testing.T) {
		t.Setenv("AISOTROPY_DATA_DIR", "")
		t.Setenv("ARENA_STUB", "")
		isolateWorkingMemoryDataDir() // must not panic; nothing to assert on disk
	})

	t.Run("ARENA_STUB=1 skips even when AISOTROPY_DATA_DIR is set", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "wm")
		sentinel := sentinelInDir(t, dir, "sentinel.txt", "pre-existing badger-like state")
		t.Setenv("AISOTROPY_DATA_DIR", dir)
		t.Setenv("ARENA_STUB", "1")

		isolateWorkingMemoryDataDir()

		if _, err := os.Stat(sentinel); err != nil {
			t.Errorf("ARENA_STUB=1 run touched the data dir (sentinel gone): %v", err)
		}
		if _, err := os.Stat(dir + dataDirMarkerSuffix); err == nil {
			t.Errorf("ARENA_STUB=1 run wrote a run marker — it must be a complete no-op")
		}
	})
}

// TestIsolateWorkingMemoryDataDir_FreshDirectoryGetsMarked covers the
// first-ever invocation against a path that does not exist yet (run.sh's
// stable default before its first use, or test-crash.sh's fresh mktemp dir
// for a NEW producer run): no wipe is observable (nothing existed to wipe),
// and a marker is created so the NEXT invocation within the TTL window is
// recognized as part of the same run.
func TestIsolateWorkingMemoryDataDir_FreshDirectoryGetsMarked(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wm")
	t.Setenv("AISOTROPY_DATA_DIR", dir)
	t.Setenv("ARENA_STUB", "")

	isolateWorkingMemoryDataDir()

	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("expected data dir to exist after isolation, err=%v", err)
	}
	if _, err := os.Stat(dir + dataDirMarkerSuffix); err != nil {
		t.Fatalf("expected a run marker to be written, err=%v", err)
	}
}

// TestIsolateWorkingMemoryDataDir_FreshMarkerPreservesState is the
// crash_recovery / memory-mode safety net: a SECOND invocation shortly after
// the first (well within dataDirTTL) must NOT wipe state the first
// invocation (a producer, or a persist call) already wrote — this is the
// exact "naive wipe-on-boot breaks crash_recovery" trap the task warned
// about.
func TestIsolateWorkingMemoryDataDir_FreshMarkerPreservesState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wm")
	t.Setenv("AISOTROPY_DATA_DIR", dir)
	t.Setenv("ARENA_STUB", "")

	// Invocation 1 (e.g. the crash_recovery producer, or a memory persist call).
	isolateWorkingMemoryDataDir()
	sentinel := sentinelInDir(t, dir, "badger-manifest-stand-in.txt", "pre-crash state a resumer must recover")

	// Invocation 2 (e.g. the crash_recovery resumer, or the memory recall
	// call) — a fresh process reopening the SAME dir moments later.
	isolateWorkingMemoryDataDir()

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("second invocation within the TTL window wiped state a paired invocation needs to recover: %v", err)
	}
}

// TestIsolateWorkingMemoryDataDir_StaleMarkerWipes is the actual fix under
// test: a marker older than dataDirTTL (an abandoned/previous benchmark run)
// must be wiped before a new run's first invocation reuses run.sh's stable
// path — this is the arena-memory-forensics.md root cause (a stale summary
// from a past run leaking into a new one because nothing ever cleared
// AISOTROPY_DATA_DIR).
func TestIsolateWorkingMemoryDataDir_StaleMarkerWipes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wm")
	t.Setenv("AISOTROPY_DATA_DIR", dir)
	t.Setenv("ARENA_STUB", "")

	sentinel := sentinelInDir(t, dir, "old-run-state.txt", "leftover from a previous benchmark run")
	marker := dir + dataDirMarkerSuffix
	if err := os.WriteFile(marker, []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile(marker): %v", err)
	}
	// Back-date the marker well past dataDirTTL.
	old := time.Now().Add(-2 * dataDirTTL)
	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	isolateWorkingMemoryDataDir()

	if _, err := os.Stat(sentinel); err == nil {
		t.Errorf("stale-marker invocation left the previous run's state in place — cross-run contamination not fixed")
	}
	newInfo, err := os.Stat(marker)
	if err != nil {
		t.Fatalf("expected a fresh marker after the wipe, err=%v", err)
	}
	if time.Since(newInfo.ModTime()) > time.Minute {
		t.Errorf("marker was not refreshed to the current time after the wipe")
	}
}

// TestIsolateWorkingMemoryDataDir_NoMarkerButDataPresentWipes covers a
// directory that predates this mechanism entirely (upgrade case): content on
// disk, no marker at all. Treated the same as "stale" — conservative, since
// there is no way to know how old unmarked state is, and leaving it in place
// is exactly the contamination bug this file exists to close.
func TestIsolateWorkingMemoryDataDir_NoMarkerButDataPresentWipes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wm")
	t.Setenv("AISOTROPY_DATA_DIR", dir)
	t.Setenv("ARENA_STUB", "")

	sentinel := sentinelInDir(t, dir, "pre-mechanism-state.txt", "written before this file existed")

	isolateWorkingMemoryDataDir()

	if _, err := os.Stat(sentinel); err == nil {
		t.Errorf("unmarked pre-existing state was left in place instead of being wiped")
	}
	if _, err := os.Stat(dir + dataDirMarkerSuffix); err != nil {
		t.Errorf("expected a fresh marker to be written after the wipe, err=%v", err)
	}
}
