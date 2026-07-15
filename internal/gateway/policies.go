package gateway

import (
	"context"
	"strings"
)

// Rule is one ordered, data-driven entry in a RulesPolicy. The first Rule
// whose When predicate returns true wins, and its Target/Reason become the
// Decision. Keeping a rule a (predicate, target, reason) triple — rather than
// hard-coded if/else in Select — is what makes the policy composable and
// configurable: the routing table is data, not control flow.
type Rule struct {
	// When is the predicate. It must be pure (see Policy's contract).
	When func(req Request) bool
	// Target is the registry name to route to when When matches.
	Target string
	// Reason is a short, stable explanation for logs/audit.
	Reason string
}

// RulesPolicy evaluates an ordered list of Rules and returns the first match.
// If no rule matches it returns the configured Default target. This is the
// workhorse policy: capability rules (HasImage -> premium), cheap-path rules
// (FAQ keyword -> cheap), size rules (big prompt -> premium), and explicit UI
// overrides are all just Rules in the slice, in priority order.
type RulesPolicy struct {
	// Rules are evaluated in order; first match wins.
	Rules []Rule
	// Default is the target used when no rule matches. If empty, Select returns
	// an empty Decision.Target (which the Gateway resolves to its own default).
	Default string
}

// Select implements Policy.
func (p RulesPolicy) Select(_ context.Context, req Request) Decision {
	for _, r := range p.Rules {
		if r.When != nil && r.When(req) {
			return Decision{Target: r.Target, Reason: r.Reason}
		}
	}
	return Decision{Target: p.Default, Reason: "rules: no rule matched, using default"}
}

// --- Rule constructors: reusable, composable predicates -------------------
//
// These build the common rules so callers assemble a routing table from data
// instead of writing predicates by hand. All are pure.

// ImageRule routes any request carrying an image to target (typically the
// premium, multimodal-capable client). Capability-based routing.
func ImageRule(target string) Rule {
	return Rule{
		When:   func(req Request) bool { return req.HasImage },
		Target: target,
		Reason: "capability: request has an image, needs multimodal model",
	}
}

// Note on UI-selector overrides: because a Rule's Target is a static string
// but Request.UISelector is dynamic (it comes from the request, not the
// table), an override cannot be expressed as a RulesPolicy Rule. It is a
// standalone OverridePolicy instead — chain it first so an explicit UI choice
// wins over the automatic rules.

// KeywordRule routes to target when the prompt (case-insensitively) contains
// any of the given keywords — e.g. an FAQ/canned-answer set that a cheap model
// handles fine. Matching is substring-based and lowercased for stability.
func KeywordRule(target string, keywords ...string) Rule {
	lowered := make([]string, len(keywords))
	for i, k := range keywords {
		lowered[i] = strings.ToLower(k)
	}
	return Rule{
		When: func(req Request) bool {
			p := strings.ToLower(req.Prompt)
			for _, k := range lowered {
				if k != "" && strings.Contains(p, k) {
					return true
				}
			}
			return false
		},
		Target: target,
		Reason: "cheap-path: prompt matched a known keyword/FAQ term",
	}
}

// LargePromptRule routes prompts whose estimated token count exceeds threshold
// to target (typically premium, which has the larger/better context handling).
func LargePromptRule(target string, threshold int) Rule {
	return Rule{
		When:   func(req Request) bool { return req.PromptTokensEstimate > threshold },
		Target: target,
		Reason: "size: prompt exceeds token threshold",
	}
}

// OverridePolicy is a standalone policy that honors Request.UISelector as a
// hard target override. It exists as its own policy (rather than a RulesPolicy
// Rule) because the override target is dynamic — it comes from the request,
// not from a static table. Chain it FIRST so an explicit UI choice wins over
// the automatic rules.
type OverridePolicy struct{}

// Select implements Policy: if UISelector is set, route there; otherwise
// abstain (empty Target) so the next policy in a chain decides.
func (OverridePolicy) Select(_ context.Context, req Request) Decision {
	if req.UISelector != "" {
		return Decision{Target: req.UISelector, Reason: "override: explicit UI selector"}
	}
	return Decision{Target: "", Reason: "override: no UI selector, abstaining"}
}

// CostAwarePolicy is REQUEST-SHAPING by money budget (contrast with
// llm.DegradeAt, which shapes by quota/token-bucket pressure — see doc.go).
// Once cumulative spend crosses BudgetUSD, it routes to Cheap; below the
// budget it abstains (empty Target) so a downstream policy — or the Gateway's
// default — picks the normal target.
//
// A non-positive BudgetUSD disables the policy (it always abstains), so a
// caller can wire it in before any budget is configured without special
// casing.
type CostAwarePolicy struct {
	// BudgetUSD is the spend ceiling for the request's budget scope. <= 0
	// disables the policy.
	BudgetUSD float64
	// Cheap is the target to route to once the budget is crossed.
	Cheap string
}

// Select implements Policy.
func (p CostAwarePolicy) Select(_ context.Context, req Request) Decision {
	if p.BudgetUSD <= 0 {
		return Decision{Target: "", Reason: "cost: no budget configured, abstaining"}
	}
	if req.CostSpentUSD >= p.BudgetUSD {
		return Decision{Target: p.Cheap, Reason: "cost: spend crossed budget, shaping to cheap target"}
	}
	return Decision{Target: "", Reason: "cost: within budget, abstaining"}
}

// ChainPolicy tries its member policies in order and returns the first
// Decision with a non-empty Target. If every policy abstains, it returns the
// last abstaining Decision (empty Target), which the Gateway resolves to its
// default. This is how capability, override, and cost policies compose into
// one routing pipeline without any single policy needing to know about the
// others.
type ChainPolicy struct {
	Policies []Policy
}

// Select implements Policy.
func (c ChainPolicy) Select(ctx context.Context, req Request) Decision {
	last := Decision{Reason: "chain: no policies configured"}
	for _, p := range c.Policies {
		if p == nil {
			continue
		}
		d := p.Select(ctx, req)
		if d.Target != "" {
			return d
		}
		last = d
	}
	return last
}
