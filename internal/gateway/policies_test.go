package gateway

import (
	"context"
	"testing"
)

func TestRulesPolicy(t *testing.T) {
	// A representative production-shaped routing table, in priority order:
	//   1. images  -> premium (capability)
	//   2. FAQ hit -> cheap    (cheap-path)
	//   3. big     -> premium  (size)
	// default: cheap.
	policy := RulesPolicy{
		Rules: []Rule{
			ImageRule("premium"),
			KeywordRule("cheap", "refund", "hours", "password reset"),
			LargePromptRule("premium", 1000),
		},
		Default: "cheap",
	}

	tests := []struct {
		name       string
		req        Request
		wantTarget string
	}{
		{
			name:       "image routes to premium",
			req:        Request{HasImage: true, Prompt: "what is in this picture"},
			wantTarget: "premium",
		},
		{
			name:       "FAQ keyword routes to cheap",
			req:        Request{Prompt: "How do I request a REFUND please"},
			wantTarget: "cheap",
		},
		{
			name:       "multi-word keyword matches case-insensitively",
			req:        Request{Prompt: "I need a Password Reset link"},
			wantTarget: "cheap",
		},
		{
			name:       "large prompt routes to premium",
			req:        Request{PromptTokensEstimate: 5000, Prompt: "write an essay"},
			wantTarget: "premium",
		},
		{
			name:       "image wins over keyword (order matters)",
			req:        Request{HasImage: true, Prompt: "refund"},
			wantTarget: "premium",
		},
		{
			name:       "no rule matches -> default cheap",
			req:        Request{Prompt: "hello there", PromptTokensEstimate: 5},
			wantTarget: "cheap",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := policy.Select(context.Background(), tt.req)
			if got.Target != tt.wantTarget {
				t.Errorf("Select() target = %q, want %q (reason: %s)", got.Target, tt.wantTarget, got.Reason)
			}
			if got.Reason == "" {
				t.Error("Decision.Reason should never be empty")
			}
		})
	}
}

func TestRulesPolicyEmptyDefault(t *testing.T) {
	policy := RulesPolicy{Rules: nil, Default: ""}
	got := policy.Select(context.Background(), Request{Prompt: "x"})
	if got.Target != "" {
		t.Errorf("expected empty target with no rules and empty default, got %q", got.Target)
	}
}

func TestOverridePolicy(t *testing.T) {
	p := OverridePolicy{}

	got := p.Select(context.Background(), Request{UISelector: "premium"})
	if got.Target != "premium" {
		t.Errorf("expected override to premium, got %q", got.Target)
	}

	got = p.Select(context.Background(), Request{UISelector: ""})
	if got.Target != "" {
		t.Errorf("expected abstain (empty target) with no selector, got %q", got.Target)
	}
}

func TestCostAwarePolicy(t *testing.T) {
	tests := []struct {
		name       string
		policy     CostAwarePolicy
		req        Request
		wantTarget string
	}{
		{
			name:       "over budget routes to cheap",
			policy:     CostAwarePolicy{BudgetUSD: 10, Cheap: "cheap"},
			req:        Request{CostSpentUSD: 12},
			wantTarget: "cheap",
		},
		{
			name:       "exactly at budget routes to cheap",
			policy:     CostAwarePolicy{BudgetUSD: 10, Cheap: "cheap"},
			req:        Request{CostSpentUSD: 10},
			wantTarget: "cheap",
		},
		{
			name:       "under budget abstains",
			policy:     CostAwarePolicy{BudgetUSD: 10, Cheap: "cheap"},
			req:        Request{CostSpentUSD: 3},
			wantTarget: "",
		},
		{
			name:       "zero budget disables policy (abstains)",
			policy:     CostAwarePolicy{BudgetUSD: 0, Cheap: "cheap"},
			req:        Request{CostSpentUSD: 999},
			wantTarget: "",
		},
		{
			name:       "negative budget disables policy (abstains)",
			policy:     CostAwarePolicy{BudgetUSD: -1, Cheap: "cheap"},
			req:        Request{CostSpentUSD: 999},
			wantTarget: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.policy.Select(context.Background(), tt.req)
			if got.Target != tt.wantTarget {
				t.Errorf("Select() target = %q, want %q", got.Target, tt.wantTarget)
			}
		})
	}
}

func TestChainPolicy(t *testing.T) {
	// Realistic chain: explicit UI override first, then cost-shaping, then the
	// capability/size rules, then default. First non-empty target wins.
	chain := ChainPolicy{
		Policies: []Policy{
			OverridePolicy{},
			CostAwarePolicy{BudgetUSD: 5, Cheap: "cheap"},
			RulesPolicy{
				Rules:   []Rule{ImageRule("premium")},
				Default: "standard",
			},
		},
	}

	tests := []struct {
		name       string
		req        Request
		wantTarget string
	}{
		{
			name:       "UI override wins over everything",
			req:        Request{UISelector: "premium", HasImage: true, CostSpentUSD: 100},
			wantTarget: "premium",
		},
		{
			name:       "cost shaping wins when over budget and no override",
			req:        Request{CostSpentUSD: 50, HasImage: true},
			wantTarget: "cheap",
		},
		{
			name:       "capability rule applies when under budget, no override",
			req:        Request{CostSpentUSD: 1, HasImage: true},
			wantTarget: "premium",
		},
		{
			name:       "falls through to rules default",
			req:        Request{CostSpentUSD: 1, Prompt: "hi"},
			wantTarget: "standard",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chain.Select(context.Background(), tt.req)
			if got.Target != tt.wantTarget {
				t.Errorf("Select() target = %q, want %q (reason: %s)", got.Target, tt.wantTarget, got.Reason)
			}
		})
	}
}

func TestChainPolicyAllAbstain(t *testing.T) {
	chain := ChainPolicy{
		Policies: []Policy{
			OverridePolicy{},
			CostAwarePolicy{BudgetUSD: 10, Cheap: "cheap"},
		},
	}
	// No override, under budget -> everyone abstains -> empty target.
	got := chain.Select(context.Background(), Request{CostSpentUSD: 1})
	if got.Target != "" {
		t.Errorf("expected empty target when all abstain, got %q", got.Target)
	}
}

func TestChainPolicyNilMemberSkipped(t *testing.T) {
	chain := ChainPolicy{
		Policies: []Policy{nil, OverridePolicy{}, nil},
	}
	got := chain.Select(context.Background(), Request{UISelector: "premium"})
	if got.Target != "premium" {
		t.Errorf("nil members should be skipped, got %q", got.Target)
	}
}

func TestChainPolicyEmpty(t *testing.T) {
	got := ChainPolicy{}.Select(context.Background(), Request{})
	if got.Target != "" {
		t.Errorf("empty chain should abstain, got %q", got.Target)
	}
}
