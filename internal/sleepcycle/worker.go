package sleepcycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Summary is the worker's one-line JSON result on stdout.
type Summary struct {
	RowsIn  int `json:"rows_in"`
	RowsOut int `json:"rows_out"`
	Dropped int `json:"dropped"`
	// NoiseDropped is the sub-count of Dropped removed by the low-signal noise
	// prune (importance below the worker's floor) rather than by deduplication;
	// dedup-dropped = Dropped - NoiseDropped. Zero when the noise pass is
	// disabled (the default). Additive to the pre-noise contract: an older
	// worker that never emits it decodes to 0.
	NoiseDropped int `json:"noise_dropped"`
	// DroppedIDs are the ids of every row the worker removed — noise-pruned OR
	// de-duplicated — so the coordinator can delete exactly those from the live
	// store.
	DroppedIDs []string `json:"dropped_ids"`
}

// Worker invokes the Rust sleep-cycle binary as a subprocess (ADR-0005). It is
// a value type: construct it inline with the binary path and optional tuning.
type Worker struct {
	// BinaryPath is the compiled sleep-cycle worker (crates/sleep-cycle).
	BinaryPath string
	// MaxAttempts bounds retries on a failed run (non-zero exit or spawn
	// error). <= 0 defaults to defaultMaxAttempts.
	MaxAttempts int
	// InitialBackoff is the first retry delay; it doubles each attempt.
	// <= 0 defaults to defaultInitialBackoff.
	InitialBackoff time.Duration
	// MinImportance is the low-signal noise floor passed to the worker as
	// --min-importance: facts below it are pruned as noise before dedup. <= 0
	// (the zero value) omits the flag entirely, so the worker runs dedup-only —
	// the historical, byte-for-byte-compatible default. The wiring
	// (aisotropy/app.go) owns whether to set a non-zero floor.
	MinImportance float64
	// Tracer emits one span per Run. nil uses a no-op tracer.
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
	return noop.NewTracerProvider().Tracer("aisotropy/sleepcycle")
}

// Run invokes the worker over inputPath, writing the deduplicated Parquet to
// outputPath, and returns the parsed Summary. A failed run — a non-zero exit,
// which is exactly how a Rust panic surfaces to Go (ADR-0005), or a spawn
// error — is retried with exponential backoff up to MaxAttempts. Every Run
// emits an OTel span carrying an agent.optimization.consolidation event so each
// consolidation mutation is auditable (Phase 6 DoD).
func (w Worker) Run(ctx context.Context, inputPath, outputPath string) (Summary, error) {
	attempts := w.MaxAttempts
	if attempts <= 0 {
		attempts = defaultMaxAttempts
	}
	backoff := w.InitialBackoff
	if backoff <= 0 {
		backoff = defaultInitialBackoff
	}

	ctx, span := w.tracer().Start(ctx, "sleepcycle.run")
	defer span.End()
	span.SetAttributes(
		attribute.String("sleepcycle.input", inputPath),
		attribute.String("sleepcycle.output", outputPath),
	)

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		summary, err := w.runOnce(ctx, inputPath, outputPath)
		if err == nil {
			span.SetAttributes(
				attribute.Int("sleepcycle.attempts", attempt),
				attribute.Int("sleepcycle.rows_in", summary.RowsIn),
				attribute.Int("sleepcycle.rows_out", summary.RowsOut),
				attribute.Int("sleepcycle.dropped", summary.Dropped),
			)
			// One auditable optimization event per mutation (Phase 6 DoD).
			span.AddEvent("agent.optimization.consolidation", trace.WithAttributes(
				attribute.Int("rows_in", summary.RowsIn),
				attribute.Int("rows_out", summary.RowsOut),
				attribute.Int("dropped", summary.Dropped),
			))
			return summary, nil
		}
		lastErr = err
		if attempt < attempts {
			select {
			case <-ctx.Done():
				span.SetStatus(codes.Error, "cancelled")
				return Summary{}, ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
		}
	}
	span.SetStatus(codes.Error, "sleepcycle worker failed")
	span.RecordError(lastErr)
	return Summary{}, fmt.Errorf("sleepcycle: worker failed after %d attempt(s): %w", attempts, lastErr)
}

// runOnce spawns the worker once. A non-zero exit becomes an error carrying the
// worker's stderr; ctx cancellation kills the subprocess (CommandContext).
func (w Worker) runOnce(ctx context.Context, inputPath, outputPath string) (Summary, error) {
	args := []string{"--input", inputPath, "--output", outputPath}
	// Only pass the noise floor when explicitly enabled, so the default argv is
	// unchanged from the dedup-only contract.
	if w.MinImportance > 0 {
		args = append(args, "--min-importance", strconv.FormatFloat(w.MinImportance, 'g', -1, 64))
	}
	cmd := exec.CommandContext(ctx, w.BinaryPath, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Summary{}, fmt.Errorf("exit: %w; stderr: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseSummary(stdout.String())
}

// parseSummary reads the worker's summary from the last non-empty stdout line
// (DataFusion may emit log lines before it).
func parseSummary(stdout string) (Summary, error) {
	line := ""
	for _, l := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if s := strings.TrimSpace(l); s != "" {
			line = s
		}
	}
	if line == "" {
		return Summary{}, errors.New("sleepcycle: worker produced no summary line")
	}
	var s Summary
	if err := json.Unmarshal([]byte(line), &s); err != nil {
		return Summary{}, fmt.Errorf("sleepcycle: parse summary %q: %w", line, err)
	}
	return s, nil
}
