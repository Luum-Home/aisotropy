// Package llm — OpenTelemetry instrumentation for the streaming chat path.
//
// See docs/decisions/0010-gollem-adoption-scope-and-observability.md
// ("Observability decision: two LLM call paths, two instrumentation
// strategies"). Aisotropy has two distinct LLM call paths:
//
//  1. The streaming chat path (internal/api/chat_stream.go) calls
//     llm.Client.StreamChat directly — it never builds a Gollem
//     core.Agent[T], so Gollem's ext/otel (which only instruments
//     Agent[T].Run) has no visibility into it.
//  2. The structured-output/reflection path (GollemReflect, gollem.go) does
//     build a core.Agent[T] and is expected to use Gollem's own ext/otel
//     when that path is exercised in production.
//
// WithOTel below is path (1)'s own provider-agnostic instrumentation: a
// decorator over the Client interface (consistent with ADR-0009's seam — it
// decorates Client, it does not bypass it), emitting one span per
// StreamChat call using the OTel GenAI semantic-convention attribute
// vocabulary docs/architecture/observability.md already specifies by hand.
package llm

import (
	"context"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// gen_ai.* / aisotropy.* attribute and span names.
//
// These are plain string consts rather than an import of
// go.opentelemetry.io/otel/semconv: the GenAI section of that package is
// still marked experimental/unstable upstream and its attribute names have
// moved across semconv versions (e.g. the "system" identifying attribute has
// been renamed in some draft revisions). A small, stable, locally-owned
// vocabulary avoids chasing that churn while staying name-compatible with
// Gollem's own ext/otel constants (AttrModelSystem, AttrModelName,
// AttrOutputTokens in github.com/fugue-labs/gollem/ext/otel) and with the
// schema docs/architecture/observability.md already documents by hand — so a
// dashboard built against gen_ai.* attributes does not need to special-case
// which of aisotropy's two LLM call paths produced a given span.
const (
	attrGenAISystem        = "gen_ai.system"
	attrGenAIRequestModel  = "gen_ai.request.model"
	attrGenAIOperationName = "gen_ai.operation.name"
	// attrGenAIInputTokens / attrGenAIOutputTokens carry REAL provider-reported
	// token counts (Token.Usage, see llm.go) — never a proxy metric. They are
	// only set on a span when the underlying Client actually supplied Usage on
	// its terminal Token; a provider that reports no usage produces a span
	// with neither attribute, rather than a fabricated number.
	attrGenAIInputTokens  = "gen_ai.usage.input_tokens"
	attrGenAIOutputTokens = "gen_ai.usage.output_tokens"

	// attrStreamDeltaCount is the number of non-empty Token deltas forwarded
	// during the stream — a stream-shape metric, deliberately NOT named
	// gen_ai.usage.* so it can never be mistaken for real token accounting
	// (the exact defect this attribute's renaming fixes: the delta count is
	// not the token count, and providers often tokenize differently than the
	// stream chunks it).
	attrStreamDeltaCount = "aisotropy.stream.delta_count"

	attrTTFTMillis     = "aisotropy.ttft_ms"
	attrDurationMillis = "aisotropy.duration_ms"

	// attrTenantID / attrUserID are the identity-and-isolation attributes
	// docs/architecture/observability.md specifies. They are populated only
	// when the ctx passed into StreamChat carries them (see context.go,
	// TenantFromContext/UserFromContext) — set by internal/api/chat_stream.go
	// via llm.WithTenant/llm.WithUser alongside its own ctx injection.
	//
	// aisotropy.bpt (Bet 6, bits-per-token-per-tenant) is a future
	// enrichment on this same span once logprobs are available from the
	// provider response — not added here (see
	// docs/research/bet-06-bits-per-token-metric.md).
	attrTenantID = "tenant_id"
	attrUserID   = "user_id"

	genAIOperationChat = "chat"
	spanNameChat       = "chat"
	// spanNameChatTools is the span for a native tool-calling provider call
	// (ToolCaller.CallTools), distinct from the streaming "chat" span.
	spanNameChatTools = "chat.tools"

	tracerName = "github.com/Luum-Home/aisotropy/internal/llm"
)

// otelClient decorates a Client with OTel GenAI-semconv tracing. It is
// transparent: every Token (Text/Done/Err) is forwarded to the caller
// unchanged — the wrapper only observes the stream to populate span
// attributes, it never filters, buffers-and-replays, or swallows a Token,
// including the Err fix from the recent StreamChat bugfix (a mid-stream
// Token.Err must still reach the caller as the final Token).
type otelClient struct {
	next   Client
	tracer trace.Tracer
}

// WithOTel wraps c so every StreamChat call emits one gen_ai span carrying
// provider/model identity, time-to-first-token, total duration, real
// input/output token usage (when the Client supplies it — see llm.go's
// Usage) alongside a separately-named stream delta count, and — when present
// on ctx — tenant_id/user_id identity attributes (docs/architecture/
// observability.md's "Identity and isolation" section).
//
// internal/llm must not import internal/api (see docs/architecture/
// architecture layering), so tenant/user are read back out of ctx via this
// package's own context.go (TenantFromContext/UserFromContext) instead of
// internal/api's own tenantCtxKey/userCtxKey: internal/api/chat_stream.go
// sets both — its own ctx injection (withIdentity) and this package's (via
// llm.WithTenant/llm.WithUser) — on the same ctx before calling StreamChat.
// product_id is not yet threaded through this seam (no per-request ctx
// carrier exists for it today); that remains a follow-up.
func WithOTel(c Client, tracer trace.Tracer) Client {
	if tracer == nil {
		tracer = noop.NewTracerProvider().Tracer(tracerName)
	}
	o := &otelClient{next: c, tracer: tracer}
	// Preserve an inner ToolCaller's native tool-calling (ADR-0012): a decorator
	// that dropped CallTools would make client.(ToolCaller) fail downstream and
	// silently disable the native tool-calling path.
	if tc, ok := c.(ToolCaller); ok {
		return &otelToolCaller{otelClient: o, tc: tc}
	}
	return o
}

// otelToolCaller preserves an inner ToolCaller through the OTel decorator,
// emitting one gen_ai span per CallTools invocation (a native tool-call is a
// provider call too), consistent with the per-StreamChat span above.
type otelToolCaller struct {
	*otelClient
	tc ToolCaller
}

var _ ToolCaller = (*otelToolCaller)(nil)

// CallTools starts one "chat.tools" span, delegates to the inner ToolCaller, and
// records provider/model + tenant/user identity attributes (same vocabulary as
// StreamChat). The span ends when the call returns.
func (o *otelToolCaller) CallTools(ctx context.Context, system, prompt string, tools []ToolSpec) (ToolCallResult, error) {
	genAISystem, genAIModel := splitClientName(o.next.Name())

	attrs := []attribute.KeyValue{
		attribute.String(attrGenAISystem, genAISystem),
		attribute.String(attrGenAIOperationName, genAIOperationChat),
	}
	if genAIModel != "" {
		attrs = append(attrs, attribute.String(attrGenAIRequestModel, genAIModel))
	}
	if tenant, ok := TenantFromContext(ctx); ok {
		attrs = append(attrs, attribute.String(attrTenantID, tenant))
	}
	if user, ok := UserFromContext(ctx); ok {
		attrs = append(attrs, attribute.String(attrUserID, user))
	}

	spanCtx, span := o.tracer.Start(ctx, spanNameChatTools, trace.WithAttributes(attrs...))
	defer span.End()

	res, err := o.tc.CallTools(spanCtx, system, prompt, tools)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	return res, err
}

// Name identifies the underlying provider, unchanged — the decorator does
// not alter the identity the rest of the codebase (logs, GollemModel) relies
// on.
func (o *otelClient) Name() string { return o.next.Name() }

// StreamChat starts one "chat" span per call, forwards every Token from the
// underlying Client unchanged, and records gen_ai.* attributes as the stream
// progresses. The span is always ended exactly once, when the returned
// channel closes (normal completion, a Token.Err, or ctx cancellation).
func (o *otelClient) StreamChat(ctx context.Context, system, prompt string) (<-chan Token, error) {
	genAISystem, genAIModel := splitClientName(o.next.Name())

	attrs := []attribute.KeyValue{
		attribute.String(attrGenAISystem, genAISystem),
		attribute.String(attrGenAIOperationName, genAIOperationChat),
	}
	if genAIModel != "" {
		attrs = append(attrs, attribute.String(attrGenAIRequestModel, genAIModel))
	}
	if tenant, ok := TenantFromContext(ctx); ok {
		attrs = append(attrs, attribute.String(attrTenantID, tenant))
	}
	if user, ok := UserFromContext(ctx); ok {
		attrs = append(attrs, attribute.String(attrUserID, user))
	}

	spanCtx, span := o.tracer.Start(ctx, spanNameChat, trace.WithAttributes(attrs...))

	tokens, err := o.next.StreamChat(spanCtx, system, prompt)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.End()
		return nil, err
	}

	out := make(chan Token)
	go forwardAndTrace(spanCtx, span, tokens, out)
	return out, nil
}

// forwardAndTrace ranges over in, forwarding every Token to out unchanged,
// while recording TTFT/duration/delta-count attributes (and REAL token usage
// when the terminal Token carries one, see llm.go's Usage) and terminating
// the span exactly once when the loop ends (in closes, a Token.Err arrives,
// or ctx is cancelled).
func forwardAndTrace(ctx context.Context, span trace.Span, in <-chan Token, out chan<- Token) {
	start := time.Now()
	var ttft time.Duration
	ttftRecorded := false
	deltaCount := 0
	var usage *Usage
	var finishErr error

	defer func() {
		attrs := []attribute.KeyValue{
			attribute.Int(attrStreamDeltaCount, deltaCount),
			attribute.Int64(attrDurationMillis, time.Since(start).Milliseconds()),
		}
		if usage != nil {
			// Real, provider-reported counts only — never derived from
			// deltaCount above.
			attrs = append(attrs,
				attribute.Int(attrGenAIInputTokens, usage.InputTokens),
				attribute.Int(attrGenAIOutputTokens, usage.OutputTokens),
			)
		}
		if ttftRecorded {
			attrs = append(attrs, attribute.Int64(attrTTFTMillis, ttft.Milliseconds()))
		}
		span.SetAttributes(attrs...)

		if finishErr != nil {
			span.RecordError(finishErr)
			span.SetStatus(codes.Error, finishErr.Error())
		} else {
			span.SetStatus(codes.Ok, "")
		}
		span.End()
		close(out)
	}()

loop:
	for {
		select {
		case <-ctx.Done():
			finishErr = ctx.Err()
			span.AddEvent("chat.cancelled")
			break loop
		case tok, ok := <-in:
			if !ok {
				break loop
			}
			if tok.Text != "" {
				deltaCount++
				if !ttftRecorded {
					ttftRecorded = true
					ttft = time.Since(start)
				}
			}
			if tok.Usage != nil {
				usage = tok.Usage
			}
			if tok.Err != nil {
				finishErr = tok.Err
			}

			select {
			case out <- tok:
			case <-ctx.Done():
				if finishErr == nil {
					finishErr = ctx.Err()
					span.AddEvent("chat.cancelled")
				}
				break loop
			}

			if tok.Done || tok.Err != nil {
				break loop
			}
		}
	}
}

// splitClientName derives the GenAI system/model pair from a Client.Name()
// value. Every current Client implementation names itself "<system>:<model>"
// (e.g. "anthropic:claude-haiku-4-5-20251001", "openrouter:some-model") or
// just "<system>" with no model (StubClient: "stub").
func splitClientName(name string) (system, model string) {
	if idx := strings.IndexByte(name, ':'); idx >= 0 {
		return name[:idx], name[idx+1:]
	}
	return name, ""
}

// NewTracerProvider builds the TracerProvider used to instrument the
// streaming chat path via WithOTel.
//
// By default (no OTEL_EXPORTER_OTLP_ENDPOINT / OTEL_EXPORTER_OTLP_TRACES_ENDPOINT
// configured) it returns a no-op provider: the walking skeleton needs no
// collector running and pays no tracing cost. When either standard OTel env
// var is set, spans are batched and exported via OTLP/HTTP to that endpoint
// instead (otlptracehttp.New reads the same env vars itself per the OTel
// spec's environment-variable auto-configuration; passing no explicit
// endpoint option lets it do so).
//
// The returned shutdown func flushes and closes the exporter (a no-op when
// the no-op provider was selected) and must be called by the caller as part
// of its own graceful-shutdown sequence.
// The returned provider is the same one that produced tracer; callers that
// need to hand a full trace.TracerProvider to another instrumentation (e.g.
// the ext/otel agent tracing hooks in RunToolAgent) pass it directly, instead
// of re-deriving one. It is a no-op provider when tracing is not configured.
func NewTracerProvider(ctx context.Context) (shutdown func(context.Context) error, tracer trace.Tracer, provider trace.TracerProvider) {
	noopShutdown := func(context.Context) error { return nil }

	endpoint := firstNonEmpty(
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"),
		os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
	)
	if endpoint == "" {
		tp := noop.NewTracerProvider()
		return noopShutdown, tp.Tracer(tracerName), tp
	}

	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		// Fail open: tracing is observability, not a request-path dependency
		// — a misconfigured collector must never block the LLM OS from
		// serving chat traffic (docs/vision/principles.md's reliability
		// posture). Fall back to no-op instrumentation.
		tp := noop.NewTracerProvider()
		return noopShutdown, tp.Tracer(tracerName), tp
	}

	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter))
	return tp.Shutdown, tp.Tracer(tracerName), tp
}
