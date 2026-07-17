package semantic

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeRawOutputLocalBin writes a POSIX-sh stand-in for crates/embed that
// ignores its input and writes a caller-chosen raw body to --output. This lets
// a test drive LocalEmbedder.readResults' malformed/misaligned branches
// deterministically without a real model.
func writeRawOutputLocalBin(t *testing.T, rawOutput string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake sh embedder is POSIX-only")
	}
	script := `#!/bin/sh
out=""
while [ $# -gt 0 ]; do
  case "$1" in
    --output) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf '%s' '__RAW__' > "$out"
`
	script = strings.NewReplacer("__RAW__", rawOutput).Replace(script)
	path := filepath.Join(t.TempDir(), "raw-embed.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // test helper
		t.Fatalf("write raw embed bin: %v", err)
	}
	return path
}

// TestLocalEmbedderMalformedRowIsError verifies an output row that is not valid
// JSON surfaces as a decode error from readResults.
func TestLocalEmbedderMalformedRowIsError(t *testing.T) {
	bin := writeRawOutputLocalBin(t, "not-json\n")
	e, _ := NewLocalEmbedder(LocalEmbedderConfig{BinPath: bin, ModelDir: t.TempDir(), Dimension: 3})
	if _, err := e.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("Embed: expected a decode error on a malformed output row, got nil")
	}
}

// TestLocalEmbedderOutOfRangeRowIDIsError verifies an output row whose id maps
// to an index outside the request range is rejected.
func TestLocalEmbedderOutOfRangeRowIDIsError(t *testing.T) {
	bin := writeRawOutputLocalBin(t, `{"id":"r99","embedding":[1,2,3],"dim":3,"model_id":"fake"}`+"\n")
	e, _ := NewLocalEmbedder(LocalEmbedderConfig{BinPath: bin, ModelDir: t.TempDir(), Dimension: 3})
	if _, err := e.Embed(context.Background(), []string{"only-one"}); err == nil {
		t.Fatal("Embed: expected an out-of-range row-id error, got nil")
	}
}

// TestLocalEmbedderDuplicateRowIDIsError verifies two output rows sharing an id
// are a hard error (one position filled twice).
func TestLocalEmbedderDuplicateRowIDIsError(t *testing.T) {
	dup := `{"id":"r0","embedding":[1,2,3],"dim":3,"model_id":"fake"}` + "\n" +
		`{"id":"r0","embedding":[4,5,6],"dim":3,"model_id":"fake"}` + "\n"
	bin := writeRawOutputLocalBin(t, dup)
	e, _ := NewLocalEmbedder(LocalEmbedderConfig{BinPath: bin, ModelDir: t.TempDir(), Dimension: 3})
	if _, err := e.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("Embed: expected a duplicate row-id error, got nil")
	}
}

// TestLocalEmbedderMissingVectorIsError verifies that when the subprocess emits
// no row for an input position, readResults reports the missing vector rather
// than returning a nil-filled slice.
func TestLocalEmbedderMissingVectorIsError(t *testing.T) {
	// One input, but the output is empty: position 0 is never filled.
	bin := writeRawOutputLocalBin(t, "")
	e, _ := NewLocalEmbedder(LocalEmbedderConfig{BinPath: bin, ModelDir: t.TempDir(), Dimension: 3})
	_, err := e.Embed(context.Background(), []string{"unanswered"})
	if err == nil || !strings.Contains(err.Error(), "missing vector") {
		t.Fatalf("Embed: expected a missing-vector error, got %v", err)
	}
}

// TestLocalEmbedderBlankLinesAreSkipped verifies readResults tolerates blank
// lines in the NDJSON output (the empty-line continue) while still aligning the
// real rows.
func TestLocalEmbedderBlankLinesAreSkipped(t *testing.T) {
	body := "\n" + `{"id":"r0","embedding":[1,2,3],"dim":3,"model_id":"fake"}` + "\n\n"
	bin := writeRawOutputLocalBin(t, body)
	e, _ := NewLocalEmbedder(LocalEmbedderConfig{BinPath: bin, ModelDir: t.TempDir(), Dimension: 3})
	vecs, err := e.Embed(context.Background(), []string{"x"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 3 {
		t.Fatalf("vecs = %#v, want one width-3 vector", vecs)
	}
}
