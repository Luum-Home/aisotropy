package llm

import (
	"context"
	"errors"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// blockingClient's StreamChat returns a channel that never emits and never
// closes, so the otel forwarding loop blocks in its read select — the exact
// state ctx cancellation must break out of (forwardTokens' <-ctx.Done() arm
// and streamTraceState.cancel).
type blockingClient struct{ ch chan Token }

func (b *blockingClient) Name() string { return "blocking:model" }

func (b *blockingClient) StreamChat(_ context.Context, _, _ string) (<-chan Token, error) {
	return b.ch, nil
}

// TestWithOTel_CtxCancelEndsSpanWithError cancels the context while the
// underlying stream is blocked, then asserts the decorator closes the forwarded
// channel and records the cancellation as an Error span with the
// "chat.cancelled" event.
func TestWithOTel_CtxCancelEndsSpanWithError(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tracer := tp.Tracer("test")

	bc := &blockingClient{ch: make(chan Token)} // never emits, never closed
	wrapped := WithOTel(bc, tracer)

	ctx, cancel := context.WithCancel(context.Background())
	tokens, err := wrapped.StreamChat(ctx, "sys", "prompt")
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	cancel()

	// The forwarded channel must close (not hang) once ctx is cancelled.
	got := drainTokens(tokens)
	if len(got) != 0 {
		t.Errorf("expected no tokens forwarded from a cancelled blocked stream, got %+v", got)
	}

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("expected 1 recorded span, got %d", len(ended))
	}
	if ended[0].Status().Code.String() != "Error" {
		t.Errorf("span status = %v, want Error (ctx cancelled)", ended[0].Status().Code)
	}
	var sawCancelEvent bool
	for _, ev := range ended[0].Events() {
		if ev.Name == "chat.cancelled" {
			sawCancelEvent = true
		}
	}
	if !sawCancelEvent {
		t.Error("expected a chat.cancelled span event")
	}
}

// TestWithOTel_StreamChatSetupError covers the branch where the underlying
// StreamChat fails to even open the stream: the decorator must record the error
// on the span, end it, and return the error (no forwarding goroutine started).
func TestWithOTel_StreamChatSetupError(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tracer := tp.Tracer("test")

	failing := &scriptedClient{name: "failing:m", err: errors.New("dial refused")}
	wrapped := WithOTel(failing, tracer)

	_, err := wrapped.StreamChat(context.Background(), "sys", "prompt")
	if err == nil {
		t.Fatal("expected StreamChat to propagate the setup error")
	}
	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("expected 1 recorded span even on setup error, got %d", len(ended))
	}
	if ended[0].Status().Code.String() != "Error" {
		t.Errorf("span status = %v, want Error", ended[0].Status().Code)
	}
}

// TestSplitClientName covers both shapes: "<system>:<model>" and a bare
// "<system>" (StubClient).
func TestSplitClientName(t *testing.T) {
	sys, model := splitClientName("anthropic:claude-x")
	if sys != "anthropic" || model != "claude-x" {
		t.Errorf("split = %q,%q", sys, model)
	}
	sys2, model2 := splitClientName("stub")
	if sys2 != "stub" || model2 != "" {
		t.Errorf("split bare = %q,%q", sys2, model2)
	}
}
