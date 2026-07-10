package bitrate

import (
	"context"
	"hash/fnv"
	"time"
)

// sampleDenominator is the held-out reservation rate: 1 in N user turns is
// reserved as a bitrate sample. 20 ≈ 5%, matching the design's leakage budget —
// small enough not to starve consolidation, large enough to build a per-tenant
// trend.
const sampleDenominator = 20

// ShouldSample decides, deterministically from a stable per-turn reference,
// whether a turn is reserved as a held-out bitrate sample. It is the ONE source
// of the ~5% rule (internal/api calls it at turn ingestion). Determinism matters
// for two reasons: the same turn is never re-decided differently on a retry, and
// the selection cannot be gamed by wall-clock timing. An empty ref is never
// sampled.
//
// LEAKAGE is the correctness hazard this guards: a reserved turn MUST be excluded
// from that turn's facts/semantic consolidation. If the scored text were already
// distilled into the tenant's memory, ContextWith would trivially predict it and
// the measured lift would be spuriously high and meaningless. The reservation
// (internal/api) and this predicate are two halves of the same anti-leakage
// contract.
func ShouldSample(ref string) bool {
	if ref == "" {
		return false
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(ref))
	return h.Sum32()%sampleDenominator == 0
}

// Sample is one reserved held-out turn: the tenant's own text, excluded from
// consolidation, that the coordinator later scores. SessionID lets the coordinator
// hydrate the same session's working memory the request would have seen.
type Sample struct {
	TenantID  string
	UserID    string
	SessionID string
	SampleID  string
	Text      string
	CreatedAt time.Time
}

// SampleRecorder reserves a held-out sample. internal/api holds this (nil
// disables sampling) and calls Reserve at turn ingestion for the ~5% of turns
// ShouldSample selects — in place of the facts/semantic consolidation enqueue,
// never in addition to it (that is the anti-leakage guarantee). *Store satisfies
// it.
type SampleRecorder interface {
	Reserve(ctx context.Context, tenantID, userID, sessionID, sampleID, text string) error
}

// Sampler reads recent held-out samples to score. The coordinator holds this;
// *Store satisfies it. HeldOut is a deliberate CROSS-TENANT read (like
// facts.ExportAll) — the coordinator re-partitions each sample by its own
// (tenant, user) and hydrates within that scope before scoring, so isolation
// holds for every measurement.
type Sampler interface {
	HeldOut(ctx context.Context, limit int) ([]Sample, error)
}
