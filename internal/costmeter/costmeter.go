// Package costmeter is the in-process aggregation layer behind Aisotropy's
// per-tenant cost view (Phase 7 DoD: "cost per token per tenant").
//
// internal/llm reports real provider token usage per completed stream (see
// llm.Usage). costmeter.Meter accumulates that usage per tenant_id — total
// input tokens, output tokens, request count, and estimated cost in USD — so
// a dashboard or observability endpoint can answer "how much has tenant X
// spent" without re-deriving it from raw traces on every request.
//
// Meter is per-process (embedded in the aisotropyd binary), not a shared or
// durable store: it holds no state across process restarts and is not
// synchronized across replicas. In a multi-replica deployment each instance
// only knows about the requests it personally served. Treat it as a live,
// best-effort observability aggregate — not as the billing system of
// record. Durable, cross-replica accounting belongs in a networked tier
// (e.g. the planned rqlite facts memory), per docs/architecture/overview.md.
package costmeter

import (
	"sort"
	"sync"
)

// TenantStats holds the accumulated usage totals for a single tenant.
type TenantStats struct {
	// TenantID identifies the tenant these totals belong to.
	TenantID string
	// Requests is the number of Record calls attributed to this tenant.
	Requests int64
	// InputTokens is the cumulative provider-reported input/prompt token count.
	InputTokens int64
	// OutputTokens is the cumulative provider-reported output/completion token count.
	OutputTokens int64
	// ReasoningTokens is the cumulative provider-reported reasoning ("thinking")
	// token count (ADR-0061), kept SEPARATE from OutputTokens so a tenant's spend
	// on hidden chain-of-thought is visible in the /costs view rather than buried
	// inside output tokens. 0 for every non-reasoning turn (the default).
	ReasoningTokens int64
	// CostUSD is the cumulative estimated cost in US dollars, as supplied by
	// callers of Record (see EstimateCostUSD for a helper to compute it from a
	// price table).
	CostUSD float64
}

// Meter is an in-memory, per-tenant token/cost accounting layer. It is safe
// for concurrent use by multiple goroutines.
//
// The zero value is not usable; construct a Meter with New.
type Meter struct {
	mu      sync.RWMutex
	tenants map[string]*TenantStats
}

// New returns a ready-to-use Meter.
func New() *Meter {
	return &Meter{
		tenants: make(map[string]*TenantStats),
	}
}

// Record accumulates one completed LLM call's usage against tenant. It
// increments the tenant's request count by one and adds inputTokens,
// outputTokens, and costUSD to the running totals.
//
// tenant is expected to be the tenant_id propagated via context.Context per
// docs/architecture/multi-tenancy.md; costmeter itself does not validate or
// interpret it beyond using it as a map key.
func (m *Meter) Record(tenant string, inputTokens, outputTokens int, costUSD float64) {
	m.RecordUsage(tenant, inputTokens, outputTokens, 0, costUSD)
}

// RecordUsage is Record plus a reasoning-token count (ADR-0061). It accumulates
// one completed LLM call's usage against tenant — request count, input, output,
// reasoning, and cost. Record delegates here with reasoningTokens=0, so every
// existing caller is unchanged (backward-compatible/additive); only the
// reasoning path calls RecordUsage directly. Negative values are clamped to zero
// (a buggy upstream usage report must never decrement a tenant's totals).
func (m *Meter) RecordUsage(tenant string, inputTokens, outputTokens, reasoningTokens int, costUSD float64) {
	if inputTokens < 0 {
		inputTokens = 0
	}
	if outputTokens < 0 {
		outputTokens = 0
	}
	if reasoningTokens < 0 {
		reasoningTokens = 0
	}
	if costUSD < 0 {
		costUSD = 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	stats, ok := m.tenants[tenant]
	if !ok {
		stats = &TenantStats{TenantID: tenant}
		m.tenants[tenant] = stats
	}
	stats.Requests++
	stats.InputTokens += int64(inputTokens)
	stats.OutputTokens += int64(outputTokens)
	stats.ReasoningTokens += int64(reasoningTokens)
	stats.CostUSD += costUSD
}

// Stats returns tenant's accumulated totals and true if tenant has recorded
// at least one call, or the zero TenantStats and false otherwise.
func (m *Meter) Stats(tenant string) (TenantStats, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats, ok := m.tenants[tenant]
	if !ok {
		return TenantStats{}, false
	}
	return *stats, true
}

// Snapshot returns a stable copy of every tenant's accumulated totals,
// sorted by TenantID ascending. The returned slice and its elements are
// independent copies: mutating them does not affect the Meter, and further
// calls to Record do not affect a Snapshot already taken.
//
// Snapshot is the intended read path for a dashboard or observability
// endpoint exposing the per-tenant cost view.
func (m *Meter) Snapshot() []TenantStats {
	m.mu.RLock()
	out := make([]TenantStats, 0, len(m.tenants))
	for _, stats := range m.tenants {
		out = append(out, *stats)
	}
	m.mu.RUnlock()

	// out already holds independent copies, so sort it OUTSIDE the read lock:
	// the sort must not hold mu.RLock, which would block concurrent Record
	// calls (writers) for the duration of the sort.
	sort.Slice(out, func(i, j int) bool { return out[i].TenantID < out[j].TenantID })
	return out
}

// EstimateCostUSD computes the estimated cost in US dollars for a single LLM
// call given provider-reported token counts and a price table expressed in
// USD per 1,000,000 tokens (the unit most provider pricing pages use).
//
// It is a pure helper: callers combine it with Record, e.g.
//
//	cost := costmeter.EstimateCostUSD(usage.InputTokens, usage.OutputTokens, 3.00, 15.00)
//	meter.Record(tenantID, usage.InputTokens, usage.OutputTokens, cost)
func EstimateCostUSD(inputTokens, outputTokens int, inPricePer1M, outPricePer1M float64) float64 {
	inCost := float64(inputTokens) / 1_000_000 * inPricePer1M
	outCost := float64(outputTokens) / 1_000_000 * outPricePer1M
	return inCost + outCost
}
