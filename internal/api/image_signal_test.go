package api

import (
	"context"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/costmeter"
	"github.com/Luum-Home/aisotropy/internal/gateway"
	"github.com/Luum-Home/aisotropy/internal/llm"
)

// TestSelectClient_ImageSignalRoutesToVisionTarget proves the image presence
// signal (ChatRequest.Images -> selectClient's hasImage -> gateway.Request.
// HasImage) activates the capability-based ImageRule, routing an image-bearing
// turn to the vision/premium target, while an image-less turn is left unchanged.
//
// The gateway chain mirrors aisotropy.buildModelGateway EXACTLY: OverridePolicy
// first (explicit UI selection), then the ImageRule (capability), then the
// cost-budget rule — so the ImageRule fires BEFORE cost-budget, as wired in
// production.
func TestSelectClient_ImageSignalRoutesToVisionTarget(t *testing.T) {
	primary := namedClient{name: "primary"}
	vision := namedClient{name: "vision"}
	cheap := namedClient{name: "cheap"}

	gw := gateway.New(gateway.ChainPolicy{Policies: []gateway.Policy{
		gateway.OverridePolicy{},
		gateway.RulesPolicy{Rules: []gateway.Rule{gateway.ImageRule("vision")}},
		gateway.CostAwarePolicy{BudgetUSD: 1.0, Cheap: "cheap"},
	}}, nil)
	for name, c := range map[string]llm.Client{"primary": primary, "vision": vision, "cheap": cheap} {
		if err := gw.Register(name, c); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	if err := gw.SetDefault("primary"); err != nil {
		t.Fatalf("set default: %v", err)
	}
	ctx := context.Background()

	// Image signal present -> the ImageRule routes to the vision target.
	if got := selectClient(ctx, gw, primary, nil, "acme", "alice", "describe this", "", true); got.Name() != "vision" {
		t.Errorf("with image signal: routed to %q, want vision", got.Name())
	}

	// No image signal -> routing unchanged (falls through to the default primary;
	// no preference, under budget).
	if got := selectClient(ctx, gw, primary, nil, "acme", "alice", "describe this", "", false); got.Name() != "primary" {
		t.Errorf("without image signal: routed to %q, want primary (unchanged)", got.Name())
	}
}

// TestSelectClient_ImageRuleFiresBeforeCostBudget proves the ImageRule takes
// precedence over the cost-budget shaping rule: even an OVER-budget tenant (which
// the cost rule would otherwise shape to the cheap target) is routed to the vision
// target when the request carries an image, because a multimodal turn must not be
// degraded to the cheap text model.
func TestSelectClient_ImageRuleFiresBeforeCostBudget(t *testing.T) {
	primary := namedClient{name: "primary"}
	vision := namedClient{name: "vision"}
	cheap := namedClient{name: "cheap"}

	gw := gateway.New(gateway.ChainPolicy{Policies: []gateway.Policy{
		gateway.OverridePolicy{},
		gateway.RulesPolicy{Rules: []gateway.Rule{gateway.ImageRule("vision")}},
		gateway.CostAwarePolicy{BudgetUSD: 1.0, Cheap: "cheap"},
	}}, nil)
	for name, c := range map[string]llm.Client{"primary": primary, "vision": vision, "cheap": cheap} {
		if err := gw.Register(name, c); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	if err := gw.SetDefault("primary"); err != nil {
		t.Fatalf("set default: %v", err)
	}

	meter := costmeter.New()
	meter.Record("acme", 0, 0, 2.0) // cumulative 2.0 >= 1.0 budget
	ctx := context.Background()

	// Over budget WITHOUT an image -> cost rule shapes to cheap.
	if got := selectClient(ctx, gw, primary, meter, "acme", "alice", "hi", "", false); got.Name() != "cheap" {
		t.Errorf("over budget, no image: routed to %q, want cheap (cost shaping)", got.Name())
	}
	// Over budget WITH an image -> ImageRule wins (fires before cost) -> vision.
	if got := selectClient(ctx, gw, primary, meter, "acme", "alice", "hi", "", true); got.Name() != "vision" {
		t.Errorf("over budget, with image: routed to %q, want vision (ImageRule beats cost)", got.Name())
	}
}
