package aisotropy

import (
	"context"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/memory"
)

// TestCostMeterPersistsAcrossRestartWithDataDir is the wired-up version of the
// bug: with a data directory configured, a tenant's accumulated spend is still
// there for the process that comes after. Because internal/api's selectClient
// routes on exactly this number, a meter that forgets is a cost budget that
// resets — three restarts, three budgets.
func TestCostMeterPersistsAcrossRestartWithDataDir(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("AISOTROPY_DATA_DIR", dir)

	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))
	rt, err := app.ensureRuntime()
	if err != nil {
		t.Fatalf("ensureRuntime: %v", err)
	}
	if !rt.costMeter.Durable() {
		t.Fatal("cost meter is not durable with AISOTROPY_DATA_DIR set — the persistence is inert")
	}
	rt.costMeter.Record("acme", 1200, 340, 7.5)
	// Shut the app down the way Run does: the final flush happens in closeFn.
	rt.close(ctx)

	// The restart: a second App over the same data directory.
	restarted := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))
	rt2, err := restarted.ensureRuntime()
	if err != nil {
		t.Fatalf("ensureRuntime after restart: %v", err)
	}
	defer rt2.close(ctx)

	got, ok := rt2.costMeter.Stats("acme")
	if !ok {
		t.Fatal("the restarted process has no spend recorded for acme — the tenant got a fresh cost budget")
	}
	if got.CostUSD != 7.5 {
		t.Errorf("CostUSD = %v after restart, want 7.5", got.CostUSD)
	}
	if got.InputTokens != 1200 || got.OutputTokens != 340 {
		t.Errorf("token totals = %d/%d after restart, want 1200/340", got.InputTokens, got.OutputTokens)
	}
}

// TestCostMeterNotDurableWithoutDataDir pins the trap-handling decision: with no
// data directory the working-memory store is in-memory, so the meter refuses to
// claim durability instead of writing into a store that dies with the process.
// The runtime keeps working; only the false impression is withheld.
func TestCostMeterNotDurableWithoutDataDir(t *testing.T) {
	ctx := context.Background()
	t.Setenv("AISOTROPY_DATA_DIR", "")

	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))
	rt, err := app.ensureRuntime()
	if err != nil {
		t.Fatalf("ensureRuntime: %v", err)
	}
	defer rt.close(ctx)

	if rt.costMeter.Durable() {
		t.Fatal("cost meter claims durability over an in-memory store")
	}
	rt.costMeter.Record("acme", 10, 5, 0.25)
	if got, ok := rt.costMeter.Stats("acme"); !ok || got.CostUSD != 0.25 {
		t.Fatalf("live accounting broken without a data dir: %+v (ok=%v)", got, ok)
	}
}

// TestCostMeterBootFailsOnUnreadablePersistedTotals: when the durable store
// holds totals that cannot be read, the runtime must refuse to boot rather than
// start from zero. Booting "clean" would hand every tenant a fresh budget while
// the real spend sat unread on disk — the failure the persistence exists to
// prevent, wearing the costume of a healthy start.
func TestCostMeterBootFailsOnUnreadablePersistedTotals(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("AISOTROPY_DATA_DIR", dir)

	// Corrupt the cost namespace in the store the runtime will open.
	wm, err := memory.Open(dir, 10, memory.NoopSummarizer{})
	if err != nil {
		t.Fatalf("open working memory: %v", err)
	}
	if err := wm.NewKV(costTotalsKeyPrefix).Store(ctx, map[string][]byte{"acme": []byte("{not json")}); err != nil {
		t.Fatalf("seed corrupt totals: %v", err)
	}
	if err := wm.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))
	rt, err := app.ensureRuntime()
	if err == nil {
		rt.close(ctx)
		t.Fatal("the runtime booted over unreadable cost totals — every tenant just got a fresh budget")
	}
	if !strings.Contains(err.Error(), "cost meter") {
		t.Errorf("boot error = %v, want it to name the cost meter", err)
	}
}
