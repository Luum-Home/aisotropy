package bitrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// MeasurementStore is the subset of *Store the coordinator writes to. A narrow
// seam so tests can inject a fake alongside a fake Runner.
type MeasurementStore interface {
	Save(ctx context.Context, m Measurement) error
}

// defaultSampleBatch bounds how many held-out samples one RunOnce scores, so a
// single pass has a bounded subprocess input regardless of backlog.
const defaultSampleBatch = 64

// Coordinator drives one full bpt measurement pass: read the held-out samples,
// hydrate each one's context_with (the exact request-path hydration, via the
// Hydrator seam) and context_without (bare persona), score both in ONE scorer
// subprocess run, compute the memory lift, and persist a Measurement per sample —
// mirroring internal/sleepcycle.Coordinator.
type Coordinator struct {
	Sampler  Sampler
	Hydrator Hydrator
	Runner   Runner
	Store    MeasurementStore
	// Dir is the scratch directory for the NDJSON artifacts; "" uses the OS temp
	// dir. Each run gets its own subdir, removed afterward.
	Dir string
	// SampleBatch bounds samples per pass; <= 0 defaults to defaultSampleBatch.
	SampleBatch int
	// Tracer emits one span per pass; nil uses a no-op tracer.
	Tracer trace.Tracer
}

func (c Coordinator) tracer() trace.Tracer {
	if c.Tracer != nil {
		return c.Tracer
	}
	return noop.NewTracerProvider().Tracer("aisotropy/bitrate")
}

// RunOnce performs a single measurement pass and returns the measurements it
// persisted (empty when there is nothing held out to score). A per-sample
// hydration failure skips that sample rather than failing the batch — a
// scheduled job stays robust to one bad session.
func (c Coordinator) RunOnce(ctx context.Context) ([]Measurement, error) {
	batch := c.SampleBatch
	if batch <= 0 {
		batch = defaultSampleBatch
	}
	samples, err := c.Sampler.HeldOut(ctx, batch)
	if err != nil {
		return nil, fmt.Errorf("bitrate: read held-out samples: %w", err)
	}
	if len(samples) == 0 {
		return nil, nil
	}

	ctx, span := c.tracer().Start(ctx, "bitrate.run")
	defer span.End()

	// Build the request set: two rows per usable sample (with / without memory).
	// A sample whose hydration fails is dropped here and never scored.
	var reqs []ScoreRequest
	usable := make([]Sample, 0, len(samples))
	for _, s := range samples {
		ctxWith, err := c.Hydrator.ContextWith(ctx, s.TenantID, s.UserID, s.SessionID, "", s.Text)
		if err != nil {
			span.AddEvent("bitrate.hydrate_skipped", trace.WithAttributes(
				attribute.String("sample_id", s.SampleID),
				attribute.String("error", err.Error()),
			))
			continue
		}
		ctxWithout, err := c.Hydrator.ContextWithout(ctx, "")
		if err != nil {
			span.AddEvent("bitrate.hydrate_skipped", trace.WithAttributes(
				attribute.String("sample_id", s.SampleID),
				attribute.String("error", err.Error()),
			))
			continue
		}
		reqs = append(reqs,
			ScoreRequest{ID: s.SampleID + "_with", Text: s.Text, Context: ctxWith},
			ScoreRequest{ID: s.SampleID + "_without", Text: s.Text, Context: ctxWithout},
		)
		usable = append(usable, s)
	}
	if len(usable) == 0 {
		return nil, nil
	}

	base := c.Dir
	if base == "" {
		base = os.TempDir()
	}
	work, err := os.MkdirTemp(base, "bitrate-")
	if err != nil {
		return nil, fmt.Errorf("bitrate: scratch dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(work) }()

	input := filepath.Join(work, "requests.ndjson")
	output := filepath.Join(work, "results.ndjson")
	if err := WriteNDJSON(input, reqs); err != nil {
		return nil, err
	}

	results, err := c.Runner.Score(ctx, input, output)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]ScoreResult, len(results))
	for _, r := range results {
		byID[r.ID] = r
	}

	now := time.Now().UTC()
	var out []Measurement
	for _, s := range usable {
		with, ok1 := byID[s.SampleID+"_with"]
		without, ok2 := byID[s.SampleID+"_without"]
		if !ok1 || !ok2 {
			// The scorer dropped a row for this sample; skip it rather than
			// persist a half measurement.
			continue
		}
		lift := without.BitsPerToken - with.BitsPerToken
		m := Measurement{
			TenantID:   s.TenantID,
			UserID:     s.UserID,
			MeasuredAt: now,
			BptWith:    with.BitsPerToken,
			BptWithout: without.BitsPerToken,
			Lift:       lift,
			NTokens:    with.NTokens,
			ModelID:    with.ModelID,
			SampleID:   s.SampleID,
		}
		if err := c.Store.Save(ctx, m); err != nil {
			return out, fmt.Errorf("bitrate: save measurement: %w", err)
		}
		// One auditable measurement event per sample, using the metric name
		// docs/research/bet-06-bits-per-token-metric.md fixes.
		span.AddEvent("agent.compression.bpt", trace.WithAttributes(
			attribute.String("tenant_id", s.TenantID),
			attribute.String("user_id", s.UserID),
			attribute.Float64("bpt_with", m.BptWith),
			attribute.Float64("bpt_without", m.BptWithout),
			attribute.Float64("lift", m.Lift),
			attribute.String("model_id", m.ModelID),
		))
		out = append(out, m)
	}
	return out, nil
}

// RunScheduler calls RunOnce every interval until ctx is cancelled. onResult (if
// non-nil) receives each pass's measurements and error, for logging/metrics. The
// caller owns the goroutine's lifetime via ctx.
func (c Coordinator) RunScheduler(ctx context.Context, interval time.Duration, onResult func([]Measurement, error)) {
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ms, err := c.RunOnce(ctx)
			if onResult != nil {
				onResult(ms, err)
			}
		}
	}
}
