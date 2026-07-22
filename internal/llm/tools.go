package llm

import (
	"context"
	"encoding/json"
)

// jsonSchemaTypeObject is the JSON Schema "type": "object" literal, shared by
// every place in this package (and its tests) that builds or asserts a
// permissive/fallback object schema for a tool's parameters.
const jsonSchemaTypeObject = "object"

// jsonKeyType is the "type" map key shared by every hand-built JSON Schema /
// provider tool-envelope literal in this package.
const jsonKeyType = "type"

// ToolSpec describes one tool offered to a ToolCaller. The model sees Name +
// Description (+ Parameters when present) and may return a ToolCall naming it.
// It is intentionally minimal and expressed in Aisotropy's own vocabulary — no
// provider-specific (OpenAI/Anthropic) tool schema leaks into this seam, per
// ADR-0012's requirement to keep the public contract free of provider-isms and
// ADR-0009's provider-agnostic Client seam.
type ToolSpec struct {
	// Name is the tool identifier the model must use verbatim when calling it.
	Name string
	// Description is the human/LLM-facing explanation of what the tool does.
	Description string
	// Parameters is the JSON Schema (draft 2020-12, as produced by SchemaFor)
	// describing the tool's argument object. It is OPTIONAL: an empty/nil
	// Parameters means "no declared argument shape" and callers advertise a
	// permissive object, preserving the ADR-0012 minimal seam. When set, it is
	// the struct-as-contract for tool INPUTS (ADR-0016 lever #1 / typed-tool-args
	// SDD): the schema the model is shown and, later, the shape its arguments are
	// validated against before a typed handler runs. Provider-agnostic JSON
	// Schema bytes only — no OpenAI/Anthropic tool envelope.
	Parameters json.RawMessage
}

// ToolCall is a single tool invocation a ToolCaller's model chose: the tool
// Name, its string-keyed Args (compat/display), and RawArgs, the model's
// arguments as raw JSON. The Name MUST be re-validated by the caller against the
// offered tool set before execution — a ToolCaller implementation (or an
// adversarial completion) that names a tool outside the offered set must never
// reach execution (see ToolCaller's security note).
//
// Args is the flattened string-keyed view kept for backward compatibility and
// the map[string]string ToolExecutor seam. RawArgs (ADR-0016 lever #1 /
// typed-tool-args SDD) preserves the model's arguments as the exact JSON it
// produced, so a typed tool can decode nested/typed fields that would be lost by
// the string flattening. RawArgs is optional: when empty, consumers fall back to
// Args. A ToolCaller SHOULD populate both.
type ToolCall struct {
	Name    string
	Args    map[string]string
	RawArgs json.RawMessage
}

// ToolCallResult is what CallTools returns for one turn: the tool calls the
// model wants executed (at least one when it chose to use a tool), plus any
// free-text Message it produced. An empty Calls slice means "no tool needed,
// answer directly" — the terminal state of an agent loop.
type ToolCallResult struct {
	Calls   []ToolCall
	Message string
	// Usage, when non-nil, carries the provider-reported token counts for this
	// tool-call turn. Unlike StreamChat (where usage lands on the terminal Token),
	// CallTools makes a single NON-streaming request whose response carries the
	// usage block by default, so it is surfaced here directly. It is the SAME
	// llm.Usage type Token.Usage uses (never a second usage type), so a consumer
	// accounts for streamed and tool-calling spend through one shape. nil when the
	// provider returned no usage block (e.g. a stub or a choice-less response).
	Usage *Usage
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
