package llm

import (
	"context"
	"errors"
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
	if !ok || system.AsString() != stubProviderName {
		t.Errorf("gen_ai.system = %v (ok=%v), want %q", system, ok, stubProviderName)
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

// erroringToolCaller is a minimal Client+ToolCaller whose CallTools always
// fails, so otelToolCaller.CallTools' error-recording branch (span.RecordError
// + codes.Error) can be driven without a real provider failure.
type erroringToolCaller struct{}

func (e *erroringToolCaller) Name() string { return "erroring-tools:model-x" }

func (e *erroringToolCaller) StreamChat(ctx context.Context, _, _ string) (<-chan Token, error) {
	out := make(chan Token, 1)
	out <- Token{Done: true}
	close(out)
	return out, nil
}

func (e *erroringToolCaller) CallTools(_ context.Context, _, _ string, _ []ToolSpec) (ToolCallResult, error) {
	return ToolCallResult{}, errToolCallFailed
}

var errToolCallFailed = &streamError{"simulated tool-call failure"}

var _ ToolCaller = (*erroringToolCaller)(nil)

// TestWithOTel_CallToolsRecordsErrorOnSpan proves otelToolCaller.CallTools
// records a failing inner ToolCaller's error onto the span (codes.Error +
// RecordError), mirroring StreamChat's error-marking behavior, and that the
// error is still propagated to the caller unchanged.
func TestWithOTel_CallToolsRecordsErrorOnSpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tracer := tp.Tracer("test")

	inner := &erroringToolCaller{}
	wrapped := WithOTel(inner, tracer)
	tc, ok := wrapped.(ToolCaller)
	if !ok {
		t.Fatal("WithOTel did not preserve the inner ToolCaller")
	}

	ctx := WithTenant(context.Background(), "acme")
	ctx = WithUser(ctx, "alice")
	if _, err := tc.CallTools(ctx, "sys", "prompt", nil); !errors.Is(err, errToolCallFailed) {
		t.Fatalf("CallTools error = %v, want %v", err, errToolCallFailed)
	}

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("expected exactly 1 recorded span, got %d", len(ended))
	}
	if ended[0].Status().Code.String() != "Error" {
		t.Errorf("span status = %v, want Error", ended[0].Status().Code)
	}
	if ended[0].Name() != spanNameChatTools {
		t.Errorf("span name = %q, want %q", ended[0].Name(), spanNameChatTools)
	}
	attrs := ended[0].Attributes()
	if model, ok := attrValue(attrs, attrGenAIRequestModel); !ok || model.AsString() != "model-x" {
		t.Errorf("gen_ai.request.model = %v (ok=%v), want %q", model, ok, "model-x")
	}
	if tenant, ok := attrValue(attrs, attrTenantID); !ok || tenant.AsString() != "acme" {
		t.Errorf("tenant_id = %v (ok=%v), want %q", tenant, ok, "acme")
	}
	if user, ok := attrValue(attrs, attrUserID); !ok || user.AsString() != "alice" {
		t.Errorf("user_id = %v (ok=%v), want %q", user, ok, "alice")
	}
}

// TestNewTracerProvider_DefaultIsNoOp proves that with neither
// OTEL_EXPORTER_OTLP_ENDPOINT nor OTEL_EXPORTER_OTLP_TRACES_ENDPOINT set,
// NewTracerProvider returns a no-op provider/tracer (no collector required)
// and a shutdown func that succeeds trivially.
func TestNewTracerProvider_DefaultIsNoOp(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")

	shutdown, tracer, provider := NewTracerProvider(context.Background())
	if tracer == nil || provider == nil {
		t.Fatal("expected a non-nil no-op tracer/provider")
	}
	// A no-op tracer must not panic and must not require a live collector.
	client := WithOTel(&StubClient{}, tracer)
	tokens, err := client.StreamChat(context.Background(), "sys", "prompt")
	if err != nil {
		t.Fatalf("StreamChat with no-op tracer: %v", err)
	}
	drainTokens(tokens)

	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("no-op shutdown returned error: %v", err)
	}
}
