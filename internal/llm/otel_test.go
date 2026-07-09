package llm

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// drainTokens consumes every Token from ch until it closes, returning them in
// order — mirrors how internal/api's SSE handler drains StreamChat.
func drainTokens(ch <-chan Token) []Token {
	var got []Token
	for tok := range ch {
		got = append(got, tok)
	}
	return got
}

func attrValue(attrs []attribute.KeyValue, key attribute.Key) (attribute.Value, bool) {
	for _, a := range attrs {
		if a.Key == key {
			return a.Value, true
		}
	}
	return attribute.Value{}, false
}

func TestWithOTel_RecordsOneSpanWithGenAIAttributes(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tracer := tp.Tracer("test")

	stub := &StubClient{Delay: time.Millisecond}
	wrapped := WithOTel(stub, tracer)

	if got := wrapped.Name(); got != stub.Name() {
		t.Fatalf("Name() = %q, want %q (decorator must be transparent)", got, stub.Name())
	}

	tokens, err := wrapped.StreamChat(context.Background(), "sys", "hello")
	if err != nil {
		t.Fatalf("StreamChat returned error: %v", err)
	}

	got := drainTokens(tokens)
	if len(got) == 0 {
		t.Fatal("expected at least one token forwarded, got none")
	}
	last := got[len(got)-1]
	if !last.Done {
		t.Fatalf("expected the final forwarded token to have Done=true, got %+v", last)
	}

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("expected exactly 1 recorded span, got %d", len(ended))
	}
	span := ended[0]

	if span.Name() != spanNameChat {
		t.Errorf("span name = %q, want %q", span.Name(), spanNameChat)
	}

	attrs := span.Attributes()

	system, ok := attrValue(attrs, attrGenAISystem)
	if !ok || system.AsString() != "stub" {
		t.Errorf("gen_ai.system = %v (ok=%v), want %q", system, ok, "stub")
	}

	op, ok := attrValue(attrs, attrGenAIOperationName)
	if !ok || op.AsString() != "chat" {
		t.Errorf("gen_ai.operation.name = %v (ok=%v), want %q", op, ok, "chat")
	}

	outputTokens, ok := attrValue(attrs, attrGenAIOutputTokens)
	if !ok || outputTokens.AsInt64() <= 0 {
		t.Errorf("gen_ai.usage.output_tokens = %v (ok=%v), want > 0", outputTokens, ok)
	}

	if _, ok := attrValue(attrs, attrDurationMillis); !ok {
		t.Errorf("expected %s attribute to be set", attrDurationMillis)
	}
	if _, ok := attrValue(attrs, attrTTFTMillis); !ok {
		t.Errorf("expected %s attribute to be set (stub emits non-empty tokens)", attrTTFTMillis)
	}
}

// TestWithOTel_GenAIUsageIsRealNotDeltaCount proves the original defect is
// fixed: gen_ai.usage.* must carry the provider's REAL token counts (from
// Token.Usage), never the number of streamed SSE deltas. The stub deliberately
// makes output_tokens (reply character count) differ from the delta count
// (reply word count), so asserting the two attributes differ — and that both
// usage attrs equal the stub's deterministic Usage — proves they are no longer
// conflated. Expected values come from the same helpers the stub uses, so the
// test tracks stub behavior instead of hardcoding brittle numbers.
func TestWithOTel_GenAIUsageIsRealNotDeltaCount(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tracer := tp.Tracer("test")

	stub := &StubClient{Delay: time.Millisecond}
	wrapped := WithOTel(stub, tracer)

	const system, prompt = "sys alpha beta", "hello there general"
	tokens, err := wrapped.StreamChat(context.Background(), system, prompt)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	drainTokens(tokens)

	words := stubWordsFor(system, prompt)
	want := stubUsageFor(system, prompt, words)
	wantDeltas := len(words)

	span := recorder.Ended()[0]
	attrs := span.Attributes()

	inTok, ok := attrValue(attrs, attrGenAIInputTokens)
	if !ok || inTok.AsInt64() != int64(want.InputTokens) {
		t.Errorf("gen_ai.usage.input_tokens = %v (ok=%v), want %d", inTok, ok, want.InputTokens)
	}
	outTok, ok := attrValue(attrs, attrGenAIOutputTokens)
	if !ok || outTok.AsInt64() != int64(want.OutputTokens) {
		t.Errorf("gen_ai.usage.output_tokens = %v (ok=%v), want %d", outTok, ok, want.OutputTokens)
	}
	delta, ok := attrValue(attrs, attrStreamDeltaCount)
	if !ok || delta.AsInt64() != int64(wantDeltas) {
		t.Errorf("%s = %v (ok=%v), want %d", attrStreamDeltaCount, delta, ok, wantDeltas)
	}

	// The crux of the fix: real output tokens (reply characters) must NOT equal
	// the delta count (reply words). If they matched, a real count would be
	// indistinguishable from the delta proxy the fix removed.
	if outTok.AsInt64() == delta.AsInt64() {
		t.Fatalf("output_tokens (%d) must differ from delta_count (%d): distinguishing them is the whole point",
			outTok.AsInt64(), delta.AsInt64())
	}
}

func TestWithOTel_ForwardsErrTokenAndMarksSpanError(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tracer := tp.Tracer("test")

	errClient := &erroringClient{}
	wrapped := WithOTel(errClient, tracer)

	tokens, err := wrapped.StreamChat(context.Background(), "sys", "prompt")
	if err != nil {
		t.Fatalf("StreamChat returned error: %v", err)
	}

	got := drainTokens(tokens)
	if len(got) != 2 {
		t.Fatalf("expected 2 forwarded tokens (text + err), got %d: %+v", len(got), got)
	}
	if got[1].Err == nil {
		t.Fatalf("expected the last forwarded token to carry the Err unchanged, got %+v", got[1])
	}

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("expected exactly 1 recorded span, got %d", len(ended))
	}
	if ended[0].Status().Code.String() != "Error" {
		t.Errorf("span status = %v, want Error", ended[0].Status().Code)
	}
}

// erroringClient is a minimal Client whose stream ends with a mid-stream
// Token.Err, to verify WithOTel forwards it unchanged and marks the span as
// failed (see the recent StreamChat Err-forwarding bugfix this decorator
// must not regress).
type erroringClient struct{}

func (e *erroringClient) Name() string { return "erroring" }

func (e *erroringClient) StreamChat(ctx context.Context, _, _ string) (<-chan Token, error) {
	out := make(chan Token, 2)
	out <- Token{Text: "partial"}
	out <- Token{Err: errStreamFailed}
	close(out)
	return out, nil
}

var errStreamFailed = &streamError{"simulated mid-stream failure"}

type streamError struct{ msg string }

func (e *streamError) Error() string { return e.msg }
