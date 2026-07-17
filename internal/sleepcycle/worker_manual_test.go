//go:build manual

// Live validation against the real Rust DataFusion worker. Behind the `manual`
// build tag, so it is EXCLUDED from `go test ./...` and CI: it needs the worker
// binary built. Build it and run:
//
//	cargo build --release --manifest-path crates/sleep-cycle/Cargo.toml
//	go test -tags manual -run TestWorker -v ./internal/sleepcycle
//
// It feeds synthetic NDJSON exports through the real subprocess and asserts the
// two consolidation passes actually run: DEDUP collapses same-content rows
// (keeping the higher-importance survivor), and the NOISE prune drops
// low-importance rows below the configured floor. Every removed id is surfaced
// in dropped_ids for the Go coordinator to delete.
package sleepcycle

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

const liveBin = "../../crates/sleep-cycle/target/release/sleep-cycle"

func requireLiveBin(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat(liveBin); err != nil {
		t.Skipf("worker binary not built (%s); run cargo build --release first", liveBin)
	}
	return liveBin
}

// TestWorkerLive proves the DEDUP pass on the real engine: two identical
// (tenant,user,content) rows collapse to one, and the SURVIVOR is the
// higher-importance row ("b", 0.9) — so the dropped id is exactly "a".
func TestWorkerLive(t *testing.T) {
	bin := requireLiveBin(t)

	dir := t.TempDir()
	input := filepath.Join(dir, "facts.ndjson")
	output := filepath.Join(dir, "deduped.parquet")

	// Two identical (tenant,user,content) rows + one distinct → dedup drops 1.
	recs := []Record{
		{ID: "a", TenantID: "acme", UserID: "alice", Content: "prefers dark mode", Importance: 0.7, CreatedAt: 1, UpdatedAt: 1},
		{ID: "b", TenantID: "acme", UserID: "alice", Content: "prefers dark mode", Importance: 0.9, CreatedAt: 1, UpdatedAt: 5},
		{ID: "c", TenantID: "acme", UserID: "alice", Content: "region is eu-west-1", Importance: 0.8, CreatedAt: 2, UpdatedAt: 2},
	}
	if err := WriteNDJSON(input, recs); err != nil {
		t.Fatal(err)
	}

	got, err := (Worker{BinaryPath: bin}).Run(context.Background(), input, output)
	if err != nil {
		t.Fatalf("Run (live): %v", err)
	}
	if got.RowsIn != 3 || got.RowsOut != 2 || got.Dropped != 1 || got.NoiseDropped != 0 {
		t.Fatalf("summary = %+v, want {rows_in:3 rows_out:2 dropped:1 noise_dropped:0}", got)
	}
	if len(got.DroppedIDs) != 1 || got.DroppedIDs[0] != "a" {
		t.Fatalf("dropped_ids = %v, want [a] (the lower-importance duplicate)", got.DroppedIDs)
	}
	if fi, err := os.Stat(output); err != nil || fi.Size() == 0 {
		t.Fatalf("expected non-empty output Parquet, err=%v", err)
	}
	t.Logf("live DataFusion dedup OK: 3 -> 2 (dropped a), parquet written")
}

// TestWorkerLiveNoisePrune proves the NOISE pass on the real engine: with a
// --min-importance floor of 0.5, the low-signal row ("n", 0.1) is pruned as
// noise BEFORE dedup — counted in noise_dropped and present in dropped_ids —
// while a genuine duplicate above the floor is still de-duplicated. So of 4 rows
// in, 2 survive, 2 are dropped (1 noise + 1 dedup).
func TestWorkerLiveNoisePrune(t *testing.T) {
	bin := requireLiveBin(t)

	dir := t.TempDir()
	input := filepath.Join(dir, "facts.ndjson")
	output := filepath.Join(dir, "survivors.parquet")

	recs := []Record{
		{ID: "keep", TenantID: "acme", UserID: "alice", Content: "prefers dark mode", Importance: 0.9, CreatedAt: 1, UpdatedAt: 5},
		{ID: "dup", TenantID: "acme", UserID: "alice", Content: "prefers dark mode", Importance: 0.7, CreatedAt: 1, UpdatedAt: 1}, // dedup-dropped
		{ID: "n", TenantID: "acme", UserID: "alice", Content: "thanks", Importance: 0.1, CreatedAt: 2, UpdatedAt: 2},              // noise-dropped
		{ID: "sig", TenantID: "acme", UserID: "alice", Content: "region is eu-west-1", Importance: 0.8, CreatedAt: 3, UpdatedAt: 3},
	}
	if err := WriteNDJSON(input, recs); err != nil {
		t.Fatal(err)
	}

	got, err := (Worker{BinaryPath: bin, MinImportance: 0.5}).Run(context.Background(), input, output)
	if err != nil {
		t.Fatalf("Run (live, noise): %v", err)
	}
	if got.RowsIn != 4 || got.RowsOut != 2 || got.Dropped != 2 || got.NoiseDropped != 1 {
		t.Fatalf("summary = %+v, want {rows_in:4 rows_out:2 dropped:2 noise_dropped:1}", got)
	}
	dropped := append([]string(nil), got.DroppedIDs...)
	sort.Strings(dropped)
	if len(dropped) != 2 || dropped[0] != "dup" || dropped[1] != "n" {
		t.Fatalf("dropped_ids = %v, want [dup n] (one dedup + one noise)", dropped)
	}
	if fi, err := os.Stat(output); err != nil || fi.Size() == 0 {
		t.Fatalf("expected non-empty output Parquet, err=%v", err)
	}
	t.Logf("live DataFusion dedup+noise OK: 4 -> 2 (dropped dup[dedup] + n[noise])")
}

// TestWorkerLiveNoiseFloorDisabledIsDedupOnly proves the default (MinImportance
// unset) is byte-for-byte the historical dedup-only behavior: the low-importance
// row survives because no floor is applied.
func TestWorkerLiveNoiseFloorDisabledIsDedupOnly(t *testing.T) {
	bin := requireLiveBin(t)

	dir := t.TempDir()
	input := filepath.Join(dir, "facts.ndjson")
	output := filepath.Join(dir, "out.parquet")

	recs := []Record{
		{ID: "lowsig", TenantID: "acme", UserID: "alice", Content: "thanks", Importance: 0.1, CreatedAt: 1, UpdatedAt: 1},
		{ID: "keep", TenantID: "acme", UserID: "alice", Content: "region is eu-west-1", Importance: 0.8, CreatedAt: 2, UpdatedAt: 2},
	}
	if err := WriteNDJSON(input, recs); err != nil {
		t.Fatal(err)
	}

	// No MinImportance → no --min-importance flag → dedup-only, nothing dropped.
	got, err := (Worker{BinaryPath: bin}).Run(context.Background(), input, output)
	if err != nil {
		t.Fatalf("Run (live, no floor): %v", err)
	}
	if got.RowsIn != 2 || got.RowsOut != 2 || got.Dropped != 0 || got.NoiseDropped != 0 {
		t.Fatalf("summary = %+v, want {2 2 0 0} (default = dedup-only, low-signal row kept)", got)
	}
}
