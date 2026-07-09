package llm

import "context"

// ToolSpec describes one tool offered to a ToolCaller. The model sees Name +
// Description and may return a ToolCall naming it. It is intentionally minimal
// and expressed in Aisotropy's own vocabulary — no provider-specific
// (OpenAI/Anthropic) tool schema leaks into this seam, per ADR-0012's
// requirement to keep the public contract free of provider-isms and ADR-0009's
// provider-agnostic Client seam.
type ToolSpec struct {
	// Name is the tool identifier the model must use verbatim when calling it.
	Name string
	// Description is the human/LLM-facing explanation of what the tool does.
	Description string
}

// ToolCall is a single tool invocation a ToolCaller's model chose: the tool
// Name and its string-keyed Args. The Name MUST be re-validated by the caller
// against the offered tool set before execution — a ToolCaller implementation
// (or an adversarial completion) that names a tool outside the offered set must
// never reach execution (see ToolCaller's security note).
type ToolCall struct {
	Name string
	Args map[string]string
}

// ToolCallResult is what CallTools returns for one turn: the tool calls the
// model wants executed (at least one when it chose to use a tool), plus any
// free-text Message it produced. An empty Calls slice means "no tool needed,
// answer directly" — the terminal state of an agent loop.
type ToolCallResult struct {
	Calls   []ToolCall
	Message string
}

// ToolCaller is an OPTIONAL capability a Client MAY additionally implement when
// its underlying provider supports native function/tool calling (ADR-0012,
// Option C). It is discovered by type assertion at the call site
// (tc, ok := client.(ToolCaller)); a Client that does not implement it is
// entirely unaffected, and callers fall back to the structured-output
// agentloop path (internal/agentloop).
//
// Adding this interface does NOT change the Client interface: ADR-0009's narrow
// StreamChat seam, the multi-connection Registry, and per-request routing are
// all preserved. ToolCaller embeds Client, so a ToolCaller is always also a
// usable streaming Client.
//
// SECURITY (the invariant the homegrown agentloop already enforces, preserved
// here): the tools slice passed to CallTools MUST already be filtered to the
// tools the caller's scopes permit, and the returned ToolCall.Name MUST STILL
// be re-validated against that permitted set before execution. A provider that
// names a tool it was never offered (hallucination, prompt injection, or an
// adversarial completion) must never reach execution. There is no path from
// "not offered" to "executed".
type ToolCaller interface {
	Client

	// CallTools asks the model — using the provider's native tool-calling
	// API — to choose among tools (or none) for the given prompt. system is
	// the base system instruction; tools are the (already scope-filtered)
	// tools the model may call. Implementations MUST honor ctx cancellation.
	CallTools(ctx context.Context, system, prompt string, tools []ToolSpec) (ToolCallResult, error)
}
