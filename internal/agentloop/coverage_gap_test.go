package agentloop

import (
	"context"
	"errors"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// blockingClient opens a stream that never sends a token and never closes,
// so the only way collectText/collectTextReasoning can return is via the
// ctx.Done() select case — used to deterministically exercise that branch.
type blockingClient struct{}

func (blockingClient) Name() string { return "blocking" }

func (blockingClient) StreamChat(_ context.Context, _, _ string) (<-chan llm.Token, error) {
	return make(chan llm.Token), nil
}

// partialThenCloseClient sends one non-final, non-error token and then
// closes the channel WITHOUT ever sending a Done or Err token — the only way
// to reach the "channel closed" (!ok) branch of the collect loops, since a
// Done/Err token always returns before the loop reads again.
type partialThenCloseClient struct{}

func (partialThenCloseClient) Name() string { return "partial-close" }

func (partialThenCloseClient) StreamChat(_ context.Context, _, _ string) (<-chan llm.Token, error) {
	ch := make(chan llm.Token, 1)
	go func() {
		defer close(ch)
		ch <- llm.Token{Text: "partial-"}
	}()
	return ch, nil
}

// TestCollectText_ContextCancelled exercises the ctx.Done() branch: the
// stream never produces a token, so the only way out is context cancellation.
func TestCollectText_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := collectText(ctx, blockingClient{}, "sys", "prompt")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty partial text, got %q", got)
	}
}

// TestCollectText_ChannelClosedWithoutDone exercises the !ok branch: the
// stream closes without ever sending Done or Err.
func TestCollectText_ChannelClosedWithoutDone(t *testing.T) {
	got, err := collectText(context.Background(), partialThenCloseClient{}, "sys", "prompt")
	if err != nil {
		t.Fatalf("expected nil error on a clean channel close, got %v", err)
	}
	if got != "partial-" {
		t.Fatalf("expected accumulated partial text, got %q", got)
	}
}

// TestCollectTextReasoning_ContextCancelled is the reasoning-aware sibling of
// TestCollectText_ContextCancelled.
func TestCollectTextReasoning_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	text, reasoning, err := collectTextReasoning(ctx, blockingClient{}, "sys", "prompt")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if text != "" {
		t.Fatalf("expected empty partial text, got %q", text)
	}
	if reasoning.ReasoningText() != "" {
		t.Fatalf("expected empty reasoning, got %q", reasoning.ReasoningText())
	}
}

// TestCollectTextReasoning_ChannelClosedWithoutDone is the reasoning-aware
// sibling of TestCollectText_ChannelClosedWithoutDone.
func TestCollectTextReasoning_ChannelClosedWithoutDone(t *testing.T) {
	text, _, err := collectTextReasoning(context.Background(), partialThenCloseClient{}, "sys", "prompt")
	if err != nil {
		t.Fatalf("expected nil error on a clean channel close, got %v", err)
	}
	if text != "partial-" {
		t.Fatalf("expected accumulated partial text, got %q", text)
	}
}

// TestRun_PropagatesOpenStreamError exercises Run's own error-propagation
// branch (line ~145) together with selectToolCapturingReasoning's open-error
// branch: a failure to even open the stream must stop Run immediately.
func TestRun_PropagatesOpenStreamError(t *testing.T) {
	client := &fakeClient{openErr: errors.New("provider unavailable")}
	loop := &Loop{Client: client, Registry: testRegistry(), Executor: &fakeExecutor{}, MaxSteps: 3}

	steps, err := loop.Run(context.Background(), []string{"search:read"}, "hi")
	if err == nil {
		t.Fatal("expected Run to propagate the stream-open error")
	}
	if len(steps) != 0 {
		t.Fatalf("expected no steps recorded before the failure, got %+v", steps)
	}
}

// TestRun_PropagatesMidStreamError exercises the tok.Err branch inside
// collectTextReasoning (distinct code path from collectText's, used only via
// Run/selectToolCapturingReasoning), plus Run's error-propagation branch.
func TestRun_PropagatesMidStreamError(t *testing.T) {
	client := &fakeClient{streamErr: errors.New("connection reset")}
	loop := &Loop{Client: client, Registry: testRegistry(), Executor: &fakeExecutor{}, MaxSteps: 3}

	steps, err := loop.Run(context.Background(), []string{"search:read"}, "hi")
	if err == nil {
		t.Fatal("expected Run to propagate the mid-stream error")
	}
	if len(steps) != 0 {
		t.Fatalf("expected no steps recorded before the failure, got %+v", steps)
	}
}

// TestRun_PropagatesMalformedDecision exercises the parseDecision-error
// branch inside selectToolCapturingReasoning (distinct from SelectTool's own
// copy, which TestSelectTool_MalformedJSONIsHandledGracefully already
// covers), reached only through Run.
func TestRun_PropagatesMalformedDecision(t *testing.T) {
	client := &fakeClient{response: "not json at all, no braces"}
	loop := &Loop{Client: client, Registry: testRegistry(), Executor: &fakeExecutor{}, MaxSteps: 3}

	steps, err := loop.Run(context.Background(), []string{"search:read"}, "hi")
	if err == nil {
		t.Fatal("expected Run to surface the malformed-decision error")
	}
	if !errors.Is(err, ErrMalformedDecision) {
		t.Fatalf("expected error to wrap ErrMalformedDecision, got: %v", err)
	}
	if len(steps) != 0 {
		t.Fatalf("expected no steps recorded before the failure, got %+v", steps)
	}
}

// TestBuildSystemPrompt_NoToolsAvailable exercises the fragment=="" branch:
// a registry with zero tools always renders the "no tools available" prompt.
func TestBuildSystemPrompt_NoToolsAvailable(t *testing.T) {
	empty := tools.NewRegistry()
	got := buildSystemPrompt(empty, []string{"search:read"})
	if !contains(got, "You have no tools available for this request.") {
		t.Fatalf("expected the no-tools sentence, got: %s", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// TestBalancedJSONObjects_EscapedQuoteInString exercises the in-string
// escape-handling branches (backslash-escape start, and the char immediately
// following an escape): without them, an escaped quote inside a tool
// argument would be mistaken for the string terminator and corrupt the
// brace-balance scan.
func TestBalancedJSONObjects_EscapedQuoteInString(t *testing.T) {
	raw := `{"tool":"search","args":{"query":"say \"hi\" please"}}`
	got := balancedJSONObjects(raw)
	if len(got) != 1 {
		t.Fatalf("got %d objects, want 1: %v", len(got), got)
	}
	if got[0] != raw {
		t.Fatalf("captured %q, want the full object %q", got[0], raw)
	}

	dec, err := parseDecision(raw)
	if err != nil {
		t.Fatalf("parseDecision: %v", err)
	}
	if dec.Args["query"] != `say "hi" please` {
		t.Fatalf("args[query] = %q, want unescaped quotes preserved by json.Unmarshal", dec.Args["query"])
	}
}

// TestTruncate covers both branches directly: the no-op (already short
// enough) path and the actual shortening path.
func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Fatalf("truncate should be a no-op under the limit, got %q", got)
	}
	if got := truncate("this is a long string", 4); got != "this..." {
		t.Fatalf("truncate(long, 4) = %q, want %q", got, "this...")
	}
}
