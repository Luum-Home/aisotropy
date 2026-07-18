package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// erroringClient is a minimal llm.Client whose StreamChat always fails,
// forcing meterClient.StreamChat's own error-propagation branch (it must
// return the inner error unchanged, without wrapping a nil channel).
type erroringClient struct{ err error }

func (c erroringClient) Name() string { return "erroring" }

func (c erroringClient) StreamChat(context.Context, string, string) (<-chan llm.Token, error) {
	return nil, c.err
}

func TestMeterClient_StreamChatPropagatesInnerError(t *testing.T) {
	wantErr := errors.New("boom: inner client failure")
	m := newMeterClient(erroringClient{wantErr})

	ch, err := m.StreamChat(context.Background(), "sys", "prompt")
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if ch != nil {
		t.Fatalf("expected a nil channel alongside the error, got %v", ch)
	}
}

// fixedTokenClient hands back a small, pre-filled, already-closed channel of
// tokens — deterministic, no producer goroutine of its own — used to prove
// meterClient.StreamChat's re-emission goroutine respects ctx cancellation
// (its `case <-ctx.Done(): return` branch) instead of blocking forever on a
// send nobody will ever read.
type fixedTokenClient struct{ tokens []llm.Token }

func (fixedTokenClient) Name() string { return "fixed" }

func (c fixedTokenClient) StreamChat(context.Context, string, string) (<-chan llm.Token, error) {
	ch := make(chan llm.Token, len(c.tokens))
	for _, tok := range c.tokens {
		ch <- tok
	}
	close(ch)
	return ch, nil
}

func TestMeterClient_StreamChatStopsReemittingOnCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before StreamChat is even called

	m := newMeterClient(fixedTokenClient{tokens: []llm.Token{{Text: "a"}}})
	out, err := m.StreamChat(ctx, "sys", "prompt")
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	// Give the re-emission goroutine time to reach its select with NO reader
	// present, so the send-to-out case can never be ready and ctx.Done() is
	// the only selectable case — deterministic instead of racing a concurrent
	// reader against the sender.
	time.Sleep(50 * time.Millisecond)

	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("expected the channel to close without ever emitting a token, since ctx was already cancelled")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("channel did not close after ctx cancellation")
	}
}

// TestMeterClient_AccumulatesUsage drives the metering decorator over the stub
// (which attaches deterministic Usage to its terminal Token) and asserts the
// snapshot reflects a completed, usage-bearing stream — the mechanism the
// harness uses to measure token spend without touching the App's cost meter.
func TestMeterClient_AccumulatesUsage(t *testing.T) {
	m := newMeterClient(llm.NewStubClient())
	if m.Name() != "stub" {
		t.Fatalf("Name = %q; want it to pass through the inner name", m.Name())
	}

	ch, err := m.StreamChat(context.Background(), "system", "hello world this is a test prompt")
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var got string
	for tok := range ch {
		got += tok.Text
	}
	if got == "" {
		t.Fatal("stream produced no text")
	}

	inTok, outTok, streams := m.snapshot()
	if streams != 1 {
		t.Fatalf("streams = %d; want 1 usage-bearing stream", streams)
	}
	if inTok <= 0 || outTok <= 0 {
		t.Fatalf("token totals = in %d / out %d; want both > 0", inTok, outTok)
	}
}

// TestMeterClient_DeltasAcrossCalls proves per-call spend can be derived by
// diffing snapshots — the per-question attribution the pipeline relies on.
func TestMeterClient_DeltasAcrossCalls(t *testing.T) {
	m := newMeterClient(llm.NewStubClient())
	drain := func(prompt string) {
		ch, err := m.StreamChat(context.Background(), "sys", prompt)
		if err != nil {
			t.Fatalf("StreamChat: %v", err)
		}
		for range ch { //nolint:revive // draining the stream is the point
		}
	}

	drain("first prompt")
	in1, out1, _ := m.snapshot()
	drain("second prompt is a bit longer than the first")
	in2, out2, _ := m.snapshot()

	if in2 <= in1 || out2 <= out1 {
		t.Fatalf("totals did not grow across calls: (%d,%d) then (%d,%d)", in1, out1, in2, out2)
	}
}
