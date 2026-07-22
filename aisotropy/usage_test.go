package aisotropy

// usage_test.go is the demonstrable artifact for the façade usage surface
// (Element E): App.Usage() returns the cumulative all-calls provider usage and
// ChatOutput.Usage carries the turn's own usage — with ZERO real model calls
// (the scripted, keyless stub client supplies deterministic usage on its
// terminal token, exercising the exact plumbing a real provider uses).

import (
	"context"
	"testing"
)

// TestAppUsageZeroBeforeAnyCall: reading App.Usage() on a fresh App neither
// boots the runtime nor fabricates numbers — it is the zero Usage.
func TestAppUsageZeroBeforeAnyCall(t *testing.T) {
	app := New(WithLLM(fastStub()))
	defer func() { _ = app.Close() }()

	if got := app.Usage(); got != (Usage{}) {
		t.Errorf("Usage() before any call = %+v, want zero", got)
	}
	if app.runtime != nil {
		t.Errorf("Usage() must not boot the runtime")
	}
}

// TestAppUsageAccumulatesAcrossTurns proves the façade accumulator: each Turn's
// per-turn usage (ChatOutput.Usage) is non-zero and the App-scoped cumulative
// total equals the sum of the observed per-turn figures when no background
// side-call ran (one scripted model call per turn here — small prompts, no
// summarizer eviction), then strictly grows turn over turn.
func TestAppUsageAccumulatesAcrossTurns(t *testing.T) {
	app, closeApp := newTurnApp(t)
	defer closeApp()

	ctx := context.Background()

	out1, err := app.Turn(ctx, ChatInput{TenantID: "tenantU", UserID: "u1", SessionID: "s1", Prompt: "hello usage"})
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if out1.Usage == (Usage{}) {
		t.Fatalf("per-turn ChatOutput.Usage is zero — the stub reports deterministic usage, so the turn's usage was lost in the pipeline")
	}

	afterFirst := app.Usage()
	if afterFirst != out1.Usage {
		t.Errorf("after one turn (one model call), App.Usage() = %+v, want the turn's own usage %+v", afterFirst, out1.Usage)
	}

	out2, err := app.Turn(ctx, ChatInput{TenantID: "tenantU", UserID: "u1", SessionID: "s1", Prompt: "second turn, longer prompt this time"})
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if out2.Usage == (Usage{}) {
		t.Fatalf("second per-turn usage is zero")
	}

	want := Usage{
		InputTokens:     out1.Usage.InputTokens + out2.Usage.InputTokens,
		OutputTokens:    out1.Usage.OutputTokens + out2.Usage.OutputTokens,
		ReasoningTokens: out1.Usage.ReasoningTokens + out2.Usage.ReasoningTokens,
	}
	if got := app.Usage(); got != want {
		t.Errorf("cumulative App.Usage() = %+v, want sum of both turns %+v", got, want)
	}
}
