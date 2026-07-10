//go:build manual

// Live validation against the REAL Python reference scorer
// (spikes/compression/bitrate_scorer.py). Behind the `manual` build tag, so it
// is EXCLUDED from `go test ./...` and CI: it needs a Python venv with torch (or
// at least the n-gram fallback path) plus the scorer wrapper.
//
// Point AISOTROPY_BITRATE_BIN at an executable that forwards --input/--output to
// the scorer, e.g. a wrapper:
//
//	#!/bin/bash
//	exec /abs/path/spikes/compression/.venv/bin/python \
//	     /abs/path/spikes/compression/bitrate_scorer.py "$@"
//
// then run:
//
//	AISOTROPY_BITRATE_BIN=/abs/path/wrapper.sh \
//	  go test -tags manual -run TestScorerLive -v ./internal/bitrate
//
// It feeds one "with memory" and one "without memory" request through the real
// subprocess and asserts both rows come back with a numeric bits_per_token.
package bitrate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestScorerLive(t *testing.T) {
	bin := os.Getenv("AISOTROPY_BITRATE_BIN")
	if bin == "" {
		t.Skip("AISOTROPY_BITRATE_BIN unset; set it to a scorer wrapper to run the live test")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("scorer binary %q not found: %v", bin, err)
	}

	dir := t.TempDir()
	input := filepath.Join(dir, "requests.ndjson")
	output := filepath.Join(dir, "results.ndjson")

	reqs := []ScoreRequest{
		{ID: "live_with", Text: "el usuario usa una computadora todos los dias",
			Context: "el usuario trabaja con computadoras y escribe en espanol"},
		{ID: "live_without", Text: "el usuario usa una computadora todos los dias", Context: ""},
	}
	if err := WriteNDJSON(input, reqs); err != nil {
		t.Fatal(err)
	}

	results, err := (Worker{BinaryPath: bin}).Score(context.Background(), input, output)
	if err != nil {
		t.Fatalf("Score (live): %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	byID := map[string]ScoreResult{}
	for _, r := range results {
		byID[r.ID] = r
	}
	for _, id := range []string{"live_with", "live_without"} {
		r, ok := byID[id]
		if !ok {
			t.Fatalf("missing result for %q", id)
		}
		if r.NTokens <= 0 || r.BitsPerToken <= 0 {
			t.Fatalf("%q has non-numeric surprisal: %+v", id, r)
		}
		if r.ModelID == "" {
			t.Fatalf("%q missing model_id", id)
		}
	}
	t.Logf("live scorer OK: with=%.3f bits/tok, without=%.3f bits/tok (model=%s)",
		byID["live_with"].BitsPerToken, byID["live_without"].BitsPerToken, byID["live_with"].ModelID)
}
