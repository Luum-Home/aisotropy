package sleepcycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeStub writes an executable bash script standing in for the Rust worker,
// so the Go orchestration contract is tested without a Rust toolchain in CI.
func writeStub(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stub.sh")
	script := "#!/bin/bash\nout=\"\"\nwhile [ $# -gt 0 ]; do case \"$1\" in --output) out=\"$2\"; shift 2;; *) shift;; esac; done\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

func TestRunSuccess(t *testing.T) {
	stub := writeStub(t, ": > \"$out\"\necho '{\"rows_in\":5,\"rows_out\":3,\"dropped\":2}'\n")
	w := Worker{BinaryPath: stub}
	out := filepath.Join(t.TempDir(), "o.parquet")
	got, err := w.Run(context.Background(), "in.ndjson", out)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.RowsIn != 5 || got.RowsOut != 3 || got.Dropped != 2 {
		t.Fatalf("summary = %+v", got)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("worker did not produce output: %v", err)
	}
}

func TestRunRetriesThenFails(t *testing.T) {
	// Always exits non-zero: exactly how a Rust panic surfaces to Go.
	stub := writeStub(t, "echo 'boom' >&2\nexit 1\n")
	w := Worker{BinaryPath: stub, MaxAttempts: 3, InitialBackoff: time.Millisecond}
	_, err := w.Run(context.Background(), "in", "out")
	if err == nil {
		t.Fatal("expected failure after retries")
	}
	if !strings.Contains(err.Error(), "3 attempt") {
		t.Fatalf("error should mention attempts: %v", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error should carry worker stderr: %v", err)
	}
}

func TestRunRetrySucceedsAfterTransientFailure(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	// Fails the first invocation (creating the marker), succeeds thereafter.
	body := "if [ ! -f '" + marker + "' ]; then touch '" + marker + "'; echo 'transient' >&2; exit 1; fi\n" +
		": > \"$out\"\necho '{\"rows_in\":2,\"rows_out\":2,\"dropped\":0}'\n"
	stub := writeStub(t, body)
	w := Worker{BinaryPath: stub, MaxAttempts: 3, InitialBackoff: time.Millisecond}
	got, err := w.Run(context.Background(), "in", filepath.Join(t.TempDir(), "o"))
	if err != nil {
		t.Fatalf("Run should recover on retry: %v", err)
	}
	if got.RowsIn != 2 {
		t.Fatalf("summary = %+v", got)
	}
}

func TestRunCancelledContext(t *testing.T) {
	stub := writeStub(t, "exit 1\n")
	w := Worker{BinaryPath: stub, MaxAttempts: 5, InitialBackoff: time.Hour} // long backoff
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	if _, err := w.Run(ctx, "in", "out"); err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

func TestParseSummaryIgnoresLeadingLogLines(t *testing.T) {
	got, err := parseSummary("some datafusion log\nanother line\n{\"rows_in\":9,\"rows_out\":7,\"dropped\":2}\n")
	if err != nil {
		t.Fatal(err)
	}
	if got.RowsIn != 9 || got.RowsOut != 7 || got.Dropped != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestWriteNDJSONRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "facts.ndjson")
	recs := []Record{
		{ID: "a", TenantID: "acme", UserID: "alice", Content: "one", Importance: 0.5, CreatedAt: 1, UpdatedAt: 2},
		{ID: "b", TenantID: "acme", UserID: "alice", Content: "two", Importance: 0.9, CreatedAt: 3, UpdatedAt: 4},
	}
	if err := WriteNDJSON(path, recs); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if !strings.Contains(lines[0], `"tenant_id":"acme"`) || !strings.Contains(lines[1], `"importance":0.9`) {
		t.Fatalf("unexpected NDJSON: %s", data)
	}
}
