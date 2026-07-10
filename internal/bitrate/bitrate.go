// Package bitrate is the Go side of the Bet 6 "bits-per-token-per-tenant"
// measurement (docs/research/bet-06-bits-per-token-metric.md). It quantifies how
// much a tenant's hydrated memory reduces the surprisal of that tenant's own
// text: for a held-out sample, bpt = -(1/N) Σ log₂ P(tokenᵢ | context), and the
// memory lift = bpt_without − bpt_with. A positive lift trending upward per
// tenant is the success signal.
//
// No serving LLM provider exposes token logprobs, so bits/token is NOT a
// serving-span enrichment — it is measured OFFLINE by a reference model invoked
// as a subprocess, mirroring the sleep-cycle DataFusion worker symbol-for-symbol
// (ADR-0005): Go materializes an NDJSON request file, execs the scorer with
// --input/--output, and parses NDJSON results back, turning a non-zero exit into
// a Go error with retry/backoff and an OTel span per run — never CGO, never
// in-process. Phase 1 (this package) drives the Python scorer
// (spikes/compression/bitrate_scorer.py, pinned to distilgpt2); Phase 2 will
// swap in a Rust candle scorer behind this SAME contract.
//
// Multi-tenant isolation is a constitutional gate: every store read and write is
// filtered by (tenant_id, user_id) through internal/tenancy, exactly as the
// facts tier does. The one reviewed cross-tenant read (the ops /bitrate view and
// the coordinator's held-out sweep) mirrors facts.ExportAll.
package bitrate

import (
	"context"
	"time"
)

// ScoreRequest is one surprisal request handed to the scorer. The json tags ARE
// the wire contract the scorer reads (spikes/compression/bitrate_scorer.py):
// keep them in lockstep. Text is the string whose bits/token is measured;
// Context is the conditioning prefix (the hydrated memory, or the bare persona).
type ScoreRequest struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Context string `json:"context"`
}

// ScoreResult is one scored row parsed back from the scorer's NDJSON output. The
// json tags ARE the wire contract the scorer writes. BitsPerToken is the
// per-token surprisal used to compute lift; ModelID attributes the measurement
// to the exact reference model that produced it.
type ScoreResult struct {
	ID           string  `json:"id"`
	BitsTotal    float64 `json:"bits_total"`
	BitsPerToken float64 `json:"bits_per_token"`
	NTokens      int     `json:"n_tokens"`
	ModelID      string  `json:"model_id"`
	Unit         string  `json:"unit"`
}

// Runner scores a batch: it reads an NDJSON request file, writes an NDJSON
// result file, and returns the parsed results. *Worker (the real subprocess
// scorer) satisfies it; tests inject a fake so the default suite never needs
// torch or a Python venv.
type Runner interface {
	Score(ctx context.Context, inputPath, outputPath string) ([]ScoreResult, error)
}

// Measurement is one persisted bpt observation for a (tenant, user). Lift is
// BptWithout − BptWith: positive means the hydrated memory made the tenant's own
// text less surprising (memory helped). SampleID ties the measurement back to
// the held-out sample it scored, so a re-run upserts rather than duplicates.
type Measurement struct {
	TenantID   string
	UserID     string
	MeasuredAt time.Time
	BptWith    float64
	BptWithout float64
	Lift       float64
	NTokens    int
	ModelID    string
	SampleID   string
}

// TenantSummary is the per-tenant aggregate the ops /bitrate view returns: the
// latest observation plus the trend over the tenant's whole lift series.
type TenantSummary struct {
	TenantID      string  `json:"tenant_id"`
	LatestLift    float64 `json:"latest_lift"`
	LatestBptWith float64 `json:"latest_bpt_with"`
	TrendSlope    float64 `json:"trend_slope"`
	ModelID       string  `json:"model_id"`
	NMeasurements int     `json:"n_measurements"`
}

// Hydrator is the anti-magic seam (ADR-0002): internal/bitrate builds the two
// contexts it scores THROUGH this interface, whose sole production implementation
// (internal/api) calls the exact same hydrateSystemPrompt code path the request
// serves — there is no second, parallel hydration. ContextWith returns the full
// hydrated system prompt (persona + facts + semantic + working-memory summary);
// ContextWithout returns the bare persona base prompt (the "no memory" baseline).
type Hydrator interface {
	ContextWith(ctx context.Context, tenantID, userID, sessionID, productID, query string) (string, error)
	ContextWithout(ctx context.Context, productID string) (string, error)
}
