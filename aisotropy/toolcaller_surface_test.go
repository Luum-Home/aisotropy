package aisotropy

import (
	"context"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// streamOnlyClient is a StreamChat-only LLMClient — it deliberately does NOT
// implement llm.ToolCaller. It models the interface-upgrade-loss trap: a
// consumer wrapper that forwards only StreamChat and silently drops native
// tool-calling. Used to prove clientSupportsToolCalling flags it.
type streamOnlyClient struct{}

func (streamOnlyClient) Name() string { return "stream-only" }

func (streamOnlyClient) StreamChat(_ context.Context, _, _ string) (<-chan llm.Token, error) {
	ch := make(chan llm.Token)
	close(ch)
	return ch, nil
}

// TestNewOpenAICompatLLM_SatisfiesToolCaller is the guard test for Element B:
// the façade constructor MUST return a client that satisfies aisotropy.ToolCaller
// (and ReasoningToolCaller), so /chat/agent drives it through native
// function-calling. If a future refactor silently drops the capability, this
// fails.
func TestNewOpenAICompatLLM_SatisfiesToolCaller(t *testing.T) {
	c := NewOpenAICompatLLM("test-key", "some-model", "https://example.invalid/v1", "test")

	if _, ok := c.(ToolCaller); !ok {
		t.Fatal("NewOpenAICompatLLM's client does not satisfy aisotropy.ToolCaller — /chat/agent would fall back to the agentloop JSON path")
	}
	if _, ok := c.(llm.ReasoningToolCaller); !ok {
		t.Error("NewOpenAICompatLLM's client does not satisfy ReasoningToolCaller (stateless reasoning carry-forward, ADR-0061)")
	}
}

// TestClientSupportsToolCalling covers the Element D predicate that drives the
// boot-time warning: a native-tool-calling client is not flagged; a
// StreamChat-only one is.
func TestClientSupportsToolCalling(t *testing.T) {
	native := NewOpenAICompatLLM("k", "m", "https://example.invalid/v1", "native")
	if !clientSupportsToolCalling(native) {
		t.Error("clientSupportsToolCalling(native ToolCaller) = false, want true")
	}

	if clientSupportsToolCalling(streamOnlyClient{}) {
		t.Error("clientSupportsToolCalling(StreamChat-only) = true, want false — this is the client that silently downgrades /chat/agent")
	}
}
