package bitrate

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// summary is the scorer's one-line JSON result on stdout (the result rows
// themselves come from the output file). It is observability only.
type summary struct {
	Scored  int    `json:"scored"`
	ModelID string `json:"model_id"`
	Backend string `json:"backend"`
}

// Worker invokes the reference-model scorer as a subprocess (ADR-0005, mirroring
// internal/sleepcycle.Worker). It is a value type: construct it inline with the
// binary path and optional tuning. BinaryPath is a single executable taking
// --input/--output — for the Phase 1 Python scorer that is a thin wrapper (e.g.
// `exec .venv/bin/python bitrate_scorer.py "$@"`); for Phase 2 it is the Rust
// candle binary. The Go contract is identical either way.
type Worker struct {
	// BinaryPath is the scorer executable (--input/--output CLI).
	BinaryPath string
	// MaxAttempts bounds retries on a failed run (non-zero exit or spawn
	// error). <= 0 defaults to defaultMaxAttempts.
	MaxAttempts int
	// InitialBackoff is the first retry delay; it doubles each attempt.
	// <= 0 defaults to defaultInitialBackoff.
	InitialBackoff time.Duration
	// Tracer emits one span per Score. nil uses a no-op tracer.
	Tracer trace.Tracer
}

const (
	defaultMaxAttempts    = 3
	defaultInitialBackoff = 500 * time.Millisecond
)

func (w Worker) tracer() trace.Tracer {
	if w.Tracer != nil {
		return w.Tracer
	}
	return noop.NewTracerProvider().Tracer("aisotropy/bitrate")
}

// Score invokes the scorer over inputPath, writing NDJSON results to outputPath,
// and returns the parsed results. A failed run — a non-zero exit (exactly how a
// scorer panic surfaces to Go) or a spawn error — is retried with exponential
// backoff up to MaxAttempts. Every Score emits an OTel span.
func (w Worker) Score(ctx context.Context, inputPath, outputPath string) ([]ScoreResult, error) {
	attempts := w.MaxAttempts
	if attempts <= 0 {
		attempts = defaultMaxAttempts
	}
	backoff := w.InitialBackoff
	if backoff <= 0 {
		backoff = defaultInitialBackoff
	}

	ctx, span := w.tracer().Start(ctx, "bitrate.score")
	defer span.End()
	span.SetAttributes(
		attribute.String("bitrate.input", inputPath),
		attribute.String("bitrate.output", outputPath),
	)

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		results, sum, err := w.runOnce(ctx, inputPath, outputPath)
		if err == nil {
			span.SetAttributes(
				attribute.Int("bitrate.attempts", attempt),
				attribute.Int("bitrate.rows", len(results)),
				attribute.Int("bitrate.scored", sum.Scored),
				attribute.String("bitrate.backend", sum.Backend),
				attribute.String("bitrate.model_id", sum.ModelID),
			)
			return results, nil
		}
		lastErr = err
		if attempt < attempts {
			select {
			case <-ctx.Done():
				span.SetStatus(codes.Error, "cancelled")
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
		}
	}
	span.SetStatus(codes.Error, "bitrate scorer failed")
	span.RecordError(lastErr)
	return nil, fmt.Errorf("bitrate: scorer failed after %d attempt(s): %w", attempts, lastErr)
}

// runOnce spawns the scorer once. A non-zero exit becomes an error carrying the
// scorer's stderr; ctx cancellation kills the subprocess (CommandContext). On
// success it reads the NDJSON results from outputPath and parses the stdout
// summary line for observability.
func (w Worker) runOnce(ctx context.Context, inputPath, outputPath string) ([]ScoreResult, summary, error) {
	cmd := exec.CommandContext(ctx, w.BinaryPath, "--input", inputPath, "--output", outputPath)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, summary{}, fmt.Errorf("exit: %w; stderr: %s", err, strings.TrimSpace(stderr.String()))
	}
	results, err := ReadResults(outputPath)
	if err != nil {
		return nil, summary{}, err
	}
	return results, parseSummary(stdout.String()), nil
}

// parseSummary reads the scorer's summary from the last non-empty stdout line
// (the scorer may emit backend log lines before it). A missing/unparseable
// summary is not fatal — the result rows are the contract, the summary is only
// observability — so it degrades to a zero summary.
func parseSummary(stdout string) summary {
	line := ""
	for _, l := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if s := strings.TrimSpace(l); s != "" {
			line = s
		}
	}
	if line == "" {
		return summary{}
	}
	var s summary
	if err := json.Unmarshal([]byte(line), &s); err != nil {
		return summary{}
	}
	return s
}
