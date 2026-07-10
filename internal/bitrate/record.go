package bitrate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// WriteNDJSON writes requests as newline-delimited JSON to path — the exact
// format the scorer reads. Materializing to disk (rather than a pipe) is
// deliberate and mirrors the sleep cycle (ADR-0005): it gives the batch job
// checkpointing, reproducibility, and debuggability.
func WriteNDJSON(path string, reqs []ScoreRequest) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("bitrate: create input %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for i, r := range reqs {
		if err := enc.Encode(r); err != nil {
			return fmt.Errorf("bitrate: encode request %d: %w", i, err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("bitrate: flush input: %w", err)
	}
	return nil
}

// ReadResults parses the scorer's NDJSON output file into ScoreResults, one per
// non-empty line, in file order. A line that is not valid JSON is a contract
// violation and fails the whole parse (the scorer must emit clean NDJSON).
func ReadResults(path string) ([]ScoreResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("bitrate: open results %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var out []ScoreResult
	sc := bufio.NewScanner(f)
	// Result rows can be long (a large context is echoed nowhere, but keep a
	// generous buffer for safety on wide unit strings / ids).
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var r ScoreResult
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return nil, fmt.Errorf("bitrate: parse result line %d %q: %w", line, raw, err)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("bitrate: scan results: %w", err)
	}
	return out, nil
}
