package semantic

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// writeFakeLocalEmbedBin writes a POSIX-sh stand-in for the crates/embed binary
// in its per-call form: it parses --input/--output, reads each NDJSON request
// line, and writes one result row per line with a fixed-width embedding. This
// exercises LocalEmbedder.Embed / writeRequest / readResults / idFromIndex with
// zero model download, mirroring the daemon fake in daemon_embedder_test.go.
func writeFakeLocalEmbedBin(t *testing.T, dim int, vals string, exitCode int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake sh embedder is POSIX-only")
	}
	tmpl := `#!/bin/sh
in=""
out=""
while [ $# -gt 0 ]; do
  case "$1" in
    --input) in="$2"; shift 2 ;;
    --output) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
if [ "__EXIT__" -ne 0 ]; then
  echo "fake embed failure" >&2
  exit __EXIT__
fi
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  printf '{"id":"%s","embedding":[__VALS__],"dim":__DIM__,"model_id":"fake@test"}\n' "$id"
done < "$in" > "$out"
`
	script := strings.NewReplacer(
		"__EXIT__", strconv.Itoa(exitCode),
		"__VALS__", vals,
		"__DIM__", strconv.Itoa(dim),
	).Replace(tmpl)

	path := filepath.Join(t.TempDir(), "fake-embed.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // test helper
		t.Fatalf("write fake embed bin: %v", err)
	}
	return path
}

func TestLocalEmbedder_EmbedRoundTrip(t *testing.T) {
	bin := writeFakeLocalEmbedBin(t, 3, "0.1,0.2,0.3", 0)
	e, err := NewLocalEmbedder(LocalEmbedderConfig{BinPath: bin, ModelDir: t.TempDir(), Dimension: 3})
	if err != nil {
		t.Fatalf("NewLocalEmbedder: %v", err)
	}
	vecs, err := e.Embed(context.Background(), []string{"first", "second", "third"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 3 {
		t.Fatalf("expected 3 vectors, got %d", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != 3 {
			t.Fatalf("vector %d width = %d, want 3", i, len(v))
		}
	}
}

func TestLocalEmbedder_EmbedEmptyInput(t *testing.T) {
	bin := writeFakeLocalEmbedBin(t, 3, "0,0,0", 0)
	e, _ := NewLocalEmbedder(LocalEmbedderConfig{BinPath: bin, ModelDir: t.TempDir(), Dimension: 3})
	vecs, err := e.Embed(context.Background(), nil)
	if err != nil || vecs != nil {
		t.Fatalf("empty input should return nil,nil; got %v (%v)", vecs, err)
	}
}

func TestLocalEmbedder_NonZeroExitIsError(t *testing.T) {
	bin := writeFakeLocalEmbedBin(t, 3, "0,0,0", 3) // Rust panic surfaces as non-zero exit
	e, _ := NewLocalEmbedder(LocalEmbedderConfig{BinPath: bin, ModelDir: t.TempDir(), Dimension: 3})
	_, err := e.Embed(context.Background(), []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "local embedder exit") {
		t.Fatalf("expected subprocess exit error, got %v", err)
	}
}

func TestLocalEmbedder_DimMismatchIsError(t *testing.T) {
	// Binary emits width-3 vectors, but the embedder is configured for width 5.
	bin := writeFakeLocalEmbedBin(t, 3, "1,2,3", 0)
	e, _ := NewLocalEmbedder(LocalEmbedderConfig{BinPath: bin, ModelDir: t.TempDir(), Dimension: 5})
	_, err := e.Embed(context.Background(), []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "dim") {
		t.Fatalf("expected dimension-mismatch error, got %v", err)
	}
}

func TestLocalEmbedder_BinaryMissingIsError(t *testing.T) {
	e, _ := NewLocalEmbedder(LocalEmbedderConfig{BinPath: "/nonexistent/embed-bin", ModelDir: t.TempDir(), Dimension: 3})
	if _, err := e.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatalf("expected error execing a missing binary")
	}
}

func TestIdFromIndex(t *testing.T) {
	if idx, ok := idFromIndex("r7"); !ok || idx != 7 {
		t.Fatalf("idFromIndex(r7) = %d,%v", idx, ok)
	}
	if _, ok := idFromIndex("x7"); ok {
		t.Fatalf("idFromIndex should reject a non-r prefix")
	}
	if _, ok := idFromIndex("rabc"); ok {
		t.Fatalf("idFromIndex should reject a non-numeric suffix")
	}
	if got := indexID(4); got != "r4" {
		t.Fatalf("indexID(4) = %q, want r4", got)
	}
}
