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
	// Bet 6 (bits-per-token-per-tenant) is deliberately NOT an attribute on
	// this serving span: no serving provider exposes token logprobs, so bpt
	// cannot be computed here. It is measured OUT OF BAND by an offline
	// reference-model scorer (internal/bitrate), invoked as a subprocess like
	// the sleep cycle, and persisted to the bpt store — surfaced via the
	// agent.compression.bpt span event on the bitrate.run span and the
	// GET /bitrate view, never as a gen_ai serving-span enrichment (see
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
	// silently disable the native tool-calling path. Same for an inner
	// ReasoningToolCaller (ADR-0061): the widest capability tier the inner
	// client supports is preserved (three-tier selection, like WithUsageMeter),
	// so instrumenting the chain never silently stops reasoning from being
	// carried across agent steps.
	if rtc, ok := c.(ReasoningToolCaller); ok {
		return &otelReasoningToolCaller{
			otelToolCaller: &otelToolCaller{otelClient: o, tc: rtc},
			rtc:            rtc,
		}
	}
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

// toolCallAttrs builds the provider/model + tenant/user identity attributes a
// tool-call span carries (same vocabulary as StreamChat's span), shared by
// CallTools and CallToolsReasoning so the two spans stay attribute-identical.
func (o *otelClient) toolCallAttrs(ctx context.Context) []attribute.KeyValue {
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
	return attrs
}

// CallTools starts one "chat.tools" span, delegates to the inner ToolCaller, and
// records provider/model + tenant/user identity attributes (same vocabulary as
// StreamChat). The span ends when the call returns.
func (o *otelToolCaller) CallTools(ctx context.Context, system, prompt string, tools []ToolSpec) (ToolCallResult, error) {
	spanCtx, span := o.tracer.Start(ctx, spanNameChatTools, trace.WithAttributes(o.toolCallAttrs(ctx)...))
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

// otelReasoningToolCaller preserves an inner ReasoningToolCaller through the
// OTel decorator: a reasoning tool-call is a provider call exactly like
// CallTools, so it gets the same one-span-per-invocation treatment. Reasoning
// CONTENT is never recorded on the span — only the same identity/status
// attributes CallTools records (chain-of-thought must not leak into traces).
type otelReasoningToolCaller struct {
	*otelToolCaller
	rtc ReasoningToolCaller
}

var _ ReasoningToolCaller = (*otelReasoningToolCaller)(nil)

// CallToolsReasoning starts one "chat.tools" span (identical vocabulary to
// CallTools), delegates to the inner ReasoningToolCaller, and records the call
// status. Result and reasoning state are returned unchanged.
func (o *otelReasoningToolCaller) CallToolsReasoning(ctx context.Context, system, prompt string, tools []ToolSpec, prior ReasoningState) (ToolCallResult, ReasoningState, error) {
	spanCtx, span := o.tracer.Start(ctx, spanNameChatTools, trace.WithAttributes(o.toolCallAttrs(ctx)...))
	defer span.End()

	res, state, err := o.rtc.CallToolsReasoning(spanCtx, system, prompt, tools, prior)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	return res, state, err
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

// streamTraceState accumulates the per-call metrics forwardAndTrace records
// on the span as tokens flow through: time-to-first-token, delta count, the
// terminal (real, provider-reported) Usage when the stream supplied one, and
// whatever error ended the loop (a Token.Err or ctx cancellation).
type streamTraceState struct {
	start        time.Time
	ttft         time.Duration
	ttftRecorded bool
	deltaCount   int
	usage        *Usage
	finishErr    error
}

// observe folds one forwarded Token's observable effects into st: the first
// non-empty text delta's latency (TTFT), the running delta count, the
// terminal Usage (when the provider supplied one), and the terminal error
// (when the Token itself carries one).
func (st *streamTraceState) observe(tok Token) {
	if tok.Text != "" {
		st.deltaCount++
		if !st.ttftRecorded {
			st.ttftRecorded = true
			st.ttft = time.Since(st.start)
		}
	}
	if tok.Usage != nil {
		st.usage = tok.Usage
	}
	if tok.Err != nil {
		st.finishErr = tok.Err
	}
}

// cancel records ctx's cancellation as the terminal error and adds the
// "chat.cancelled" span event — but only if no terminal error was already
// recorded (a Token.Err that raced with cancellation wins, matching the
// original single-select loop's priority).
func (st *streamTraceState) cancel(ctx context.Context, span trace.Span) {
	if st.finishErr != nil {
		return
	}
	st.finishErr = ctx.Err()
	span.AddEvent("chat.cancelled")
}

// finish sets every gen_ai/aisotropy span attribute st accumulated, records
// st.finishErr (or Ok when none), and ends the span. Deferred by
// forwardAndTrace so it runs exactly once regardless of which loop exit path
// was taken.
func (st *streamTraceState) finish(span trace.Span) {
	attrs := []attribute.KeyValue{
		attribute.Int(attrStreamDeltaCount, st.deltaCount),
		attribute.Int64(attrDurationMillis, time.Since(st.start).Milliseconds()),
	}
	if st.usage != nil {
		// Real, provider-reported counts only — never derived from
		// deltaCount above.
		attrs = append(attrs,
			attribute.Int(attrGenAIInputTokens, st.usage.InputTokens),
			attribute.Int(attrGenAIOutputTokens, st.usage.OutputTokens),
		)
	}
	if st.ttftRecorded {
		attrs = append(attrs, attribute.Int64(attrTTFTMillis, st.ttft.Milliseconds()))
	}
	span.SetAttributes(attrs...)

	if st.finishErr != nil {
		span.RecordError(st.finishErr)
		span.SetStatus(codes.Error, st.finishErr.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
}

// forwardAndTrace ranges over in, forwarding every Token to out unchanged,
// while recording TTFT/duration/delta-count attributes (and REAL token usage
// when the terminal Token carries one, see llm.go's Usage) and terminating
// the span exactly once when the loop ends (in closes, a Token.Err arrives,
// or ctx is cancelled).
func forwardAndTrace(ctx context.Context, span trace.Span, in <-chan Token, out chan<- Token) {
	st := &streamTraceState{start: time.Now()}
	defer close(out)
	defer st.finish(span)

	forwardTokens(ctx, span, in, out, st)
}

// forwardTokens is forwardAndTrace's core loop: it ranges over in, honoring
// ctx cancellation on both the read and the out<- send, forwards every Token
// to out unchanged, and folds each Token's observable effects into st. It
// returns when in closes, a Token signals Done/Err, or ctx is cancelled.
func forwardTokens(ctx context.Context, span trace.Span, in <-chan Token, out chan<- Token, st *streamTraceState) {
	for {
		select {
		case <-ctx.Done():
			st.cancel(ctx, span)
			return
		case tok, ok := <-in:
			if !ok {
				return
			}
			st.observe(tok)
			if !sendTracedToken(ctx, span, out, tok, st) {
				return
			}
			if tok.Done || tok.Err != nil {
				return
			}
		}
	}
}

// sendTracedToken sends tok to out, honoring ctx cancellation on the send. It
// returns false when forwardTokens' loop should stop (ctx was cancelled
// mid-send, recorded into st via cancel).
func sendTracedToken(ctx context.Context, span trace.Span, out chan<- Token, tok Token, st *streamTraceState) bool {
	select {
	case out <- tok:
		return true
	case <-ctx.Done():
		st.cancel(ctx, span)
		return false
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
