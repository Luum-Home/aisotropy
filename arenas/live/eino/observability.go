package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// This file wires Eino's NATIVE callbacks.Handler mechanism
// (github.com/cloudwego/eino/callbacks — the same seam every other mode's
// usageToolTracker already uses) to a REAL, in-process OpenTelemetry SDK
// (go.opentelemetry.io/otel/sdk/trace), exporting to an in-memory span
// recorder — never an external collector or network exporter.
//
// This mirrors the pattern eino-ext itself ships for OTel integration
// (github.com/cloudwego/eino-ext/libs/acl/opentelemetry +
// github.com/cloudwego/eino-ext/callbacks/apmplus): both build a
// callbacks.Handler on top of a go.opentelemetry.io/otel/trace.Tracer,
// starting one real span per component OnStart and ending it on
// OnEnd/OnError. The difference is the exporter: eino-ext's own OTel
// integration only ships an OTLP/gRPC exporter, which needs a live
// collector endpoint to even construct (see README.md#observability-mode).
// This adapter swaps in tracetest.InMemoryExporter — part of the OTel Go
// SDK itself, not a third-party mock — via sdktrace.WithSyncer, so every
// span is captured synchronously, in-process, with zero network I/O.

func init() {
	// Any internal OTel SDK error (e.g. a malformed attribute) surfaces on
	// stderr instead of being silently dropped — mirrors how eino-ext's own
	// OtelProvider registers errors via otel.Handle in provider.go.
	otelapi.SetErrorHandler(otelapi.ErrorHandlerFunc(func(err error) {
		log.Printf("eino-live-arena: otel sdk error: %v", err)
	}))
}

// obsSpanKeyT is the context key the observability handler uses to pass the
// just-started span from OnStart/OnStartWithStreamInput to the matching
// OnEnd/OnError/OnEndWithStreamOutput call — the same per-handler
// ctx-chaining contract callbacks.Handler documents (see mode_tools.go's
// usageToolTracker for the established pattern in this adapter).
type obsSpanKeyT struct{}

// obsRecorder is the observability mode's callback bookkeeping: it counts
// every raw Eino callback firing (OnStart/OnEnd/OnError — the native
// signal, no OTel involved) and drives the real OTel tracer that turns each
// firing into a real recorded span.
type obsRecorder struct {
	tracer trace.Tracer

	mu    sync.Mutex
	order []string // span names, in the order their OnStart fired

	rawEvents int64 // atomic: total OnStart+OnEnd/OnError callback firings observed
}

// newObsRecorder builds a real go.opentelemetry.io/otel/sdk/trace
// TracerProvider backed by an in-memory exporter, wired via
// sdktrace.WithSyncer so every span.End() call exports synchronously — no
// background batching goroutine, no network hop, no collector process. The
// returned shutdown func flushes/closes the provider; call it once the
// scenario's Generate call returns.
func newObsRecorder(serviceName string) (*obsRecorder, *tracetest.InMemoryExporter, func(context.Context) error) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	tracer := provider.Tracer(
		"eino-live-arena/observability",
		trace.WithInstrumentationAttributes(attribute.String("service.name", serviceName)),
	)
	return &obsRecorder{tracer: tracer}, exporter, provider.Shutdown
}

// handler returns a real Eino callbacks.Handler. It starts one OTel span per
// component invocation (OnStart family) and ends it (OnEnd/OnError family) —
// the same shape eino-ext's own OTel handler (callbacks/apmplus) uses, see
// the file-level comment above.
func (r *obsRecorder) handler() callbacks.Handler {
	return callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
			return r.start(ctx, info)
		}).
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, out callbacks.CallbackOutput) context.Context {
			r.annotateChatUsage(ctx, info, out)
			r.end(ctx, nil)
			return ctx
		}).
		OnErrorFn(func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
			r.end(ctx, err)
			return ctx
		}).
		OnStartWithStreamInputFn(func(ctx context.Context, info *callbacks.RunInfo, input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
			input.Close() // this adapter's observability scenario never streams input; drain+close per the Handler contract
			return r.start(ctx, info)
		}).
		OnEndWithStreamOutputFn(func(ctx context.Context, info *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
			defer output.Close()
			for {
				if _, err := output.Recv(); err != nil {
					break // io.EOF (normal end) or a stream error — either way, done
				}
			}
			r.end(ctx, nil)
			return ctx
		}).
		Build()
}

func (r *obsRecorder) spanName(info *callbacks.RunInfo) string {
	if info == nil {
		return "unknown"
	}
	name := info.Name
	if name == "" {
		name = string(info.Component)
	}
	if info.Type != "" {
		return fmt.Sprintf("%s(%s)", name, info.Type)
	}
	return name
}

func (r *obsRecorder) start(ctx context.Context, info *callbacks.RunInfo) context.Context {
	atomic.AddInt64(&r.rawEvents, 1)
	if info == nil {
		return ctx
	}
	name := r.spanName(info)
	ctx, span := r.tracer.Start(ctx, name, trace.WithSpanKind(trace.SpanKindInternal))
	span.SetAttributes(
		attribute.String("eino.run_info.name", info.Name),
		attribute.String("eino.run_info.type", info.Type),
		attribute.String("eino.run_info.component", string(info.Component)),
	)
	r.mu.Lock()
	r.order = append(r.order, name)
	r.mu.Unlock()
	return context.WithValue(ctx, obsSpanKeyT{}, span)
}

func (r *obsRecorder) end(ctx context.Context, callErr error) {
	atomic.AddInt64(&r.rawEvents, 1)
	span, ok := ctx.Value(obsSpanKeyT{}).(trace.Span)
	if !ok || span == nil {
		return
	}
	if callErr != nil {
		span.RecordError(callErr)
		span.SetStatus(codes.Error, callErr.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
}

// annotateChatUsage adds prompt/completion token counts to the current
// ChatModel span when Eino's own callback output carries them
// (model.ConvCallbackOutput) — the one piece of framework-native structured
// data Eino emits per call, same field mode_tools.go's usageToolTracker
// already reads for the tools-mode token totals.
func (r *obsRecorder) annotateChatUsage(ctx context.Context, info *callbacks.RunInfo, out callbacks.CallbackOutput) {
	if info == nil || info.Component != components.ComponentOfChatModel {
		return
	}
	mo := model.ConvCallbackOutput(out)
	if mo == nil || mo.TokenUsage == nil {
		return
	}
	span, ok := ctx.Value(obsSpanKeyT{}).(trace.Span)
	if !ok || span == nil {
		return
	}
	span.SetAttributes(
		attribute.Int("gen_ai.usage.prompt_tokens", mo.TokenUsage.PromptTokens),
		attribute.Int("gen_ai.usage.completion_tokens", mo.TokenUsage.CompletionTokens),
	)
}

// rawEventCount returns the total number of raw OnStart+OnEnd/OnError
// callback firings observed — Eino's native signal, independent of whether
// anything downstream (like this file's OTel wiring) turns them into spans.
func (r *obsRecorder) rawEventCount() int64 { return atomic.LoadInt64(&r.rawEvents) }

// spanTimingsMS converts the in-memory exporter's finished spans into the
// observability-mode wire contract: an ordered name list (duplicates kept,
// exactly as they fired — e.g. "ChatModel(...)" appears twice for the ReAct
// agent's tool-call turn and its final-answer turn) plus a
// name-to-duration-ms map, disambiguating repeated names with a "#N" suffix
// so no timing is silently dropped by a map key collision.
func spanTimingsMS(spans tracetest.SpanStubs) (names []string, timingsMS map[string]float64) {
	timingsMS = map[string]float64{}
	seen := map[string]int{}
	for _, sp := range spans {
		names = append(names, sp.Name)
		seen[sp.Name]++
		key := sp.Name
		if seen[sp.Name] > 1 {
			key = fmt.Sprintf("%s#%d", sp.Name, seen[sp.Name])
		}
		timingsMS[key] = float64(sp.EndTime.Sub(sp.StartTime)) / float64(time.Millisecond)
	}
	return names, timingsMS
}
