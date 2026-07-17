package semantic

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestNewDaemonEmbedderValidation verifies the required-field checks reject a
// missing binary path, model dir, or non-positive dimension rather than
// constructing a broken daemon (mirrors TestNewLocalEmbedder_Validation).
func TestNewDaemonEmbedderValidation(t *testing.T) {
	if _, err := NewDaemonEmbedder(DaemonEmbedderConfig{ModelDir: "m", Dimension: 8}); err == nil {
		t.Error("empty BinPath: expected error, got nil")
	}
	if _, err := NewDaemonEmbedder(DaemonEmbedderConfig{BinPath: "b", Dimension: 8}); err == nil {
		t.Error("empty ModelDir: expected error, got nil")
	}
	if _, err := NewDaemonEmbedder(DaemonEmbedderConfig{BinPath: "b", ModelDir: "m", Dimension: 0}); err == nil {
		t.Error("zero Dimension: expected error, got nil")
	}
	e, err := NewDaemonEmbedder(DaemonEmbedderConfig{BinPath: "b", ModelDir: "m", Dimension: 384})
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if e.Dimension() != 384 {
		t.Fatalf("Dimension() = %d, want 384", e.Dimension())
	}
}

// TestRingBufferKeepsOnlyLastMaxBytes verifies the bounded stderr ring buffer
// retains only the trailing `max` bytes across writes that overflow it — the
// truncation branch that keeps daemon diagnostics from growing unbounded.
func TestRingBufferKeepsOnlyLastMaxBytes(t *testing.T) {
	r := &ringBuffer{max: 4}
	if n, err := r.Write([]byte("ab")); err != nil || n != 2 {
		t.Fatalf("Write(ab) = (%d, %v), want (2, nil)", n, err)
	}
	if got := r.String(); got != "ab" {
		t.Fatalf("after short write, String() = %q, want \"ab\"", got)
	}
	// Overflow: total would be "abcdefg" (7 bytes); only the last 4 survive.
	if _, err := r.Write([]byte("cdefg")); err != nil {
		t.Fatalf("Write(cdefg): %v", err)
	}
	if got := r.String(); got != "defg" {
		t.Fatalf("after overflow, String() = %q, want \"defg\" (last 4 bytes)", got)
	}
}

// writeRawDaemon writes a POSIX-sh fake `embed --serve` that echoes one
// caller-chosen raw line per stdin request, ignoring the request content. This
// lets a test drive exchangeLocked's malformed-response branches (bad JSON,
// out-of-range id, duplicate id) deterministically.
func writeRawDaemon(t *testing.T, rawLine string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake sh daemon is POSIX-only")
	}
	script := "#!/bin/sh\nwhile IFS= read -r line; do\n  printf '%s\\n' '" + rawLine + "'\ndone\n"
	path := filepath.Join(t.TempDir(), "raw-daemon.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // test helper
		t.Fatalf("write raw daemon: %v", err)
	}
	return path
}

// TestDaemonEmbedderMalformedRowIsError verifies a daemon row that is not valid
// JSON is a returned decode error (and the fault tears the daemon down).
func TestDaemonEmbedderMalformedRowIsError(t *testing.T) {
	bin := writeRawDaemon(t, "not-json-at-all")
	e := mustDaemon(t, bin, 4)
	defer func() { _ = e.Close() }()

	if _, err := e.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("Embed: expected a decode error on a malformed daemon row, got nil")
	}
}

// TestDaemonEmbedderOutOfRangeRowIDIsError verifies a daemon row whose id maps
// to an index outside the request range is rejected, never used to index past
// the result slice.
func TestDaemonEmbedderOutOfRangeRowIDIsError(t *testing.T) {
	bin := writeRawDaemon(t, `{"id":"r42","embedding":[1,2,3,4],"dim":4,"model_id":"fake"}`)
	e := mustDaemon(t, bin, 4)
	defer func() { _ = e.Close() }()

	if _, err := e.Embed(context.Background(), []string{"only-one"}); err == nil {
		t.Fatal("Embed: expected an out-of-range row-id error, got nil")
	}
}

// TestDaemonEmbedderDuplicateRowIDIsError verifies a daemon that returns the
// same row id twice (for a two-input request) is a hard error — one position
// filled twice would leave another empty.
func TestDaemonEmbedderDuplicateRowIDIsError(t *testing.T) {
	// Every stdin line yields id "r0"; with two inputs the second row is a
	// duplicate of the first.
	bin := writeRawDaemon(t, `{"id":"r0","embedding":[1,2,3,4],"dim":4,"model_id":"fake"}`)
	e := mustDaemon(t, bin, 4)
	defer func() { _ = e.Close() }()

	if _, err := e.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("Embed: expected a duplicate row-id error, got nil")
	}
}

// TestDaemonEmbedderStartFailureIsError verifies that when the configured
// binary cannot be started (it does not exist), the first Embed surfaces the
// start error rather than hanging or panicking.
func TestDaemonEmbedderStartFailureIsError(t *testing.T) {
	e, err := NewDaemonEmbedder(DaemonEmbedderConfig{
		BinPath:   filepath.Join(t.TempDir(), "does-not-exist"),
		ModelDir:  t.TempDir(),
		Dimension: 4,
	})
	if err != nil {
		t.Fatalf("NewDaemonEmbedder: %v", err)
	}
	defer func() { _ = e.Close() }()

	_, err = e.Embed(context.Background(), []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "start daemon") {
		t.Fatalf("Embed: expected a start-daemon error, got %v", err)
	}
}
