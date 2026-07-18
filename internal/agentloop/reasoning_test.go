package agentloop

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// reasoningClient streams a reasoning delta plus an answer (a tool Decision on
// the first turn, a no-tool Decision afterwards) and records every prompt it is
// asked, so a test can assert the prior reasoning was reinjected on step 2. It
// is request-local per Run call.
type reasoningClient struct {
	mu      sync.Mutex
	prompts []string
	turn    int
}

func (c *reasoningClient) Name() string { return "reasoning-fake" }

func (c *reasoningClient) StreamChat(_ context.Context, _, prompt string) (<-chan llm.Token, error) {
	c.mu.Lock()
	c.prompts = append(c.prompts, prompt)
	turn := c.turn
	c.turn++
	c.mu.Unlock()

	ch := make(chan llm.Token, 3)
	go func() {
		defer close(ch)
		ch <- llm.Token{Reasoning: "cot-step-" + itoa(turn)}
		if turn == 0 {
			ch <- llm.Token{Text: `{"tool":"echo","args":{}}`, Done: true}
			return
		}
		ch <- llm.Token{Text: `{"tool":"","args":{}}`, Done: true}
	}()
	return ch, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return string(rune('0' + n))
}

// TestRun_ReasoningReinjectedAcrossSteps: the reasoning captured on step 1 must
// be reinjected as leading context into step 2's prompt (stateless carry), while
// NEVER contaminating the answer/Decision parsing. Request-local: the second
// Run below (fresh client) never sees the first's reasoning.
func TestRun_ReasoningReinjectedAcrossSteps(t *testing.T) {
	client := &reasoningClient{}
	exec := &fakeExecutor{result: "echoed"}
	loop := &Loop{Client: client, Registry: testRegistry(), Executor: exec, MaxSteps: 2}

	steps, err := loop.Run(context.Background(), []string{}, "start")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2 (tool then terminal): %+v", len(steps), steps)
	}
	client.mu.Lock()
	prompts := append([]string(nil), client.prompts...)
	client.mu.Unlock()
	if len(prompts) < 2 {
		t.Fatalf("expected >=2 model turns, got %d", len(prompts))
	}
	// Step 2's prompt must carry step 1's reasoning as reinjected leading context.
	if !strings.Contains(prompts[1], "cot-step-0") {
		t.Fatalf("prior reasoning was not reinjected into step 2:\n%s", prompts[1])
	}
	// The tool result path is preserved; the Decision on step 1 executed echo.
	if len(exec.calls) != 1 || exec.calls[0].name != "echo" {
		t.Fatalf("tool execution path broke: %+v", exec.calls)
	}
}

// TestRun_ReasoningNoOpWithoutReasoning confirms graceful degradation: a client
// that streams NO reasoning (the default fakeClient) makes reinjection a no-op —
// the loop behaves byte-for-byte as before.
func TestRun_ReasoningNoOpWithoutReasoning(t *testing.T) {
	client := &fakeClient{response: `{"tool":"","args":{}}`}
	exec := &fakeExecutor{}
	loop := &Loop{Client: client, Registry: testRegistry(), Executor: exec, MaxSteps: 3}

	steps, err := loop.Run(context.Background(), []string{}, "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(steps) != 1 || steps[0].Decision.Tool != "" {
		t.Fatalf("no-reasoning run must terminate with a single no-tool step: %+v", steps)
	}
}
