package costmeter

import (
	"context"
	"errors"
	"maps"
	"math"
	"sync"
	"testing"
	"time"
)

// fakeKV is an in-test stand-in for the working-memory-backed store: a map that
// survives the Meter that wrote it, which is exactly what a process restart
// looks like from the Meter's side.
type fakeKV struct {
	mu      sync.Mutex
	data    map[string][]byte
	durable bool
	stores  int
	failNow error
	onStore func()
}

func newFakeKV(durable bool) *fakeKV {
	return &fakeKV{data: make(map[string][]byte), durable: durable}
}

func (k *fakeKV) Load(context.Context) (map[string][]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.failNow != nil {
		return nil, k.failNow
	}
	out := make(map[string][]byte, len(k.data))
	maps.Copy(out, k.data)
	return out, nil
}

func (k *fakeKV) Store(_ context.Context, entries map[string][]byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.failNow != nil {
		return k.failNow
	}
	k.stores++
	maps.Copy(k.data, entries)
	if k.onStore != nil {
		hook := k.onStore
		k.onStore = nil
		hook()
	}
	return nil
}

func (k *fakeKV) Durable() bool { return k.durable }

func (k *fakeKV) storeCount() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.stores
}

// TestPersistentMeterSurvivesRestart is the bug: the meter is the input to the
// cost-budget routing policy, so a process that restarts three times hands the
// tenant three fresh budgets. The totals must be there when the next process
// reads them.
func TestPersistentMeterSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	kv := newFakeKV(true)

	m, err := NewPersistent(ctx, kv, 0)
	if err != nil {
		t.Fatalf("NewPersistent: %v", err)
	}
	if !m.Durable() {
		t.Fatal("Durable() = false over a durable store")
	}
	m.RecordUsage("acme", 1000, 500, 120, 4.25)
	m.RecordUnmetered("acme")
	m.Record("globex", 10, 5, 0.01)
	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The restart: a brand-new Meter over the same store.
	restarted, err := NewPersistent(ctx, kv, 0)
	if err != nil {
		t.Fatalf("NewPersistent after restart: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close(ctx) })

	got, ok := restarted.Stats("acme")
	if !ok {
		t.Fatal("acme has no stats after restart — the tenant got a fresh cost budget")
	}
	if got.CostUSD != 4.25 {
		t.Errorf("CostUSD = %v after restart, want 4.25 — the budget the routing policy reads reset", got.CostUSD)
	}
	if got.InputTokens != 1000 || got.OutputTokens != 500 || got.ReasoningTokens != 120 {
		t.Errorf("token totals = %+v, want 1000/500/120 preserved", got)
	}
	if got.Requests != 2 || got.UnmeteredTurns != 1 {
		t.Errorf("Requests/UnmeteredTurns = %d/%d, want 2/1", got.Requests, got.UnmeteredTurns)
	}
	if g, ok := restarted.Stats("globex"); !ok || g.CostUSD != 0.01 {
		t.Errorf("globex stats = %+v (ok=%v), want CostUSD 0.01", g, ok)
	}

	// And accumulation CONTINUES from the loaded totals rather than restarting.
	restarted.Record("acme", 0, 0, 0.75)
	if got, _ := restarted.Stats("acme"); got.CostUSD != 5.0 {
		t.Errorf("CostUSD after post-restart spend = %v, want 5.00 (4.25 loaded + 0.75)", got.CostUSD)
	}
}

// TestPersistentMeterRefusesVolatileStore is the trap: the working-memory
// BadgerDB is in-memory unless AISOTROPY_DATA_DIR is set, so "persisting" into
// it in the default configuration writes to a store that dies with the process.
// A fix that is inert but LOOKS applied is worse than no fix, so the Meter
// refuses to claim durability it does not have.
func TestPersistentMeterRefusesVolatileStore(t *testing.T) {
	ctx := context.Background()
	kv := newFakeKV(false)

	m, err := NewPersistent(ctx, kv, 0)
	if err != nil {
		t.Fatalf("NewPersistent over a volatile store must not fail the boot: %v", err)
	}
	t.Cleanup(func() { _ = m.Close(ctx) })

	if m.Durable() {
		t.Fatal("Durable() = true over a store that does not survive the process")
	}
	m.Record("acme", 1, 1, 1.0)
	if err := m.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if n := kv.storeCount(); n != 0 {
		t.Fatalf("wrote to a volatile store %d times — the accounting would look persisted and not be", n)
	}
	// The meter itself still works; only the durability claim is withheld.
	if got, ok := m.Stats("acme"); !ok || got.CostUSD != 1.0 {
		t.Fatalf("in-process accounting broken: %+v (ok=%v)", got, ok)
	}
}

// TestPersistentMeterNilStore keeps the zero-config path working: no store at
// all behaves exactly like New().
func TestPersistentMeterNilStore(t *testing.T) {
	ctx := context.Background()
	m, err := NewPersistent(ctx, nil, time.Second)
	if err != nil {
		t.Fatalf("NewPersistent(nil): %v", err)
	}
	if m.Durable() {
		t.Fatal("Durable() = true with no store")
	}
	m.Record("acme", 1, 1, 2.0)
	if got, _ := m.Stats("acme"); got.CostUSD != 2.0 {
		t.Fatalf("CostUSD = %v, want 2.0", got.CostUSD)
	}
	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestPersistentMeterLoadFailureIsFatal: a durable store that cannot be READ is
// not the same as no store. Starting anyway would silently zero every tenant's
// spend, which is the exact failure this whole fix exists to stop.
func TestPersistentMeterLoadFailureIsFatal(t *testing.T) {
	kv := newFakeKV(true)
	kv.failNow = errors.New("boom")
	if _, err := NewPersistent(context.Background(), kv, 0); err == nil {
		t.Fatal("NewPersistent returned no error when the durable store could not be read")
	}
}

// TestPersistentMeterWriteBehindFlushes covers the background half: totals reach
// the store without any explicit Flush from the request path.
func TestPersistentMeterWriteBehindFlushes(t *testing.T) {
	ctx := context.Background()
	kv := newFakeKV(true)
	m, err := NewPersistent(ctx, kv, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("NewPersistent: %v", err)
	}
	t.Cleanup(func() { _ = m.Close(ctx) })

	m.Record("acme", 1, 1, 3.5)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if kv.storeCount() > 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if kv.storeCount() == 0 {
		t.Fatal("write-behind never flushed")
	}

	restarted, err := NewPersistent(ctx, kv, 0)
	if err != nil {
		t.Fatalf("NewPersistent after write-behind: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close(ctx) })
	if got, ok := restarted.Stats("acme"); !ok || got.CostUSD != 3.5 {
		t.Fatalf("write-behind persisted %+v (ok=%v), want CostUSD 3.5", got, ok)
	}
}

// TestPersistentMeterFlushIsIncremental: only tenants that moved since the last
// flush are written. The meter is on the hot path's tail; rewriting every tenant
// every tick would make a large fleet's flush cost grow with the tenant count.
func TestPersistentMeterFlushIsIncremental(t *testing.T) {
	ctx := context.Background()
	kv := newFakeKV(true)
	m, err := NewPersistent(ctx, kv, 0)
	if err != nil {
		t.Fatalf("NewPersistent: %v", err)
	}
	t.Cleanup(func() { _ = m.Close(ctx) })

	m.Record("acme", 1, 1, 1.0)
	if err := m.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	first := kv.storeCount()
	// Nothing changed: the next flush must be a no-op, not a rewrite.
	if err := m.Flush(ctx); err != nil {
		t.Fatalf("second Flush: %v", err)
	}
	if kv.storeCount() != first {
		t.Fatalf("flush with no new usage still wrote (%d -> %d)", first, kv.storeCount())
	}
}

// TestPersistentMeterFlushDoesNotSwallowConcurrentSpend guards the window
// between reading a tenant's totals and clearing its dirty flag: a Record that
// lands while the store write is in flight must survive the flush that was
// already under way. Losing it would silently under-report that tenant's spend
// — the exact failure the persistence exists to prevent, arriving by a subtler
// route.
func TestPersistentMeterFlushDoesNotSwallowConcurrentSpend(t *testing.T) {
	ctx := context.Background()
	kv := newFakeKV(true)
	m, err := NewPersistent(ctx, kv, 0)
	if err != nil {
		t.Fatalf("NewPersistent: %v", err)
	}
	t.Cleanup(func() { _ = m.Close(ctx) })

	m.Record("acme", 0, 0, 1.0)
	// The interleaving: more spend is recorded while the first flush is writing.
	kv.onStore = func() { m.Record("acme", 0, 0, 2.0) }
	if err := m.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := m.Flush(ctx); err != nil {
		t.Fatalf("second Flush: %v", err)
	}

	restarted, err := NewPersistent(ctx, kv, 0)
	if err != nil {
		t.Fatalf("NewPersistent after restart: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close(ctx) })
	if got, ok := restarted.Stats("acme"); !ok || got.CostUSD != 3.0 {
		t.Fatalf("persisted CostUSD = %v (ok=%v), want 3.0 — spend recorded during a flush was dropped", got.CostUSD, ok)
	}
}

// TestPersistentMeterFlushFailureKeepsRetrying: a store that rejects a write
// must not cost the totals. The tenant stays dirty and the next flush lands it.
func TestPersistentMeterFlushFailureKeepsRetrying(t *testing.T) {
	ctx := context.Background()
	kv := newFakeKV(true)
	m, err := NewPersistent(ctx, kv, 0)
	if err != nil {
		t.Fatalf("NewPersistent: %v", err)
	}
	t.Cleanup(func() { _ = m.Close(ctx) })

	m.Record("acme", 0, 0, 4.0)
	kv.mu.Lock()
	kv.failNow = errors.New("store unavailable")
	kv.mu.Unlock()
	if err := m.Flush(ctx); err == nil {
		t.Fatal("Flush reported success over a failing store")
	}
	if m.LastFlushError() == nil {
		t.Error("LastFlushError is nil after a failed flush — a stale persisted total would be invisible")
	}

	kv.mu.Lock()
	kv.failNow = nil
	kv.mu.Unlock()
	if err := m.Flush(ctx); err != nil {
		t.Fatalf("retry Flush: %v", err)
	}
	if m.LastFlushError() != nil {
		t.Errorf("LastFlushError still set after a successful flush: %v", m.LastFlushError())
	}
	restarted, err := NewPersistent(ctx, kv, 0)
	if err != nil {
		t.Fatalf("NewPersistent after retry: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close(ctx) })
	if got, ok := restarted.Stats("acme"); !ok || got.CostUSD != 4.0 {
		t.Fatalf("persisted CostUSD = %v (ok=%v), want 4.0 — the failed flush dropped the totals instead of retrying", got.CostUSD, ok)
	}
}

// TestPersistentMeterWriteBehindSurfacesFailures: a background flush that keeps
// failing must be visible. Silence here means the persisted totals drift behind
// the live ones and nobody learns until a restart reads the stale number.
func TestPersistentMeterWriteBehindSurfacesFailures(t *testing.T) {
	ctx := context.Background()
	kv := newFakeKV(true)
	m, err := NewPersistent(ctx, kv, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("NewPersistent: %v", err)
	}
	t.Cleanup(func() { _ = m.Close(ctx) })

	kv.mu.Lock()
	kv.failNow = errors.New("store unavailable")
	kv.mu.Unlock()
	m.Record("acme", 1, 1, 1.0)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && m.LastFlushError() == nil {
		time.Sleep(2 * time.Millisecond)
	}
	if m.LastFlushError() == nil {
		t.Fatal("a repeatedly failing write-behind reported nothing")
	}

	// And it recovers on its own once the store comes back.
	kv.mu.Lock()
	kv.failNow = nil
	kv.mu.Unlock()
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && m.LastFlushError() != nil {
		time.Sleep(2 * time.Millisecond)
	}
	if m.LastFlushError() != nil {
		t.Fatalf("write-behind never recovered: %v", m.LastFlushError())
	}
	if got := kv.storeCount(); got == 0 {
		t.Fatal("nothing was ever persisted after the store recovered")
	}
}

// TestPersistentMeterRejectsCorruptPersistedTotals: garbage under the key
// namespace stops the boot rather than being silently discarded. Discarding it
// would zero that tenant's budget, which is the failure this persistence exists
// to prevent — arriving as a "clean" start.
func TestPersistentMeterRejectsCorruptPersistedTotals(t *testing.T) {
	kv := newFakeKV(true)
	kv.data["acme"] = []byte("{not json")
	if _, err := NewPersistent(context.Background(), kv, 0); err == nil {
		t.Fatal("NewPersistent accepted a corrupt persisted entry")
	}
}

// TestPersistentMeterKeyIsTheTenantFallback: an entry whose payload carries no
// tenant id is still attributed, from the key it was stored under, instead of
// collapsing into a single unnamed bucket.
func TestPersistentMeterKeyIsTheTenantFallback(t *testing.T) {
	ctx := context.Background()
	kv := newFakeKV(true)
	kv.data["acme"] = []byte(`{"CostUSD":2.5}`)

	m, err := NewPersistent(ctx, kv, 0)
	if err != nil {
		t.Fatalf("NewPersistent: %v", err)
	}
	t.Cleanup(func() { _ = m.Close(ctx) })
	got, ok := m.Stats("acme")
	if !ok || got.CostUSD != 2.5 || got.TenantID != "acme" {
		t.Fatalf("Stats = %+v (ok=%v), want acme with CostUSD 2.5", got, ok)
	}
}

// TestRecordRejectsNonFiniteCost is the same discipline as the negative clamp: a
// buggy upstream usage report must not be able to poison a tenant's running
// total. +Inf or NaN would stick forever — every later addition stays +Inf/NaN —
// and would additionally make the totals unencodable, so the persistence layer
// could never write that tenant again.
func TestRecordRejectsNonFiniteCost(t *testing.T) {
	m := New()
	m.Record("acme", 1, 1, math.Inf(1))
	m.Record("acme", 1, 1, math.NaN())
	m.Record("acme", 1, 1, 0.5)
	got, _ := m.Stats("acme")
	if got.CostUSD != 0.5 {
		t.Fatalf("CostUSD = %v, want 0.5 — a non-finite report poisoned the total", got.CostUSD)
	}
	if got.Requests != 3 {
		t.Errorf("Requests = %d, want 3: the turns still happened", got.Requests)
	}
}
