package main

import (
	"context"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

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
