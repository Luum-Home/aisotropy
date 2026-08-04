package api

import (
	"context"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/costmeter"
	"github.com/Luum-Home/aisotropy/internal/gateway"
	"github.com/Luum-Home/aisotropy/internal/llm"
)

// CHARACTERIZATION, not a fix. These tests pin a KNOWN, UNFIXED limit so it stays
// visible and cannot be silently re-assumed away: the cost budget that
// gateway.CostAwarePolicy enforces is read from the in-process costmeter, so it
// resets on every restart and is multiplied by the replica count. They pass
// against the current code by construction — they exist to make the defect
// executable and to give a future durable meter a test to flip, not to prove a
// change. See selectClient's "KNOWN LIMIT" note for why a bounded local fix
// (persisting into the working-memory BadgerDB) would not honestly close either
// half of the problem.

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

// TestCostBudget_ResetsWithTheProcess pins limit (1): a restarted process hands
// the tenant a fresh budget. A new Meter IS the restart — the meter holds no
// state across process restarts, so a fresh one is exactly what boot produces.
func TestCostBudget_ResetsWithTheProcess(t *testing.T) {
	gw := budgetGateway(t, 1.0)
	fallback := tokenClient{name: "fallback", token: "FALLBACK"}

	beforeRestart := costmeter.New()
	beforeRestart.RecordUsage("acme", 0, 0, 0, 5.0) // well past the $1 budget
	if got := selectClient(context.Background(), gw, fallback, beforeRestart, "acme", "alice", "hi", "", false); got.Name() != "cheap" {
		t.Fatalf("pre-restart routing = %q, want cheap (the tenant is over budget)", got.Name())
	}

	afterRestart := costmeter.New() // the process came back up
	got := selectClient(context.Background(), gw, fallback, afterRestart, "acme", "alice", "hi", "", false)
	if got.Name() != "premium" {
		t.Fatalf("post-restart routing = %q; this test documents the KNOWN limit that it is %q — "+
			"if the meter became durable, flip this expectation and delete the KNOWN LIMIT note on selectClient",
			got.Name(), "premium")
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
