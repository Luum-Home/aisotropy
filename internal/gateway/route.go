package gateway

import "context"

// Request is the routing-relevant projection of an incoming chat request. It
// carries only the signals a Policy needs to decide a target — never the
// llm.Client, never the response stream. Keeping this a plain value type (no
// pointers to mutable state, no I/O) is what makes Policy.Select deterministic
// and trivially testable.
//
// It is deliberately decoupled from internal/api's request structs: the
// gateway must not depend on the HTTP layer. The orchestrator that wires this
// in is responsible for projecting its handler input down to a Request.
type Request struct {
	// Prompt is the user prompt text. Policies may inspect it (e.g. keyword
	// matching) but should treat it as opaque, untrusted input.
	Prompt string

	// PromptTokensEstimate is a caller-supplied estimate of the prompt size in
	// tokens. It is an estimate on purpose: the gateway routes BEFORE the model
	// call, so an exact provider-reported count is not yet available. A cheap
	// heuristic (e.g. len(prompt)/4) is a perfectly good input here — routing
	// on prompt size is exactly the len() baseline that beat the refuted
	// compressibility predictor (see doc.go).
	PromptTokensEstimate int

	// HasImage is true when the request carries at least one image part, i.e.
	// it needs a multimodal-capable (typically premium) model.
	HasImage bool

	// Tenant and User identify the caller. They are provided for tenant-scoped
	// policies (e.g. per-tenant budgets); the gateway itself does not enforce
	// isolation — that remains the job of ctx-propagated tenancy.Scope upstream.
	Tenant string
	User   string

	// UISelector is an explicit, user/UI-chosen target override (e.g. a model
	// picker in the product UI). When non-empty a policy may choose to honor it
	// as a hard override. It is a target NAME, matched against the Gateway's
	// registry — an unknown value still falls back safely at resolution time.
	UISelector string

	// CostSpentUSD is the cumulative spend attributable to this budget scope
	// (tenant or user, caller's choice) so far. CostAwarePolicy compares it to
	// a budget to decide whether to shape the request to the cheap target.
	CostSpentUSD float64
}

// Decision is the outcome of routing: the chosen target NAME plus a
// human-readable reason for observability/audit. Target is resolved to a
// concrete llm.Client by the Gateway; a Policy never touches clients itself,
// which keeps policy logic pure and the client registry the single place that
// knows about live connections.
type Decision struct {
	// Target is the registry name of the chosen client (e.g. "premium",
	// "cheap"). An empty Target means "no opinion" — ChainPolicy treats it as a
	// signal to try the next policy, and the Gateway resolves it to its default.
	Target string

	// Reason explains why this target was chosen, for logs and audit trails.
	Reason string
}

// Policy decides a routing Decision for a Request. Implementations MUST be
// pure and deterministic: no I/O, no wall-clock reads, no randomness. That
// contract is what lets the whole routing layer be table-tested and reasoned
// about. ctx is accepted for cancellation/deadline propagation and for future
// ctx-scoped policies, but a Policy must not block on it.
type Policy interface {
	Select(ctx context.Context, req Request) Decision
}
