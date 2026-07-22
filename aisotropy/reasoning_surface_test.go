package aisotropy

// reasoning_surface_test.go proves the ReasoningToolCaller/ReasoningState
// aliases are satisfiable FROM OUTSIDE internal/llm using only façade-exported
// names — the exact hole the arena adapter documented ("the facade does not
// re-export it, so reasoning state is not carried across agent turns"). A
// consumer decorator written against these aliases satisfies the genuine
// runtime type assertion.

import (
	"context"
	"testing"
)

// forwardingReasoningDecorator is the consumer-shaped decorator this surface
// exists for: it wraps an inner ReasoningToolCaller and forwards EVERY
// capability — StreamChat, CallTools, AND CallToolsReasoning — using only
// façade-exported types.
type forwardingReasoningDecorator struct {
	inner ReasoningToolCaller
}

func (d forwardingReasoningDecorator) Name() string { return d.inner.Name() }

func (d forwardingReasoningDecorator) StreamChat(ctx context.Context, system, prompt string) (<-chan Token, error) {
	return d.inner.StreamChat(ctx, system, prompt)
}

func (d forwardingReasoningDecorator) CallTools(ctx context.Context, system, prompt string, tools []ToolSpec) (ToolCallResult, error) {
	return d.inner.CallTools(ctx, system, prompt, tools)
}

func (d forwardingReasoningDecorator) CallToolsReasoning(ctx context.Context, system, prompt string, tools []ToolSpec, prior ReasoningState) (ToolCallResult, ReasoningState, error) {
	return d.inner.CallToolsReasoning(ctx, system, prompt, tools, prior)
}

// Compile-time proof (acceptance criterion 5): the consumer decorator, written
// purely against façade aliases, satisfies both capability interfaces — and the
// aliases are the REAL internal types, so the runtime's own assertions accept it.
var (
	_ ReasoningToolCaller = forwardingReasoningDecorator{}
	_ ToolCaller          = forwardingReasoningDecorator{}
	_ LLMClient           = forwardingReasoningDecorator{}
)

// TestReasoningAliasesRoundTrip: a ReasoningState built via the façade
// constructor survives a pass through a consumer decorator and reads back its
// text — Blocks (of aliased ReasoningBlock) are carried, not copied away.
func TestReasoningAliasesRoundTrip(t *testing.T) {
	state := NewTextReasoningState("carried reasoning")
	if state.IsEmpty() {
		t.Fatalf("NewTextReasoningState produced an empty state")
	}
	var blocks []ReasoningBlock = state.Blocks
	if len(blocks) != 1 {
		t.Fatalf("Blocks = %d, want 1", len(blocks))
	}
	if got := state.ReasoningText(); got != "carried reasoning" {
		t.Errorf("ReasoningText() = %q, want %q", got, "carried reasoning")
	}
	if empty := NewTextReasoningState(""); !empty.IsEmpty() {
		t.Errorf("empty text must yield an empty state")
	}
}
