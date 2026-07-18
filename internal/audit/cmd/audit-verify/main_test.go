package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/audit"
)

// writeValidChain builds a small, intact hash-chained audit log at path via
// the real audit.HashChainedSink, so tests verify against the same on-disk
// format the production sink writes (not a hand-rolled fixture).
func writeValidChain(t *testing.T, path string, n int) {
	t.Helper()
	sink, err := audit.NewHashChainedSink(path)
	if err != nil {
		t.Fatalf("NewHashChainedSink: %v", err)
	}
	for i := 0; i < n; i++ {
		sink.Record(audit.Event{
			Timestamp: time.Unix(1700000000+int64(i), 0).UTC(),
			TenantID:  "tenant-a",
			UserID:    "user-a",
			Method:    "GET",
			Route:     "/v1/tenant/tenant-a/user/user-a/chat",
			Status:    200,
			LatencyMS: int64(i + 1),
		})
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestRun_UsageErrorWhenPathMissing covers the "no -path given" branch: exit
// code 2 and a usage message on stderr, nothing written to stdout.
func TestRun_UsageErrorWhenPathMissing(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage: audit-verify") {
		t.Errorf("stderr = %q, want a usage message", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty on a usage error", stdout.String())
	}
}

// TestRun_BadFlagReturnsUsageExitCode covers fs.Parse's own error branch (an
// unrecognized flag), proving it returns exit code 2 rather than the process
// exiting directly (flag.ContinueOnError, not the package-global
// flag.CommandLine's default ExitOnError).
func TestRun_BadFlagReturnsUsageExitCode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-not-a-real-flag"}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

// TestRun_FileNotFoundIsIOError covers the audit.VerifyFile open-error
// branch: brokenAt is -1 (not a chain-break), so run must exit 2, not 1.
func TestRun_FileNotFoundIsIOError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.jsonl")
	var stdout, stderr bytes.Buffer
	code := run([]string{"-path", missing}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit code = %d, want 2 (I/O error)", code)
	}
	if !strings.Contains(stderr.String(), "audit-verify:") {
		t.Errorf("stderr = %q, want the audit-verify error prefix", stderr.String())
	}
}

// TestRun_IntactChainExitsZero covers the clean-verification success path.
func TestRun_IntactChainExitsZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	writeValidChain(t, path, 5)

	var stdout, stderr bytes.Buffer
	code := run([]string{"-path", path}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "OK: audit chain") {
		t.Errorf("stdout = %q, want the OK message", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty on success", stderr.String())
	}
}

// TestRun_BrokenChainExitsOne covers the ok=false (chain-break, no read
// error) branch: a record's content is tampered with after the fact so its
// stored hash no longer matches the recomputed one.
func TestRun_BrokenChainExitsOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	writeValidChain(t, path, 3)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// Flip the tenant_id on the (still well-formed JSON) first record so its
	// content no longer matches its recorded hash, without corrupting the
	// JSON structure itself (that's the "unparseable" case, covered below).
	tampered := bytes.Replace(raw, []byte(`"tenant_id":"tenant-a"`), []byte(`"tenant_id":"tenant-mallory"`), 1)
	if bytes.Equal(tampered, raw) {
		t.Fatal("tamper replacement did not match — fixture format assumption is stale")
	}
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"-path", path}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "BROKEN: audit chain") {
		t.Errorf("stdout = %q, want the BROKEN message", stdout.String())
	}
}

// TestRun_UnparseableRecordExitsOneWithIndex covers the VerifyFile-returns-
// an-error-with-a-valid-brokenAt branch: a line that is not valid JSON at
// all (distinct from the well-formed-but-tampered case above).
func TestRun_UnparseableRecordExitsOneWithIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := os.WriteFile(path, []byte("not valid json at all\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"-path", path}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "first unparseable record at index 0") {
		t.Errorf("stderr = %q, want the unparseable-record message", stderr.String())
	}
}
