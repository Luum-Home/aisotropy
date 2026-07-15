// Package gateway is Aisotropy's model gateway: a rules/policy-based router
// that decides WHICH model client should handle a request, and then resolves
// that decision to a concrete llm.Client.
//
// This is the "Engine-X for models" the book's stage-3 describes — a front
// door that sits in front of a set of provider connections and shapes traffic
// (cheap vs. premium, budget-aware, capability-aware, explicit override) by
// policy rather than by wiring each caller to a single hard-coded client.
//
// # Deliberately NOT an ML difficulty predictor
//
// The original ambition (Bet 4, "compression-difficulty routing": predict a
// prompt's difficulty from how compressible it is, then send hard prompts to
// a premium model) was REFUTED — a trivial len(prompt) baseline beat the
// compressibility signal, so the extra machinery earned nothing. This gateway
// is therefore intentionally RULES / COST / CAPABILITY based, not a learned
// difficulty model. That is not a downgrade: it matches the book's framing
// that "the logic [in the gateway] can be as complex as you want" — the value
// is in the seam (one place that decides routing), not in a clever predictor.
// Every policy here is transparent, data-driven, and deterministic, which is
// exactly what a production traffic-shaping layer wants.
//
// # Shape of the package
//
//   - route.go      — the Request / Decision value types and the Policy seam.
//   - policies.go   — reference policies: RulesPolicy (ordered, data-driven
//     rules), CostAwarePolicy (budget/spend request-shaping), and ChainPolicy
//     (try policies in order, first decision wins).
//   - gateway.go    — the Gateway: a name->llm.Client registry that turns a
//     Decision.Target into a concrete client, failing safe to a configured
//     default when the target is unknown.
//
// # Relationship to internal/llm/degrade.go
//
// llm.DegradeAt is QUOTA-PRESSURE degradation: as a tenant nears its rate
// limit, downgrade the model to protect the bucket. CostAwarePolicy here is
// REQUEST-SHAPING: given a budget and cumulative spend, steer requests to the
// cheap target once spend crosses a threshold. They are complementary — one
// reacts to the token bucket, the other to the money budget — and can both be
// active without conflict.
//
// The package is standalone and side-effect free apart from best-effort
// structured logging on the fail-safe path. It is not wired into the HTTP
// handlers by this package; wiring is the orchestrator's responsibility.
package gateway
