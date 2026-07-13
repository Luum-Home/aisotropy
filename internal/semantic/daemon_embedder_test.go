package semantic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// writeFakeDaemon writes a POSIX-sh stand-in for `embed --serve`: it loops over
// stdin, extracts each request's id, and echoes one NDJSON row per line with a
// fixed `vals` embedding of width `dim`. With sleep>0 it stalls before each
// response so a ctx-cancellation watchdog can be exercised. This lets the Go
// daemon logic (realignment, dim check, restart, cancellation) be tested with
// zero model download — mirroring how bitrate/local tests avoid the real model.
func writeFakeDaemon(t *testing.T, dim int, vals string, sleep time.Duration) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake sh daemon is POSIX-only")
	}
	sleepCmd := ""
	if sleep > 0 {
		// sh `sleep` takes seconds; render the Duration as fractional seconds
		// (e.g. 800ms -> "0.8").
		sleepCmd = "sleep " + strconv.FormatFloat(sleep.Seconds(), 'f', -1, 64)
	}
	tmpl := `#!/bin/sh
# fake embed --serve daemon (tests only)
while IFS= read -r line; do
  __SLEEP__
  id=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  printf '{"id":"%s","embedding":[__VALS__],"dim":__DIM__,"model_id":"fake@test"}\n' "$id"
done
`
	script := strings.NewReplacer(
		"__SLEEP__", sleepCmd,
		"__VALS__", vals,
		"__DIM__", strconv.Itoa(dim),
	).Replace(tmpl)

	path := filepath.Join(t.TempDir(), "fake-embed.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // test helper
		t.Fatalf("write fake daemon: %v", err)
	}
	return path
}

func mustDaemon(t *testing.T, bin string, dim int) *DaemonEmbedder {
	t.Helper()
	e, err := NewDaemonEmbedder(DaemonEmbedderConfig{
		BinPath:   bin,
		ModelDir:  t.TempDir(), // fake ignores it; must be non-empty
		Dimension: dim,
	})
	if err != nil {
		t.Fatalf("NewDaemonEmbedder: %v", err)
	}
	return e
}

func TestDaemonEmbedderBatchRealign(t *testing.T) {
	bin := writeFakeDaemon(t, 4, "0.1,0.2,0.3,0.4", 0)
	e := mustDaemon(t, bin, 4)
	defer func() { _ = e.Close() }()

	got, err := e.Embed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d vectors, want 3", len(got))
	}
	for i, v := range got {
		if len(v) != 4 {
			t.Fatalf("vector %d dim %d, want 4", i, len(v))
		}
	}
	// The warm process must survive across calls (this is the whole point).
	if _, err := e.Embed(context.Background(), []string{"again"}); err != nil {
		t.Fatalf("second Embed on the same warm daemon: %v", err)
	}
}

func TestDaemonEmbedderEmptyInput(t *testing.T) {
	bin := writeFakeDaemon(t, 4, "0,0,0,0", 0)
	e := mustDaemon(t, bin, 4)
	defer func() { _ = e.Close() }()
	got, err := e.Embed(context.Background(), nil)
	if err != nil || got != nil {
		t.Fatalf("empty input: got (%v, %v), want (nil, nil)", got, err)
	}
}

func TestDaemonEmbedderDimensionMismatch(t *testing.T) {
	// Fake emits a width-3 vector; embedder is configured for 4.
	bin := writeFakeDaemon(t, 3, "0,0,0", 0)
	e := mustDaemon(t, bin, 4)
	defer func() { _ = e.Close() }()

	_, err := e.Embed(context.Background(), []string{"x"})
	if !errors.Is(err, ErrDimensionMismatch) {
		t.Fatalf("got %v, want ErrDimensionMismatch", err)
	}
	// A dim mismatch is a fault -> the daemon is torn down; the next call must
	// transparently respawn (and hit the same mismatch, proving restart works).
	if _, err := e.Embed(context.Background(), []string{"y"}); !errors.Is(err, ErrDimensionMismatch) {
		t.Fatalf("after fault, respawn+retry got %v, want ErrDimensionMismatch", err)
	}
}

func TestDaemonEmbedderRestartsAfterClose(t *testing.T) {
	bin := writeFakeDaemon(t, 4, "1,0,0,0", 0)
	e := mustDaemon(t, bin, 4)

	if _, err := e.Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatalf("first Embed: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := e.Close(); err != nil { // idempotent
		t.Fatalf("second Close: %v", err)
	}
	// After Close, a fresh Embed must respawn the daemon and succeed.
	if _, err := e.Embed(context.Background(), []string{"b"}); err != nil {
		t.Fatalf("Embed after Close (respawn): %v", err)
	}
	_ = e.Close()
}

func TestDaemonEmbedderContextCancel(t *testing.T) {
	// Fake stalls ~800ms before each response; cancel well before that.
	bin := writeFakeDaemon(t, 4, "0,0,0,0", 800*time.Millisecond)
	e := mustDaemon(t, bin, 4)
	defer func() { _ = e.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := e.Embed(ctx, []string{"slow"})
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want a context.DeadlineExceeded wrap", err)
	}
	if elapsed := time.Since(start); elapsed > 600*time.Millisecond {
		t.Fatalf("cancellation took %v; watchdog should have killed the daemon promptly", elapsed)
	}
	// The daemon was torn down on cancel; a fresh call must respawn and work.
	if _, err := e.Embed(context.Background(), []string{"ok"}); err != nil {
		t.Fatalf("Embed after cancel (respawn): %v", err)
	}
}
