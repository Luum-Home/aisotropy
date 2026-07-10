package bitrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeStub writes an executable bash script standing in for the reference-model
// scorer, so the Go orchestration contract is tested without a Python venv or
// torch in CI. The stub captures the --output path and runs body, which is
// expected to write the NDJSON results there and print a summary line.
func writeStub(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stub.sh")
	script := "#!/bin/bash\nout=\"\"\nwhile [ $# -gt 0 ]; do case \"$1\" in --output) out=\"$2\"; shift 2;; *) shift;; esac; done\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

func TestScoreSuccess(t *testing.T) {
	body := "cat > \"$out\" <<'ROWS'\n" +
		"{\"id\":\"s1_with\",\"bits_total\":10.0,\"bits_per_token\":2.5,\"n_tokens\":4,\"model_id\":\"distilgpt2@pinned\",\"unit\":\"subword-distilgpt2\"}\n" +
		"{\"id\":\"s1_without\",\"bits_total\":16.0,\"bits_per_token\":4.0,\"n_tokens\":4,\"model_id\":\"distilgpt2@pinned\",\"unit\":\"subword-distilgpt2\"}\n" +
		"ROWS\n" +
		"echo '{\"scored\":2,\"model_id\":\"distilgpt2@pinned\",\"backend\":\"LM_REAL\"}'\n"
	stub := writeStub(t, body)
	w := Worker{BinaryPath: stub}
	out := filepath.Join(t.TempDir(), "results.ndjson")
	got, err := w.Score(context.Background(), "in.ndjson", out)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	if got[0].ID != "s1_with" || got[0].BitsPerToken != 2.5 || got[0].NTokens != 4 {
		t.Fatalf("row 0 = %+v", got[0])
	}
	if got[1].ID != "s1_without" || got[1].BitsPerToken != 4.0 {
		t.Fatalf("row 1 = %+v", got[1])
	}
	if got[0].ModelID != "distilgpt2@pinned" {
		t.Fatalf("model_id not echoed: %+v", got[0])
	}
}

func TestScoreRetriesThenFails(t *testing.T) {
	// Always exits non-zero: exactly how a scorer panic surfaces to Go.
	stub := writeStub(t, "echo 'boom' >&2\nexit 1\n")
	w := Worker{BinaryPath: stub, MaxAttempts: 3, InitialBackoff: time.Millisecond}
	_, err := w.Score(context.Background(), "in", filepath.Join(t.TempDir(), "o"))
	if err == nil {
		t.Fatal("expected failure after retries")
	}
	if !strings.Contains(err.Error(), "3 attempt") {
		t.Fatalf("error should mention attempts: %v", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error should carry scorer stderr: %v", err)
	}
}

func TestScoreRetrySucceedsAfterTransientFailure(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	body := "if [ ! -f '" + marker + "' ]; then touch '" + marker + "'; echo 'transient' >&2; exit 1; fi\n" +
		"echo '{\"id\":\"s1_with\",\"bits_total\":1.0,\"bits_per_token\":0.5,\"n_tokens\":2,\"model_id\":\"m@pinned\",\"unit\":\"word\"}' > \"$out\"\n" +
		"echo '{\"scored\":1,\"model_id\":\"m@pinned\",\"backend\":\"FALLBACK_NGRAM\"}'\n"
	stub := writeStub(t, body)
	w := Worker{BinaryPath: stub, MaxAttempts: 3, InitialBackoff: time.Millisecond}
	got, err := w.Score(context.Background(), "in", filepath.Join(t.TempDir(), "o"))
	if err != nil {
		t.Fatalf("Score should recover on retry: %v", err)
	}
	if len(got) != 1 || got[0].BitsPerToken != 0.5 {
		t.Fatalf("results = %+v", got)
	}
}

func TestScoreCancelledContext(t *testing.T) {
	stub := writeStub(t, "exit 1\n")
	w := Worker{BinaryPath: stub, MaxAttempts: 5, InitialBackoff: time.Hour} // long backoff
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	if _, err := w.Score(ctx, "in", "out"); err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

func TestReadResultsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.ndjson")
	reqs := []ScoreRequest{
		{ID: "a_with", Text: "hola mundo", Context: "memoria del usuario"},
		{ID: "a_without", Text: "hola mundo", Context: ""},
	}
	if err := WriteNDJSON(path, reqs); err != nil {
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
	if !strings.Contains(lines[0], `"id":"a_with"`) || !strings.Contains(lines[0], `"context":"memoria del usuario"`) {
		t.Fatalf("unexpected request NDJSON: %s", lines[0])
	}

	// And the result reader round-trips a results file back into ScoreResults.
	resPath := filepath.Join(t.TempDir(), "results.ndjson")
	if err := os.WriteFile(resPath, []byte(
		"{\"id\":\"a_with\",\"bits_total\":8.0,\"bits_per_token\":2.0,\"n_tokens\":4,\"model_id\":\"m@pinned\",\"unit\":\"word\"}\n"+
			"\n"+ // a blank line must be skipped, not error
			"{\"id\":\"a_without\",\"bits_total\":12.0,\"bits_per_token\":3.0,\"n_tokens\":4,\"model_id\":\"m@pinned\",\"unit\":\"word\"}\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	results, err := ReadResults(resPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].BitsPerToken != 2.0 || results[1].BitsPerToken != 3.0 {
		t.Fatalf("results = %+v", results)
	}
}

func TestShouldSampleDeterministicAndBounded(t *testing.T) {
	if ShouldSample("") {
		t.Fatal("empty ref must never be sampled")
	}
	// Determinism: same ref → same decision.
	for _, ref := range []string{"abc123", "deadbeef", "0f0f0f", "held-out-ref"} {
		if ShouldSample(ref) != ShouldSample(ref) {
			t.Fatalf("ShouldSample(%q) not deterministic", ref)
		}
	}
	// Rate: over many distinct refs the held-out fraction is roughly 1/20.
	// Assert it is a plausible minority (never 0, never a majority) so a broken
	// predicate that samples everything or nothing is caught.
	held := 0
	total := 4000
	for i := 0; i < total; i++ {
		if ShouldSample(refFor(i)) {
			held++
		}
	}
	if held == 0 || held > total/4 {
		t.Fatalf("held-out fraction implausible: %d/%d", held, total)
	}
}

func refFor(i int) string {
	// Vary the ref like a real per-turn sha prefix would; a regular generator
	// can land in a single hash residue class and hide a working predicate.
	return fmt.Sprintf("turn-%d-%x", i, i*2654435761)
}
