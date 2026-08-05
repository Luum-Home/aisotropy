package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/costmeter"
	"github.com/Luum-Home/aisotropy/internal/gateway"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
)

// CHARACTERIZATION of what the cost budget does and does not guarantee. The
// budget gateway.CostAwarePolicy enforces is read from costmeter through
// selectClient, so it is only ever as good as that meter's memory.
//
// Half of what this file used to pin is now FIXED. costmeter.NewPersistent
// loads and persists the per-tenant totals (aisotropyd wires it over the
// working-memory BadgerDB), so a restart no longer resets the budget when a
// durable store is configured — TestCostBudget_SurvivesRestartWithDurableMeter
// is the flipped expectation the old note asked for. What is NOT fixed, and
// stays pinned here, is the replica multiplication: each process keeps its own
// store, so N replicas still grant N x BudgetUSD.
//
// The volatile constructor (costmeter.New) keeps the old behaviour, and the
// tests below that use it remain true statements about IT — including the
// zero-config default, where the working-memory store is in-memory and
// costmeter deliberately refuses to attach rather than pretend to persist.

// budgetGateway builds a gateway whose only rule is a cost budget: under budget
// it resolves to the default target, over budget it shapes to the cheap one.
func budgetGateway(t *testing.T, budgetUSD float64) *gateway.Gateway {
	t.Helper()
	gw := gateway.New(gateway.CostAwarePolicy{BudgetUSD: budgetUSD, Cheap: "cheap"}, nil)
	if err := gw.Register("premium", tokenClient{name: "premium", token: "PREMIUM"}); err != nil {
		t.Fatalf("register premium: %v", err)
	}
	if err := gw.Register("cheap", tokenClient{name: "cheap", token: "CHEAP"}); err != nil {
		t.Fatalf("register cheap: %v", err)
	}
	if err := gw.SetDefault("premium"); err != nil {
		t.Fatalf("set default: %v", err)
	}
	return gw
}

// TestCostBudget_ShapesDownOnceSpendCrosses is the control: within one process
// the budget does work.
func TestCostBudget_ShapesDownOnceSpendCrosses(t *testing.T) {
	meter := costmeter.New()
	gw := budgetGateway(t, 1.0)
	fallback := tokenClient{name: "fallback", token: "FALLBACK"}

	if got := selectClient(context.Background(), gw, fallback, meter, "acme", "alice", "hi", "", false); got.Name() != "premium" {
		t.Fatalf("with no spend recorded the tenant routes to %q, want premium", got.Name())
	}

	// Spend past the budget.
	meter.RecordUsage("acme", 0, 0, 0, 5.0)

	if got := selectClient(context.Background(), gw, fallback, meter, "acme", "alice", "hi", "", false); got.Name() != "cheap" {
		t.Fatalf("over budget the tenant routes to %q, want cheap", got.Name())
	}
}

// TestCostBudget_ResetsWithTheProcess pins what a VOLATILE meter still does: a
// restarted process hands the tenant a fresh budget. A new Meter IS the restart.
// This is the zero-config default (no AISOTROPY_DATA_DIR, nothing durable to
// persist into), so the behaviour is still reachable and still worth pinning —
// but it is no longer the only option; see the durable case below.
func TestCostBudget_ResetsWithTheProcess(t *testing.T) {
	gw := budgetGateway(t, 1.0)
	fallback := tokenClient{name: "fallback", token: "FALLBACK"}

	beforeRestart := costmeter.New()
	beforeRestart.RecordUsage("acme", 0, 0, 0, 5.0) // well past the $1 budget
	if got := selectClient(context.Background(), gw, fallback, beforeRestart, "acme", "alice", "hi", "", false); got.Name() != "cheap" {
		t.Fatalf("pre-restart routing = %q, want cheap (the tenant is over budget)", got.Name())
	}

	afterRestart := costmeter.New() // the process came back up, with nothing to reload
	got := selectClient(context.Background(), gw, fallback, afterRestart, "acme", "alice", "hi", "", false)
	if got.Name() != "premium" {
		t.Fatalf("post-restart routing = %q, want %q: a VOLATILE meter has no memory of the spend, "+
			"so the budget abstains", got.Name(), "premium")
	}
}

// TestCostBudget_SurvivesRestartWithDurableMeter is the flip: with a durable
// store behind the meter, the tenant that blew its budget is STILL over budget
// after the process comes back, so the routing decision holds instead of
// resetting. This is the request-path proof of the persistence fix — the
// accounting matters here because it changes which model the next turn hits.
func TestCostBudget_SurvivesRestartWithDurableMeter(t *testing.T) {
	ctx := context.Background()
	gw := budgetGateway(t, 1.0)
	fallback := tokenClient{name: "fallback", token: "FALLBACK"}

	wm, err := memory.Open(filepath.Join(t.TempDir(), "wm"), 10, memory.NoopSummarizer{})
	if err != nil {
		t.Fatalf("open working memory: %v", err)
	}
	defer func() { _ = wm.Close() }()
	kv := wm.NewKV("cost:totals:")

	beforeRestart, err := costmeter.NewPersistent(ctx, kv, 0)
	if err != nil {
		t.Fatalf("NewPersistent: %v", err)
	}
	beforeRestart.RecordUsage("acme", 0, 0, 0, 5.0) // well past the $1 budget
	if got := selectClient(ctx, gw, fallback, beforeRestart, "acme", "alice", "hi", "", false); got.Name() != "cheap" {
		t.Fatalf("pre-restart routing = %q, want cheap (the tenant is over budget)", got.Name())
	}
	if err := beforeRestart.Close(ctx); err != nil { // graceful shutdown flushes
		t.Fatalf("Close: %v", err)
	}

	afterRestart, err := costmeter.NewPersistent(ctx, kv, 0)
	if err != nil {
		t.Fatalf("NewPersistent after restart: %v", err)
	}
	defer func() { _ = afterRestart.Close(ctx) }()
	if got := selectClient(ctx, gw, fallback, afterRestart, "acme", "alice", "hi", "", false); got.Name() != "cheap" {
		t.Fatalf("post-restart routing = %q, want cheap: the restarted process handed the tenant a fresh budget", got.Name())
	}
}

// TestCostBudget_IsMultipliedByReplicaCount pins limit (2): two replicas are two
// independent meters, so each tenant gets the full budget on each one and the
// effective ceiling is N x BudgetUSD.
func TestCostBudget_IsMultipliedByReplicaCount(t *testing.T) {
	gw := budgetGateway(t, 1.0)
	fallback := tokenClient{name: "fallback", token: "FALLBACK"}

	replicaA, replicaB := costmeter.New(), costmeter.New()
	replicaA.RecordUsage("acme", 0, 0, 0, 5.0) // acme blew the budget on replica A

	if got := selectClient(context.Background(), gw, fallback, replicaA, "acme", "alice", "hi", "", false); got.Name() != "cheap" {
		t.Fatalf("replica A routing = %q, want cheap", got.Name())
	}
	got := selectClient(context.Background(), gw, fallback, replicaB, "acme", "alice", "hi", "", false)
	if got.Name() != "premium" {
		t.Fatalf("replica B routing = %q; this test documents the KNOWN limit that it is %q — "+
			"replica B has never seen acme's spend, so the same tenant gets a second full budget",
			got.Name(), "premium")
	}
}

// TestCostBudget_NilMeterMeansZeroSpend pins the nil-meter degradation the
// routing seam documents: no meter reads as no spend, so the budget abstains.
func TestCostBudget_NilMeterMeansZeroSpend(t *testing.T) {
	gw := budgetGateway(t, 1.0)
	var fallback llm.Client = tokenClient{name: "fallback", token: "FALLBACK"}

	if got := selectClient(context.Background(), gw, fallback, nil, "acme", "alice", "hi", "", false); got.Name() != "premium" {
		t.Fatalf("nil-meter routing = %q, want premium (no spend known)", got.Name())
	}
}
