package llm

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/fugue-labs/gollem/core"
	"go.opentelemetry.io/otel/trace/noop"
)

// capturingExporter is a fake sdktrace.SpanExporter that records exactly the
// []sdktrace.ReadOnlySpan slice it was handed — used to observe what
// redactingExporter forwards downstream, without needing a real OTLP
// collector or protobuf decoding.
type capturingExporter struct {
	got []sdktrace.ReadOnlySpan
}

func (c *capturingExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	c.got = append(c.got, spans...)
	return nil
}

func (c *capturingExporter) Shutdown(context.Context) error { return nil }

var _ sdktrace.SpanExporter = (*capturingExporter)(nil)

// endedSpanWithAttrs produces one real, ended sdktrace.ReadOnlySpan carrying
// exactly attrs, via an in-process TracerProvider + tracetest.SpanRecorder —
// the same technique otel_test.go/otel_otlp_test.go already use to get a real
// SDK span (rather than a hand-rolled fake) to assert against.
func endedSpanWithAttrs(t *testing.T, attrs ...attribute.KeyValue) sdktrace.ReadOnlySpan {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	_, span := tp.Tracer("test").Start(context.Background(), "test-span")
	span.SetAttributes(attrs...)
	span.End()

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("test setup: expected exactly 1 ended span, got %d", len(ended))
	}
	return ended[0]
}

// TestRedactingExporter_StripsContentAttributesByDefault is ACCEPTANCE
// CRITERION 2: a span carrying gollem.agent.prompt + gollem.tool.args (plus a
// benign, non-content attribute) comes out of redactingExporter WITHOUT the
// two content attributes, with the benign attribute intact, by default (no
// AISOTROPY_OTEL_CAPTURE_CONTENT set).
func TestRedactingExporter_StripsContentAttributesByDefault(t *testing.T) {
	span := endedSpanWithAttrs(t,
		attribute.String("gollem.agent.prompt", "the user's actual secret prompt"),
		attribute.String("gollem.tool.args", `{"ssn":"111-22-3333"}`),
		attribute.String(attrGenAISystem, "anthropic"), // benign — must survive
	)

	next := &capturingExporter{}
	exp := &redactingExporter{next: next}

	if err := exp.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{span}); err != nil {
		t.Fatalf("ExportSpans: %v", err)
	}
	if len(next.got) != 1 {
		t.Fatalf("expected 1 span forwarded downstream, got %d", len(next.got))
	}

	attrs := next.got[0].Attributes()
	if _, ok := attrValue(attrs, "gollem.agent.prompt"); ok {
		t.Error("gollem.agent.prompt leaked through the redacting exporter")
	}
	if _, ok := attrValue(attrs, "gollem.tool.args"); ok {
		t.Error("gollem.tool.args leaked through the redacting exporter")
	}
	system, ok := attrValue(attrs, attrGenAISystem)
	if !ok || system.AsString() != "anthropic" {
		t.Errorf("benign attribute gen_ai.system = %v (ok=%v), want %q — redaction must be pass-through for non-content attributes", system, ok, "anthropic")
	}
}

// TestRedactingExporter_StripsToolResultAndFutureMessagesShape covers the
// defense-in-depth entries: gollem.tool.result (exact-key denylist) and a
// gollem.*.messages-shaped key (the reserved suffix rule for a gollem version
// that starts emitting WithCaptureModelMessages-style content).
func TestRedactingExporter_StripsToolResultAndFutureMessagesShape(t *testing.T) {
	span := endedSpanWithAttrs(t,
		attribute.String("gollem.tool.result", "sensitive tool output"),
		attribute.String("gollem.model.messages", "full chat history"),
		attribute.Int("gollem.tool.duration_ms", 42), // benign — must survive
	)

	next := &capturingExporter{}
	exp := &redactingExporter{next: next}
	if err := exp.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{span}); err != nil {
		t.Fatalf("ExportSpans: %v", err)
	}

	attrs := next.got[0].Attributes()
	if _, ok := attrValue(attrs, "gollem.tool.result"); ok {
		t.Error("gollem.tool.result leaked through the redacting exporter")
	}
	if _, ok := attrValue(attrs, "gollem.model.messages"); ok {
		t.Error("gollem.model.messages (a *.messages-shaped key) leaked through the redacting exporter")
	}
	if dur, ok := attrValue(attrs, "gollem.tool.duration_ms"); !ok || dur.AsInt64() != 42 {
		t.Errorf("benign attribute gollem.tool.duration_ms = %v (ok=%v), want 42", dur, ok)
	}
}

// TestOtelCaptureContentEnabled_OptInRestoresCapture is ACCEPTANCE CRITERION
// 3: setting AISOTROPY_OTEL_CAPTURE_CONTENT flips otelCaptureContentEnabled
// to true (the switch NewTracerProvider and otelTracingHooks both read), and
// an unset/empty var keeps it false — the default, redact-everything posture.
func TestOtelCaptureContentEnabled_OptInRestoresCapture(t *testing.T) {
	if otelCaptureContentEnabled() {
		t.Fatal("expected otelCaptureContentEnabled() = false with the env var unset (default posture)")
	}

	t.Setenv(otelCaptureContentEnv, "1")
	if !otelCaptureContentEnabled() {
		t.Fatal("expected otelCaptureContentEnabled() = true once AISOTROPY_OTEL_CAPTURE_CONTENT is set")
	}

	t.Setenv(otelCaptureContentEnv, "")
	if otelCaptureContentEnabled() {
		t.Fatal("expected otelCaptureContentEnabled() = false again once the env var is cleared to empty")
	}
}

// TestRunToolAgent_ToolArgsNotCapturedByDefault is the hook-level half of
// ACCEPTANCE CRITERION 2/3: RunToolAgent's otelTracingHooks (toolagent.go)
// must itself pass WithCaptureToolArgs(false) by default, so the
// tool.execute.<name> span gollem emits never carries gollem.tool.args in the
// first place — independent of the redacting exporter above, which only
// guards the OTLP export path (NewTracerProvider), not every possible
// consumer of the raw TracerProvider handed to RunToolAgent.
func TestRunToolAgent_ToolArgsNotCapturedByDefault(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	tc := &fakeToolCaller{
		Client: NewStubClient(),
		script: []ToolCallResult{
			{Calls: []ToolCall{{Name: "greet", Args: map[string]string{"who": "world"}}}},
			{Message: "greeted the world"},
		},
	}
	exec := func(_ context.Context, _ string, _ string) (string, error) { return "hello world", nil }

	_, err := RunToolAgent(context.Background(), tc, tp,
		"You may call tools.", "say hi to the world",
		[]ToolSpec{{Name: "greet", Description: "greet someone"}}, exec, 3)
	if err != nil {
		t.Fatalf("RunToolAgent: %v", err)
	}

	toolSpan := findSpanByPrefix(t, recorder.Ended(), "tool.execute.")
	attrs := toolSpan.Attributes()
	if _, ok := attrValue(attrs, "gollem.tool.args"); ok {
		t.Error("gollem.tool.args present on the tool.execute span by default — otelTracingHooks must pass WithCaptureToolArgs(false)")
	}
	if name, ok := attrValue(attrs, "gollem.tool.name"); !ok || name.AsString() != "greet" {
		t.Errorf("benign attribute gollem.tool.name = %v (ok=%v), want %q", name, ok, "greet")
	}
}

// TestRunToolAgent_ToolArgsCapturedWhenOptedIn: with
// AISOTROPY_OTEL_CAPTURE_CONTENT set, otelTracingHooks must pass
// WithCaptureToolArgs(true), restoring gollem.tool.args on the raw span (the
// opt-in escape hatch's other half — this is what an operator gets if they
// deliberately choose to see tool arguments in a dev/debug trace backend).
func TestRunToolAgent_ToolArgsCapturedWhenOptedIn(t *testing.T) {
	t.Setenv(otelCaptureContentEnv, "1")

	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	tc := &fakeToolCaller{
		Client: NewStubClient(),
		script: []ToolCallResult{
			{Calls: []ToolCall{{Name: "greet", Args: map[string]string{"who": "world"}}}},
			{Message: "greeted the world"},
		},
	}
	exec := func(_ context.Context, _ string, _ string) (string, error) { return "hello world", nil }

	_, err := RunToolAgent(context.Background(), tc, tp,
		"You may call tools.", "say hi to the world",
		[]ToolSpec{{Name: "greet", Description: "greet someone"}}, exec, 3)
	if err != nil {
		t.Fatalf("RunToolAgent: %v", err)
	}

	toolSpan := findSpanByPrefix(t, recorder.Ended(), "tool.execute.")
	attrs := toolSpan.Attributes()
	if _, ok := attrValue(attrs, "gollem.tool.args"); !ok {
		t.Error("gollem.tool.args absent on the tool.execute span despite AISOTROPY_OTEL_CAPTURE_CONTENT being set")
	}
}

// findSpanByPrefix locates the single recorded span whose name starts with
// prefix, failing the test if none or more than one match. Used because
// gollem's ext/otel names tool spans "tool.execute.<toolName>" (tracing.go's
// spanName helper), so the exact name depends on the tool under test.
func findSpanByPrefix(t *testing.T, spans []sdktrace.ReadOnlySpan, prefix string) sdktrace.ReadOnlySpan {
	t.Helper()
	var matches []sdktrace.ReadOnlySpan
	for _, s := range spans {
		if len(s.Name()) >= len(prefix) && s.Name()[:len(prefix)] == prefix {
			matches = append(matches, s)
		}
	}
	if len(matches) != 1 {
		names := make([]string, len(spans))
		for i, s := range spans {
			names[i] = s.Name()
		}
		t.Fatalf("expected exactly 1 span with name prefix %q, got %d (all recorded span names: %v)", prefix, len(matches), names)
	}
	return matches[0]
}

// TestRunToolAgent_NoOtherGollemCaptureOptionEnabled is ACCEPTANCE CRITERION
// 4 as an executable test (not just a grep): it proves via observable
// behavior that RunToolAgent never enables gollem's captureToolResults —
// the tool.execute span must carry no gollem.tool.result attribute even
// though onToolEnd (gollem's ext/otel) always has a non-empty result
// available to it, by default AND under the content-capture opt-in (which
// only ever touches captureToolArgs in this codebase — see otelTracingHooks,
// toolagent.go).
func TestRunToolAgent_NoOtherGollemCaptureOptionEnabled(t *testing.T) {
	for _, optIn := range []bool{false, true} {
		if optIn {
			t.Setenv(otelCaptureContentEnv, "1")
		}

		recorder := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

		tc := &fakeToolCaller{
			Client: NewStubClient(),
			script: []ToolCallResult{
				{Calls: []ToolCall{{Name: "greet", Args: map[string]string{"who": "world"}}}},
				{Message: "greeted the world"},
			},
		}
		exec := func(_ context.Context, _ string, _ string) (string, error) { return "a non-empty tool result", nil }

		_, err := RunToolAgent(context.Background(), tc, tp,
			"You may call tools.", "say hi to the world",
			[]ToolSpec{{Name: "greet", Description: "greet someone"}}, exec, 3)
		if err != nil {
			t.Fatalf("RunToolAgent (optIn=%v): %v", optIn, err)
		}

		toolSpan := findSpanByPrefix(t, recorder.Ended(), "tool.execute.")
		if _, ok := attrValue(toolSpan.Attributes(), "gollem.tool.result"); ok {
			t.Errorf("optIn=%v: gollem.tool.result present — captureToolResults must never be enabled by aisotropy", optIn)
		}
	}
}

// TestOtelTracingHooksIsAGollemCoreHook is a narrow smoke test that
// otelTracingHooks still returns a usable core.Hook after this change (the
// WithCaptureToolArgs argument became dynamic instead of a literal true).
func TestOtelTracingHooksIsAGollemCoreHook(t *testing.T) {
	hook := otelTracingHooks(noop.NewTracerProvider())
	var _ core.Hook = hook
	if hook.OnToolStart == nil {
		t.Fatal("expected otelTracingHooks to wire OnToolStart (gollem's ext/otel.TracingHooks contract)")
	}
}
