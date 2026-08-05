package costmeter

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// KV is the durable byte store a Meter persists its per-tenant totals into. It
// is declared here, structurally, so this package keeps importing nothing but
// the standard library: internal/memory's prefixed KV over the working-memory
// BadgerDB satisfies it without either package importing the other.
//
// Durable is the important method. The working-memory store is an IN-MEMORY
// BadgerDB unless AISOTROPY_DATA_DIR is set, and "persisting" into that is not
// persistence — it is the same volatile map with extra steps, plus the false
// impression that the problem is solved. A KV that answers false is refused
// rather than used (see NewPersistent).
type KV interface {
	// Load returns every stored entry, keyed by tenant id.
	Load(ctx context.Context) (map[string][]byte, error)
	// Store writes the given entries, overwriting any previous value for those
	// keys and leaving every other key alone.
	Store(ctx context.Context, entries map[string][]byte) error
	// Durable reports whether what Store writes actually outlives this process.
	Durable() bool
}

// defaultFlushInterval is the write-behind period when the caller passes 0.
//
// Seconds of lag are the correct trade here and it is worth being explicit
// about why. These totals feed gateway.CostAwarePolicy, which SHAPES a tenant's
// requests toward the cheap model once its cumulative spend crosses a budget —
// a coarse control whose input is already an estimate derived from a price
// table. Paying a synchronous BadgerDB write on every completed turn to make
// that estimate a few seconds fresher would put a durability fsync on the tail
// of the request path for no decision that changes. What matters is that the
// number is not ZERO after a restart, and write-behind delivers that: the worst
// case is a crash losing the last few seconds of spend, which under-counts by
// seconds instead of by the whole process lifetime.
const defaultFlushInterval = 5 * time.Second

// NewPersistent returns a Meter whose per-tenant totals are loaded from kv at
// construction and written back to it behind the request path, so a process
// restart no longer hands every tenant a fresh cost budget.
//
// WHY THIS IS NOT ONLY OBSERVABILITY. internal/api's selectClient reads
// TenantStats.CostUSD and passes it to gateway.CostAwarePolicy as the tenant's
// cumulative spend. With a volatile meter, a pod that restarts three times gives
// the tenant three budgets — the shaping control silently resets with the
// process, and nothing in the logs says so.
//
// THE VOLATILE-STORE TRAP, handled explicitly. kv == nil, or a kv whose Durable
// reports false (the default working-memory store: in-memory BadgerDB unless
// AISOTROPY_DATA_DIR is set), does NOT get attached: the returned Meter is the
// plain volatile one, Durable() reports false, and nothing is ever written. The
// alternative — wiring the writes up anyway — would leave the default
// configuration exactly as broken as before while every reader of the code, the
// docs, and the /costs endpoint believed the totals were persisted. An inert fix
// that looks applied is worse than a missing one. Callers that care (aisotropyd
// does) check Durable() and say so out loud at boot.
//
// A durable store that cannot be READ is a different matter and IS an error:
// starting with zeroed totals against a store that has real ones would reset
// every tenant's budget silently, which is the failure this exists to prevent.
//
// WHAT THIS DOES NOT FIX. The totals are still this process's own. Each replica
// keeps its own store (BadgerDB holds an exclusive directory lock, so they
// cannot share one), so with N replicas the effective ceiling remains N x
// BudgetUSD — restart-reset is closed, replica-multiplication is not. Dividing
// the configured budget by the declared replica count (as the quota limiter
// does) or moving the accumulator to the shared networked tier are the two ways
// to close that; neither is done here.
//
// flushEvery <= 0 uses defaultFlushInterval.
func NewPersistent(ctx context.Context, kv KV, flushEvery time.Duration) (*Meter, error) {
	m := New()
	if kv == nil || !kv.Durable() {
		return m, nil
	}

	entries, err := kv.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("costmeter: load persisted totals: %w", err)
	}
	for key, raw := range entries {
		var stats TenantStats
		if uerr := json.Unmarshal(raw, &stats); uerr != nil {
			return nil, fmt.Errorf("costmeter: decode persisted totals for %q: %w", key, uerr)
		}
		if stats.TenantID == "" {
			stats.TenantID = key
		}
		m.tenants[stats.TenantID] = &stats
	}

	if flushEvery <= 0 {
		flushEvery = defaultFlushInterval
	}
	m.kv = kv
	m.dirty = make(map[string]*TenantStats)
	m.stopFlush = make(chan struct{})
	m.flushDone = make(chan struct{})
	go m.writeBehind(m.stopFlush, m.flushDone, flushEvery)
	return m, nil
}

// Durable reports whether this Meter's totals outlive the process. False means
// the accounting is live-only: correct for the requests this process served,
// reset to zero on the next start — and, because selectClient routes on it, so
// is the cost budget.
func (m *Meter) Durable() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.kv != nil
}

// markDirty records that tenant's totals have moved since the last flush,
// holding the stats pointer the flush will encode. The caller must already hold
// m.mu for writing.
//
// It keeps the POINTER rather than just the name so the flush never has to look
// the tenant up again — there is no "marked dirty but not in the map" case to
// handle, because the only way to be marked is to have just been written.
func (m *Meter) markDirty(tenant string, stats *TenantStats) {
	if m.dirty != nil {
		m.dirty[tenant] = stats
	}
}

// writeBehind flushes dirty tenants every interval until Close stops it. The
// channels are passed in rather than read off m: Close clears the field as part
// of making itself idempotent, and a goroutine re-reading it would end up
// selecting on a nil channel (i.e. blocking forever) instead of on the closed
// stop signal.
func (m *Meter) writeBehind(stop, done chan struct{}, interval time.Duration) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			// Its own bounded context: the flush must not inherit (nor outlive
			// unboundedly) any request's lifetime.
			ctx, cancel := context.WithTimeout(context.Background(), interval)
			err := m.Flush(ctx)
			cancel()
			if err != nil {
				// Non-fatal by construction: a store that is briefly unwritable
				// must not take down the process, and the tenant stays dirty so
				// the next tick retries it.
				m.recordFlushErr(err)
			}
		}
	}
}

// recordFlushErr keeps the most recent write-behind failure so Close (and any
// operator surface) can report that the persisted totals may be stale rather
// than discovering it after a restart.
func (m *Meter) recordFlushErr(err error) {
	m.mu.Lock()
	m.lastFlushErr = err
	m.mu.Unlock()
}

// LastFlushError returns the most recent background flush failure, or nil. A
// non-nil value means the durable totals are behind the live ones.
func (m *Meter) LastFlushError() error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastFlushErr
}

// Flush writes every tenant whose totals changed since the last successful
// flush. It is a no-op on a Meter with no durable store, and a no-op when
// nothing moved — so a quiet fleet costs nothing and a large one pays for the
// tenants that were actually active, not for every tenant it has ever seen.
//
// On failure the affected tenants stay marked dirty, so the next flush retries
// them: a transient store error delays persistence, it never drops it.
func (m *Meter) Flush(ctx context.Context) error {
	m.mu.Lock()
	if m.kv == nil || len(m.dirty) == 0 {
		m.mu.Unlock()
		return nil
	}
	pending := make(map[string][]byte, len(m.dirty))
	for tenant, stats := range m.dirty {
		raw, err := json.Marshal(*stats)
		if err != nil {
			// Leave this tenant dirty and give up on the pass; a totals struct
			// that will not encode is a bug, not something to retry silently.
			m.lastFlushErr = err
			m.mu.Unlock()
			return fmt.Errorf("costmeter: encode totals for %q: %w", tenant, err)
		}
		pending[tenant] = raw
		// Clear the flag HERE, under the same lock that read the totals — not
		// after the store call returns. Otherwise a Record landing while the
		// write is in flight gets its flag cleared along with the older snapshot
		// that was actually written, and that tenant's delta is never persisted
		// until it happens to spend again (and is lost outright if it goes quiet).
		delete(m.dirty, tenant)
	}
	kv := m.kv
	m.mu.Unlock()

	if err := kv.Store(ctx, pending); err != nil {
		// Nothing landed: put the flags back so the next pass retries. A tenant
		// re-marked by a concurrent Record is simply marked twice — harmless.
		wrapped := fmt.Errorf("costmeter: persist totals: %w", err)
		m.mu.Lock()
		for tenant := range pending {
			if stats, ok := m.tenants[tenant]; ok {
				m.dirty[tenant] = stats
			}
		}
		m.lastFlushErr = wrapped
		m.mu.Unlock()
		return wrapped
	}

	m.mu.Lock()
	m.lastFlushErr = nil
	m.mu.Unlock()
	return nil
}

// Close stops the write-behind goroutine and performs one final flush, so a
// graceful shutdown persists the spend of the turns it just served instead of
// discarding up to one flush interval of it. It is safe to call on a volatile
// Meter (a no-op) and safe to call more than once.
func (m *Meter) Close(ctx context.Context) error {
	m.mu.Lock()
	stop := m.stopFlush
	m.stopFlush = nil
	done := m.flushDone
	m.mu.Unlock()

	if stop != nil {
		close(stop)
		<-done
	}
	return m.Flush(ctx)
}
