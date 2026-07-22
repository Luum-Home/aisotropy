package llm

import (
	"context"
	"errors"
	"testing"
	"time"
)

// scriptedUsageClient is a fake ReasoningToolCaller with fully scripted usage:
// every StreamChat reports streamUsage on its terminal token, every CallTools /
// CallToolsReasoning reports toolUsage on its result. No network, no
// randomness.
type scriptedUsageClient struct {
	streamUsage Usage
	toolUsage   Usage
}

func (s *scriptedUsageClient) Name() string { return "scripted:usage" }

func (s *scriptedUsageClient) StreamChat(ctx context.Context, system, prompt string) (<-chan Token, error) {
	out := make(chan Token, 3)
	u := s.streamUsage
	out <- Token{Text: "hello "}
	out <- Token{Text: "world"}
	out <- Token{Done: true, Usage: &u}
	close(out)
	return out, nil
}

func (s *scriptedUsageClient) CallTools(ctx context.Context, system, prompt string, tools []ToolSpec) (ToolCallResult, error) {
	u := s.toolUsage
	return ToolCallResult{Message: "no tool needed", Usage: &u}, nil
}

func (s *scriptedUsageClient) CallToolsReasoning(ctx context.Context, system, prompt string, tools []ToolSpec, prior ReasoningState) (ToolCallResult, ReasoningState, error) {
	u := s.toolUsage
	return ToolCallResult{Message: "reasoned", Usage: &u}, NewTextReasoningState("because"), nil
}

var _ ReasoningToolCaller = (*scriptedUsageClient)(nil)

// drainStream consumes a token channel to completion and returns the terminal
// token's usage (nil if none).
func drainStream(t *testing.T, ch <-chan Token) *Usage {
	t.Helper()
	var got *Usage
	for tok := range ch {
		if tok.Usage != nil {
			got = tok.Usage
		}
	}
	return got
}

// TestUsageMeterHonestTotalThroughComposedChain is the all-calls honest-total
// proof: the meter wraps the BASE client, the runtime chain (middleware ->
// breaker -> OTel, the exact startRuntime order) composes ON TOP of it, and the
// meter captures (1) a StreamChat through the full chain, (2) a CallTools
// through the full chain, and (3) a second StreamChat DIRECTLY on the metered
// base — the summarizer's path shape, which bypasses the middleware chain
// entirely (aisotropy/app.go hands newWorkingMemory the base client before
// WithModelMiddleware runs). The total must be the sum of all three.
func TestUsageMeterHonestTotalThroughComposedChain(t *testing.T) {
	base := &scriptedUsageClient{
		streamUsage: Usage{InputTokens: 11, OutputTokens: 7, ReasoningTokens: 2},
		toolUsage:   Usage{InputTokens: 5, OutputTokens: 3},
	}
	meter := NewUsageMeter()
	metered := WithUsageMeter(base, meter)

	// The startRuntime composition, verbatim order: middleware -> breaker -> OTel.
	// passthroughMiddleware (middleware_test.go) makes the middleware hop REAL —
	// not the len(mws)==0 shortcut that returns the client unchanged.
	composed := WithOTel(WithCircuitBreaker(WithModelMiddleware(metered, &passthroughMiddleware{tag: "usage"}), 5, time.Second), nil)

	ctx := context.Background()

	// (1) Main-turn StreamChat through the full composed chain.
	ch, err := composed.StreamChat(ctx, "sys", "prompt")
	if err != nil {
		t.Fatalf("composed StreamChat: %v", err)
	}
	drainStream(t, ch)

	// (2) Native tool-call through the full composed chain — the capability must
	// have survived every decorator for this to even compile-and-run.
	tc, ok := composed.(ToolCaller)
	if !ok {
		t.Fatalf("composed chain lost ToolCaller — a decorator dropped the capability")
	}
	if _, err := tc.CallTools(ctx, "sys", "prompt", nil); err != nil {
		t.Fatalf("composed CallTools: %v", err)
	}

	// (3) Summarizer side-call: StreamChat directly on the metered base,
	// bypassing middleware/breaker/OTel — exactly what LLMSummarizer.Condense
	// does with the client newWorkingMemory received.
	ch2, err := metered.StreamChat(ctx, "condense", "transcript")
	if err != nil {
		t.Fatalf("metered (summarizer-path) StreamChat: %v", err)
	}
	drainStream(t, ch2)

	want := Usage{
		InputTokens:     11 + 5 + 11,
		OutputTokens:    7 + 3 + 7,
		ReasoningTokens: 2 + 0 + 2,
	}
	if got := meter.Total(); got != want {
		t.Errorf("meter.Total() = %+v, want %+v", got, want)
	}
	if got := meter.Calls(); got != 3 {
		t.Errorf("meter.Calls() = %d, want 3", got)
	}
}

// TestUsageMeterCountsCallToolsReasoning proves the reasoning tool path is
// metered too, and that its result/state pass through unchanged.
func TestUsageMeterCountsCallToolsReasoning(t *testing.T) {
	base := &scriptedUsageClient{toolUsage: Usage{InputTokens: 9, OutputTokens: 4, ReasoningTokens: 6}}
	meter := NewUsageMeter()
	metered := WithUsageMeter(base, meter)

	rtc, ok := metered.(ReasoningToolCaller)
	if !ok {
		t.Fatalf("WithUsageMeter dropped ReasoningToolCaller from a ReasoningToolCaller base")
	}
	res, state, err := rtc.CallToolsReasoning(context.Background(), "sys", "p", nil, ReasoningState{})
	if err != nil {
		t.Fatalf("CallToolsReasoning: %v", err)
	}
	if res.Message != "reasoned" {
		t.Errorf("result passed through altered: %+v", res)
	}
	if state.IsEmpty() {
		t.Errorf("reasoning state was dropped by the meter")
	}
	if got, want := meter.Total(), (Usage{InputTokens: 9, OutputTokens: 4, ReasoningTokens: 6}); got != want {
		t.Errorf("meter.Total() = %+v, want %+v", got, want)
	}
}

// streamOnlyClient is a Client with no tool-calling capability.
type streamOnlyClient struct{}

func (streamOnlyClient) Name() string { return "stream-only" }
func (streamOnlyClient) StreamChat(ctx context.Context, system, prompt string) (<-chan Token, error) {
	out := make(chan Token, 1)
	out <- Token{Done: true, Usage: &Usage{InputTokens: 1, OutputTokens: 1}}
	close(out)
	return out, nil
}

// TestUsageMeterCapabilityFidelity: the meter must neither invent nor drop
// capabilities — a stream-only base stays stream-only, a ReasoningToolCaller
// base stays a ReasoningToolCaller, and a nil meter returns the client
// UNCHANGED (identity, not a wrapper).
func TestUsageMeterCapabilityFidelity(t *testing.T) {
	plain := WithUsageMeter(streamOnlyClient{}, NewUsageMeter())
	if _, ok := plain.(ToolCaller); ok {
		t.Errorf("meter invented ToolCaller on a stream-only base")
	}

	full := WithUsageMeter(&scriptedUsageClient{}, NewUsageMeter())
	if _, ok := full.(ReasoningToolCaller); !ok {
		t.Errorf("meter dropped ReasoningToolCaller")
	}

	base := streamOnlyClient{}
	if got := WithUsageMeter(base, nil); got != Client(base) {
		t.Errorf("nil meter must return the client unchanged, got a wrapper: %T", got)
	}
}

// erroringStreamClient emits a mid-stream error whose terminal token carries a
// usage block (the "provider supplies usage before an error" case documented
// on Token.Usage).
type erroringStreamClient struct{}

func (erroringStreamClient) Name() string { return "erroring" }
func (erroringStreamClient) StreamChat(ctx context.Context, system, prompt string) (<-chan Token, error) {
	out := make(chan Token, 2)
	out <- Token{Text: "partial"}
	out <- Token{Err: errors.New("boom"), Usage: &Usage{InputTokens: 3, OutputTokens: 1}}
	close(out)
	return out, nil
}

// TestUsageMeterCountsUsageOnErrTerminal: spend that the provider reported
// before failing is still real spend — the meter must count it, and the Err
// token must still reach the consumer unchanged.
func TestUsageMeterCountsUsageOnErrTerminal(t *testing.T) {
	meter := NewUsageMeter()
	metered := WithUsageMeter(erroringStreamClient{}, meter)

	ch, err := metered.StreamChat(context.Background(), "s", "p")
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var sawErr bool
	for tok := range ch {
		if tok.Err != nil {
			sawErr = true
		}
	}
	if !sawErr {
		t.Errorf("terminal Err token was swallowed by the meter")
	}
	if got, want := meter.Total(), (Usage{InputTokens: 3, OutputTokens: 1}); got != want {
		t.Errorf("meter.Total() = %+v, want %+v", got, want)
	}
}
