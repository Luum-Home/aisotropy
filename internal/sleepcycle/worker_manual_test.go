//go:build manual

// Live validation against the real Rust DataFusion worker. Behind the `manual`
// build tag, so it is EXCLUDED from `go test ./...` and CI: it needs the worker
// binary built. Build it and run:
//
//	cargo build --release --manifest-path crates/sleep-cycle/Cargo.toml
//	go test -tags manual -run TestWorkerLive -v ./internal/sleepcycle
//
// It feeds an NDJSON export with a duplicate fact through the real subprocess
// and asserts DataFusion deduplicated it and wrote the output Parquet.
package sleepcycle

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkerLive(t *testing.T) {
	bin := "../../crates/sleep-cycle/target/release/sleep-cycle"
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("worker binary not built (%s); run cargo build --release first", bin)
	}

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
	if got.RowsIn != 3 || got.RowsOut != 2 || got.Dropped != 1 {
		t.Fatalf("summary = %+v, want {3 2 1}", got)
	}
	if fi, err := os.Stat(output); err != nil || fi.Size() == 0 {
		t.Fatalf("expected non-empty output Parquet, err=%v", err)
	}
	t.Logf("live DataFusion dedup OK: %d -> %d (dropped %d), parquet written", got.RowsIn, got.RowsOut, got.Dropped)
}
