// Package costmeter is the in-process aggregation layer behind Aisotropy's
// per-tenant cost view (Phase 7 DoD: "cost per token per tenant").
//
// internal/llm reports real provider token usage per completed stream (see
// llm.Usage). costmeter.Meter accumulates that usage per tenant_id — total
// input tokens, output tokens, request count, and estimated cost in USD — so
// a dashboard or observability endpoint can answer "how much has tenant X
// spent" without re-deriving it from raw traces on every request.
//
// Meter is per-process (embedded in the aisotropyd binary). It is NOT
// synchronized across replicas: each instance only knows about the requests it
// personally served, so it is a live, best-effort aggregate — not the billing
// system of record.
//
// DURABILITY (persist.go). New returns the volatile Meter; NewPersistent loads
// the totals from a durable store at boot and writes them back behind the
// request path, so a restart no longer starts every tenant from zero. That
// matters beyond the dashboard: internal/api's selectClient feeds
// TenantStats.CostUSD into gateway.CostAwarePolicy as the tenant's cumulative
// spend, so a volatile meter is a cost budget that resets with the process — a
// pod restarting three times hands the tenant three budgets. aisotropyd wires
// the persistent form over the working-memory BadgerDB.
//
// What is closed and what is not: restart-reset is CLOSED when a durable store
// is configured (AISOTROPY_DATA_DIR); replica-multiplication is NOT — with N
// replicas the effective ceiling is still N x BudgetUSD, because each process
// keeps its own store. The budget shapes requests; it does not cap spend. An
// undurable store is refused rather than written to, so the guarantee is never
// claimed where it does not hold (see NewPersistent).
package costmeter

import (
	"math"
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
	// token count (ADR-0061). It is a BREAKDOWN OF OutputTokens, not an addition
	// to it: every provider that surfaces a reasoning count reports it as a
	// subset of the completion tokens it already billed (OpenAI-compat reads it
	// from completion_tokens_details, a detail object of completion_tokens;
	// Anthropic bills thinking inside output_tokens and surfaces no separate
	// count, so this stays 0). It is reported on its own line so a tenant's spend
	// on hidden chain-of-thought is VISIBLE rather than buried — but a consumer
	// that ADDS it to OutputTokens double-counts. 0 for every non-reasoning turn
	// (the default).
	ReasoningTokens int64
	// CostUSD is the cumulative estimated cost in US dollars, as supplied by
	// callers of Record (see EstimateCostUSD for a helper to compute it from a
	// price table).
	CostUSD float64
	// UnmeteredTurns is how many of Requests were served WITHOUT any
	// provider-reported token usage — a turn the provider billed whose counts
	// never reached this process. It happens when the terminal usage block is
	// never observed: the client cancelled mid-stream, the stream failed
	// mid-flight, the peer disconnected before the reply could be written, or the
	// provider simply reported no usage.
	//
	// It exists because the alternative was worse. Recording nothing made "billed
	// but uncounted" indistinguishable from "never happened", so the gap was
	// invisible; recording an ESTIMATE would fabricate numbers into a meter whose
	// whole contract is real provider accounting (see llm.Usage). This counter
	// keeps the turn visible and the counts honestly absent: Requests minus
	// UnmeteredTurns is the metered population, and the ratio is the accounting
	// health signal to reconcile against the provider's own invoice.
	UnmeteredTurns int64
}

// Meter is a per-tenant token/cost accounting layer. It is safe for concurrent
// use by multiple goroutines.
//
// New returns the in-process, volatile Meter. NewPersistent returns one whose
// totals are loaded from — and written back to — a durable store, so a restart
// does not hand every tenant a fresh cost budget; see persist.go. The zero value
// is not usable; construct a Meter with New or NewPersistent.
type Meter struct {
	mu      sync.RWMutex
	tenants map[string]*TenantStats

	// Durability (persist.go). All nil/zero on a volatile Meter, in which case
	// every persistence path below is a no-op.
	kv           KV                      // nil => volatile
	dirty        map[string]*TenantStats // tenants changed since the last successful flush (value: the totals the flush encodes)
	stopFlush    chan struct{}           // closed by Close to stop the write-behind goroutine
	flushDone    chan struct{}           // closed by the write-behind goroutine on exit
	lastFlushErr error                   // most recent background flush failure, if any
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
	// Clamp on BOTH sides of "not a usable number": negative (a buggy upstream
	// report must never decrement a tenant's totals) and non-finite. A +Inf or
	// NaN cost is absorbing — every later addition stays +Inf/NaN, so one bad
	// report would permanently destroy that tenant's accounting AND make its
	// totals unencodable, which would silently stop the durable meter from ever
	// persisting that tenant again (encoding/json rejects non-finite floats).
	if costUSD < 0 || math.IsNaN(costUSD) || math.IsInf(costUSD, 0) {
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
	m.markDirty(tenant, stats)
}

// RecordUnmetered accounts one turn that the provider served and billed but for
// which NO usage block reached this process — see TenantStats.UnmeteredTurns for
// when that happens and why it is counted rather than estimated or dropped.
//
// It increments Requests and UnmeteredTurns and adds nothing to the token or
// cost totals. That asymmetry is deliberate: the turn is real (so it counts as a
// request), the counts are unknown (so they stay zero rather than invented), and
// the fact that they are unknown is itself recorded (so the gap is measurable).
func (m *Meter) RecordUnmetered(tenant string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	stats, ok := m.tenants[tenant]
	if !ok {
		stats = &TenantStats{TenantID: tenant}
		m.tenants[tenant] = stats
	}
	stats.Requests++
	stats.UnmeteredTurns++
	m.markDirty(tenant, stats)
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
