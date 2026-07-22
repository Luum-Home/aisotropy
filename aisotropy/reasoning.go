// Package aisotropy — the reasoning-capability aliases (ADR-0061 surface).
//
// Why these exist: a consumer who wraps the client they pass to WithLLM in a
// decorator can only FORWARD a capability they can NAME. The façade already
// re-exported ToolCaller (graph.go), but not ReasoningToolCaller — so a
// consumer decorator, however careful, could not declare or forward the
// reasoning capability and silently downgraded it (observed in the arena's
// aisotropy adapter, whose usage decorator documented exactly this loss:
// "ReasoningToolCaller is NOT forwarded — the facade does not re-export it").
// These aliases close that hole. They hand back the REAL internal types (the
// same open-core discipline as ToolCaller/ToolSpec/ToolCallResult), so a
// consumer's decorator satisfies the genuine runtime assertion, not a shadow
// copy.
package aisotropy

import "github.com/Luum-Home/aisotropy/internal/llm"

// ReasoningToolCaller is the OPTIONAL capability a consumer's LLMClient MAY
// additionally implement (beyond ToolCaller) when its provider can carry
// reasoning ("thinking") across tool-call steps. It is discovered by type
// assertion at the call site, exactly like ToolCaller — which means a consumer
// DECORATOR around a reasoning-capable client (e.g. NewOpenAICompatLLM's
// return value) MUST forward CallToolsReasoning too, or the capability is
// silently lost through the wrap (the Go interface-upgrade-loss trap): the
// agent loop then still runs, but cross-step chain-of-thought is dropped.
// Re-exported precisely so such a decorator CAN forward it.
type ReasoningToolCaller = llm.ReasoningToolCaller

// ReasoningState is the opaque intra-task reasoning carry-forward threaded
// through CallToolsReasoning: a VALUE on the request goroutine's stack, never
// shared across tenants, never persisted. Consumers shuttle it between steps
// (or construct one via NewTextReasoningState); its Blocks are opaque by
// design — only the producing client interprets them.
type ReasoningState = llm.ReasoningState

// ReasoningBlock is one opaque unit of carried reasoning inside a
// ReasoningState (its fields are unexported by design — provider-native
// payloads such as signed thinking blocks must pass through verbatim, never be
// inspected or rewritten by consumers). Re-exported so a consumer can name the
// element type of ReasoningState.Blocks.
type ReasoningBlock = llm.ReasoningBlock

// NewTextReasoningState builds a ReasoningState carrying a single plain-text
// reasoning block — the shape a StreamChat-only capture path produces. It is
// the one public constructor for a non-empty ReasoningState, re-exported for
// consumers implementing their own ReasoningToolCaller or a capture-and-carry
// decorator. Empty text yields an empty state.
func NewTextReasoningState(text string) ReasoningState {
	return llm.NewTextReasoningState(text)
}
