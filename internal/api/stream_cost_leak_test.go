package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/costmeter"
	"github.com/Luum-Home/aisotropy/internal/guardrails"
	"github.com/Luum-Home/aisotropy/internal/llm"
)

// These tests pin the revenue-leak fix: a streaming turn that the provider
// already billed MUST leave a record on the cost meter, whichever of the five
// terminals StreamCore takes. Before the fix only the clean `Done` terminal
// recorded anything, so a cancelled turn, a mid-stream failure and — most
// expensively — a client that disconnected AFTER the model produced the whole
// buffered reply all recorded zero while the provider charged in full.

// scriptedStreamClient replays a fixed script of llm.Token values, then (unless
// the script closed the stream) parks until ctx is cancelled. Parking is what
// makes the cancellation test deterministic: with the channel empty, StreamCore's
// select can only take the ctx.Done() branch.
type scriptedStreamClient struct {
	name   string
	script []llm.Token
	park   bool
}

func (c scriptedStreamClient) Name() string {
	if c.name == "" {
		return "scripted"
	}
	return c.name
}

func (c scriptedStreamClient) StreamChat(ctx context.Context, _, _ string) (<-chan llm.Token, error) {
	out := make(chan llm.Token, len(c.script))
	go func() {
		defer close(out)
		for _, tok := range c.script {
			select {
			case out <- tok:
			case <-ctx.Done():
				return
			}
		}
		if c.park {
			<-ctx.Done()
		}
	}()
	return out, nil
}

// meteredStreamDeps builds the minimal StreamDeps a terminal-accounting test
// needs: the scripted client, a real working memory, and a fresh meter with an
// explicit price basis so a recorded cost is unambiguously positive.
func meteredStreamDeps(t *testing.T, client llm.Client) (StreamDeps, *costmeter.Meter) {
	t.Helper()
	meter := costmeter.New()
	return StreamDeps{
		Client:        client,
		WM:            newTestWorkingMemory(t),
		Meter:         meter,
		InPricePer1M:  3.0,
		OutPricePer1M: 15.0,
	}, meter
}

// statsFor returns the meter's accumulated stats for tenant, failing when the
// tenant has no record at all — the exact "the turn was invisible" symptom.
func statsFor(t *testing.T, meter *costmeter.Meter, tenant string) costmeter.TenantStats {
	t.Helper()
	s, ok := meter.Stats(tenant)
	if !ok {
		t.Fatalf("tenant %q has NO record on the cost meter: the turn was billed by the provider and never accounted", tenant)
	}
	return s
}

// TestStreamCore_CancelledTurnLeavesARecord: the client hangs up mid-stream. The
// provider already generated (and billed) the tokens delivered so far, but the
// terminal token that carries llm.Usage never arrives, so the exact count is
// unknowable locally. The turn must still be counted, as an UNMETERED turn —
// never silently dropped, and never with an invented token count.
func TestStreamCore_CancelledTurnLeavesARecord(t *testing.T) {
	client := scriptedStreamClient{script: []llm.Token{{Text: "partial"}}, park: true}
	deps, meter := meteredStreamDeps(t, client)
	in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{Prompt: "hi", SessionID: "s1"}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := StreamSink{
		// Hang up as soon as the first delta lands: the next select sees a
		// cancelled ctx and an empty channel, so the ctx.Done() terminal is taken.
		OnToken: func(string) error { cancel(); return nil },
		OnDone:  func(StreamFinal) { t.Error("OnDone must not fire on a cancelled turn") },
	}
	if err := StreamCore(ctx, deps, in, sink); err != nil {
		t.Fatalf("a cancellation is not a pipeline failure: %v", err)
	}

	s := statsFor(t, meter, "acme")
	if s.Requests != 1 {
		t.Errorf("Requests = %d, want 1 (the turn happened and was billed)", s.Requests)
	}
	if s.UnmeteredTurns != 1 {
		t.Errorf("UnmeteredTurns = %d, want 1 (no provider usage reached us)", s.UnmeteredTurns)
	}
	if s.InputTokens != 0 || s.OutputTokens != 0 || s.CostUSD != 0 {
		t.Errorf("token/cost totals = %d/%d/%v, want all zero: an unmetered turn must never invent counts",
			s.InputTokens, s.OutputTokens, s.CostUSD)
	}
}

// TestStreamCore_MidStreamErrorLeavesARecord: the provider failed part-way. It
// generated (and billed) whatever it emitted before the failure, and no usage
// block arrived. Same posture as cancellation: counted, not costed.
func TestStreamCore_MidStreamErrorLeavesARecord(t *testing.T) {
	client := scriptedStreamClient{script: []llm.Token{
		{Text: "half an answer"},
		{Err: errors.New("upstream exploded")},
	}}
	deps, meter := meteredStreamDeps(t, client)
	in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{Prompt: "hi", SessionID: "s1"}}

	var got strings.Builder
	err := StreamCore(context.Background(), deps, in, discardSink(&got))
	if err == nil {
		t.Fatal("a mid-stream provider error must surface as an error")
	}

	s := statsFor(t, meter, "acme")
	if s.Requests != 1 || s.UnmeteredTurns != 1 {
		t.Errorf("Requests/UnmeteredTurns = %d/%d, want 1/1", s.Requests, s.UnmeteredTurns)
	}
}

// TestStreamCore_BlockingDisconnectRecordsExactCost is the most expensive leak
// and the one the fix can close with FULL fidelity. In output-blocking mode the
// model produced the ENTIRE reply (that is why there is something to flush) and
// the terminal token carrying llm.Usage has already been received — the client
// merely vanished before the single buffered write. The provider charged for
// every one of those tokens and the exact count is in hand, so this terminal
// records real cost, not an unmetered turn.
func TestStreamCore_BlockingDisconnectRecordsExactCost(t *testing.T) {
	client := scriptedStreamClient{script: []llm.Token{
		{Text: "a complete and fully generated answer"},
		{Done: true, Usage: &llm.Usage{InputTokens: 1000, OutputTokens: 2000}},
	}}
	deps, meter := meteredStreamDeps(t, client)
	deps.Guard = &Guard{G: guardrails.Default(), BlockOutput: true}
	in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{Prompt: "hi", SessionID: "s1"}}

	sink := StreamSink{
		// The buffered reply is written exactly once, and the peer is gone.
		OnToken: func(string) error { return errors.New("client disconnected") },
		OnDone:  func(StreamFinal) { t.Error("OnDone must not fire when the terminal write failed") },
	}
	if err := StreamCore(context.Background(), deps, in, sink); err != nil {
		t.Fatalf("a disconnect is not a pipeline failure: %v", err)
	}

	s := statsFor(t, meter, "acme")
	if s.InputTokens != 1000 || s.OutputTokens != 2000 {
		t.Errorf("recorded tokens = %d/%d, want 1000/2000 (the terminal token's exact usage was in hand)",
			s.InputTokens, s.OutputTokens)
	}
	if s.CostUSD <= 0 {
		t.Errorf("CostUSD = %v, want a positive estimate: the provider billed this turn in full", s.CostUSD)
	}
	if s.UnmeteredTurns != 0 {
		t.Errorf("UnmeteredTurns = %d, want 0: usage WAS available on this terminal", s.UnmeteredTurns)
	}
}

// TestStreamCore_LiveDisconnectLeavesARecord: the non-blocking (default) live
// flush fails mid-stream. Tokens were generated and billed; no usage arrived.
func TestStreamCore_LiveDisconnectLeavesARecord(t *testing.T) {
	client := scriptedStreamClient{script: []llm.Token{
		{Text: "first"},
		{Text: "second"},
		{Done: true, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 10}},
	}}
	deps, meter := meteredStreamDeps(t, client)
	in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{Prompt: "hi", SessionID: "s1"}}

	sink := StreamSink{
		OnToken: func(string) error { return errors.New("client disconnected") },
		OnDone:  func(StreamFinal) { t.Error("OnDone must not fire after a failed token write") },
	}
	if err := StreamCore(context.Background(), deps, in, sink); err != nil {
		t.Fatalf("a disconnect is not a pipeline failure: %v", err)
	}

	s := statsFor(t, meter, "acme")
	if s.Requests != 1 || s.UnmeteredTurns != 1 {
		t.Errorf("Requests/UnmeteredTurns = %d/%d, want 1/1", s.Requests, s.UnmeteredTurns)
	}
	if s.OutputTokens != 0 {
		t.Errorf("OutputTokens = %d, want 0: the terminal usage block was never consumed", s.OutputTokens)
	}
}

// TestStreamCore_CleanTurnWithoutUsageLeavesARecord: a clean end-of-stream from a
// provider that reported no usage at all. The turn completed and was billed; the
// counts are simply unknown. Before the fix this recorded literally nothing —
// indistinguishable from a turn that never happened.
func TestStreamCore_CleanTurnWithoutUsageLeavesARecord(t *testing.T) {
	client := scriptedStreamClient{script: []llm.Token{
		{Text: "an answer"},
		{Done: true}, // no Usage: the provider surfaced none
	}}
	deps, meter := meteredStreamDeps(t, client)
	in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{Prompt: "hi", SessionID: "s1"}}

	var got strings.Builder
	if err := StreamCore(context.Background(), deps, in, discardSink(&got)); err != nil {
		t.Fatalf("StreamCore: %v", err)
	}

	s := statsFor(t, meter, "acme")
	if s.Requests != 1 || s.UnmeteredTurns != 1 {
		t.Errorf("Requests/UnmeteredTurns = %d/%d, want 1/1", s.Requests, s.UnmeteredTurns)
	}
}

// TestStreamCore_NilMeterTerminalsDoNotPanic: every terminal must stay nil-safe,
// exactly as recordStreamCost already was.
func TestStreamCore_NilMeterTerminalsDoNotPanic(t *testing.T) {
	client := scriptedStreamClient{script: []llm.Token{
		{Text: "x"},
		{Err: errors.New("boom")},
	}}
	deps := StreamDeps{Client: client, WM: newTestWorkingMemory(t)} // Meter is nil
	in := &ChatInput{TenantID: "acme", UserID: "alice", Body: ChatRequest{Prompt: "hi", SessionID: "s1"}}

	var got strings.Builder
	if err := StreamCore(context.Background(), deps, in, discardSink(&got)); err == nil {
		t.Fatal("expected the mid-stream error to surface")
	}
}
