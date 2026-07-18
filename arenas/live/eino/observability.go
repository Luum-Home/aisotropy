package main

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	traceeino "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/cloudwego/eino"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"
	otelapi "go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// This file wires Eino's NATIVE callbacks.Handler mechanism
// (github.com/cloudwego/eino/callbacks — the same seam every other mode's
// usageToolTracker already uses) to a PACKAGED, third-party, OSS OTel
// integration for Eino: github.com/braintrustdata/braintrust-sdk-go's
// trace/contrib/cloudwego/eino module (Apache-2.0). That module implements
// callbacks.Handler itself and turns each ChatModel/Tool/Embedding callback
// firing into a real go.opentelemetry.io/otel span — this file only supplies
// the *sdktrace.TracerProvider it writes to.
//
// WHY this package, not the hand-rolled OTel bridge this file used to
// contain: per README.md#observability-mode, three community options were
// evaluated. (a) eino-ext's own OTel integration
// (github.com/cloudwego/eino-ext/callbacks/apmplus, built on
// github.com/cloudwego/eino-ext/libs/acl/opentelemetry) only constructs an
// OTLP/gRPC exporter — it requires a live collector endpoint just to
// construct, so it cannot run offline/in-process. (c) loongsuite-go-agent
// (github.com/alibaba/loongsuite-go-agent) is compile-time/eBPF
// instrumentation driven by an `otel` build-wrapper, not an importable
// library, and its own docs describe Eino support as still rolling out
// framework-by-framework — not a drop-in Go module dependency this adapter
// can `go get`. (b) THIS package —
// github.com/braintrustdata/braintrust-sdk-go/trace/contrib/cloudwego/eino —
// is a plain importable Go module: its Handler takes an *optional*
// trace.TracerProvider (HandlerOptions.TracerProvider) and, verified by
// reading traceeino.go directly (not just its docs), makes ZERO network
// calls and requires NO Braintrust API key to construct or emit spans — it
// is decoupled from Braintrust's hosted backend and only touches whatever
// TracerProvider/exporter is handed to it. That is exactly the shape this
// adapter needs: swap in tracetest.InMemoryExporter (part of the OTel Go SDK
// itself) via sdktrace.WithSyncer, and every span from a REAL Eino turn
// lands in-process, synchronously, with zero external collector.
//
// Honest scope note: this packaged handler only instruments the THREE
// component kinds it recognizes at the RunInfo level — ChatModel, Tool, and
// Embedding (see its OnStart: model.ConvCallbackInput /
// tool.ConvCallbackInput / embedding.ConvCallbackInput, each gated behind a
// type check on the callback payload). It does NOT create a span for the
// surrounding graph/composite nodes (e.g. the ReAct agent's own root node or
// the "Tools" node wrapper) the way this file's PREVIOUS hand-rolled
// implementation did (which started a span for every RunInfo unconditionally).
// So the span count/shape below is narrower — and, we judge, more honestly
// "OTel spans an Eino OSS integration actually produces" — than a bespoke
// span-per-callback implementation would be. See mode_observability.go's
// Notes field for the exact counts from a real run.

func init() {
	// Any internal OTel SDK error (e.g. a malformed attribute) surfaces on
	// stderr instead of being silently dropped.
	otelapi.SetErrorHandler(otelapi.ErrorHandlerFunc(func(err error) {
		log.Printf("eino-live-arena: otel sdk error: %v", err)
	}))
}

// obsRecorder is the observability mode's callback bookkeeping. It pairs two
// independent callbacks.Handler registrations on the SAME Eino graph run
// (Eino copies the callback stream per handler — see
// github.com/cloudwego/eino/callbacks/aspect_inject.go — so registering both
// is safe and neither interferes with the other):
//
//  1. counterHandler(): a ~15-line handler owned by THIS adapter that counts
//     every raw OnStart/OnEnd/OnError(/stream variant) firing — Eino's
//     baseline native signal, independent of whether anything downstream
//     turns it into a span.
//  2. span: the packaged traceeino.Handler (github.com/braintrustdata/
//     braintrust-sdk-go), which does the actual OTel span production.
type obsRecorder struct {
	rawEvents int64 // atomic: total OnStart+OnEnd/OnError callback firings observed

	span *traceeino.Handler
}

// newObsRecorder builds a real go.opentelemetry.io/otel/sdk/trace
// TracerProvider backed by an in-memory exporter, wired via
// sdktrace.WithSyncer so every span.End() call exports synchronously — no
// background batching goroutine, no network hop, no collector process — and
// hands that provider to the packaged traceeino.Handler. The returned
// shutdown func flushes/closes the provider; call it once the scenario's
// Generate call returns.
func newObsRecorder() (*obsRecorder, *tracetest.InMemoryExporter, func(context.Context) error) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	span := traceeino.NewHandlerWithOptions(traceeino.HandlerOptions{
		TracerProvider: provider,
	})
	return &obsRecorder{span: span}, exporter, provider.Shutdown
}

// handlers returns every callbacks.Handler this mode registers on the run —
// pass this whole slice to compose.WithCallbacks(recorder.handlers()...).
func (r *obsRecorder) handlers() []callbacks.Handler {
	return []callbacks.Handler{r.counterHandler(), r.span}
}

// wait blocks until the packaged handler's async streaming-output goroutines
// (if this scenario used the ChatModel's Stream path anywhere internally)
// have finished annotating and ending their spans, so exporter.GetSpans()
// below is guaranteed to see every finished span before the caller reads it.
// Safe to call even when no streaming happened (the underlying WaitGroup is
// then simply already at zero).
func (r *obsRecorder) wait() { r.span.Wait() }

// counterHandler builds a minimal Eino callbacks.Handler that does nothing
// but count raw callback firings — no OTel involved. It receives its OWN
// independent copy of any stream (Eino's framework-level guarantee; see the
// file-level comment above), so draining/closing it here never starves the
// packaged span handler's copy of the same stream.
func (r *obsRecorder) counterHandler() callbacks.Handler {
	inc := func() { atomic.AddInt64(&r.rawEvents, 1) }
	return callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
			inc()
			return ctx
		}).
		OnEndFn(func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackOutput) context.Context {
			inc()
			return ctx
		}).
		OnErrorFn(func(ctx context.Context, _ *callbacks.RunInfo, _ error) context.Context {
			inc()
			return ctx
		}).
		OnStartWithStreamInputFn(func(ctx context.Context, _ *callbacks.RunInfo, input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
			input.Close() // this counter's own independent stream copy — drain+close per the Handler contract
			inc()
			return ctx
		}).
		OnEndWithStreamOutputFn(func(ctx context.Context, _ *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
			defer output.Close()
			for {
				if _, err := output.Recv(); err != nil {
					break // io.EOF (normal end) or a stream error — either way, done
				}
			}
			inc()
			return ctx
		}).
		Build()
}

// rawEventCount returns the total number of raw OnStart+OnEnd/OnError
// callback firings observed — Eino's native signal, independent of whether
// anything downstream (like the packaged OTel handler) turns them into
// spans.
func (r *obsRecorder) rawEventCount() int64 { return atomic.LoadInt64(&r.rawEvents) }

// spanTimingsMS converts the in-memory exporter's finished spans into the
// observability-mode wire contract: an ordered name list (duplicates kept,
// exactly as they fired — e.g. a chat-model span name appears twice for the
// ReAct agent's tool-call turn and its final-answer turn) plus a
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
