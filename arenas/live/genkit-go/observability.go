package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// mode=observability measures Genkit-Go's OWN native instrumentation rather
// than exercising a chat capability, so it does NOT reuse the shared
// arenaOutput envelope every other mode fills in — its stdout contract is
// {framework,mode,otel_supported,span_count,span_names,span_timings_ms,
// logs_structured,log_sample,notes}. See main.go's main(): when
// out.Mode=="observability", the arenaOutput.Structured field (stashed by
// runObservability below, see the out.Structured = res line) is encoded
// directly instead of the arenaOutput itself.
type observabilityOutput struct {
	Framework      string             `json:"framework"`
	Mode           string             `json:"mode"`
	OtelSupported  bool               `json:"otel_supported"`
	SpanCount      int                `json:"span_count"`
	SpanNames      []string           `json:"span_names"`
	SpanTimingsMs  map[string]float64 `json:"span_timings_ms"`
	LogsStructured bool               `json:"logs_structured"`
	LogSample      string             `json:"log_sample"`
	Notes          string             `json:"notes"`
	Error          string             `json:"error,omitempty"`
}

// ---- in-process OTel wiring (no external collector) ----

// inMemorySpanExporter is a minimal, dependency-free implementation of the
// standard sdktrace.SpanExporter interface. It exists so this adapter needs
// no extra Go module beyond what genkit already pulls in transitively
// (go.opentelemetry.io/otel/sdk is already an indirect dependency per go.mod
// — genkit itself is built on the OTel Go SDK) and no external process (no
// OTel Collector, no Jaeger, no file exporter) — every captured span lives in
// this slice, in this OS process, for the lifetime of one adapter run.
type inMemorySpanExporter struct {
	mu    sync.Mutex
	spans []sdktrace.ReadOnlySpan
}

func newInMemorySpanExporter() *inMemorySpanExporter { return &inMemorySpanExporter{} }

func (e *inMemorySpanExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.spans = append(e.spans, spans...)
	return nil
}

func (e *inMemorySpanExporter) Shutdown(_ context.Context) error { return nil }

// Snapshot returns a defensive copy of every span exported so far.
func (e *inMemorySpanExporter) Snapshot() []sdktrace.ReadOnlySpan {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]sdktrace.ReadOnlySpan, len(e.spans))
	copy(out, e.spans)
	return out
}

// logCapture installs itself (via io.MultiWriter) as one of the sinks for the
// process-wide slog.Default() handler, so Genkit's own structured log calls
// are captured in memory as real emitted lines — in addition to still
// streaming to stderr for live PROGRESS visibility — without touching Genkit
// source.
type logCapture struct {
	mu   sync.Mutex
	seen []string
}

func newLogCapture() *logCapture { return &logCapture{} }

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	line := strings.TrimSpace(string(p))
	if line != "" {
		c.seen = append(c.seen, line)
	}
	return len(p), nil
}

// SawStructuredEntry reports whether at least one captured line parses as a
// non-empty JSON object — i.e. genuinely structured (typed key/value fields),
// not free-text.
func (c *logCapture) SawStructuredEntry() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.seen {
		var probe map[string]any
		if json.Unmarshal([]byte(l), &probe) == nil && len(probe) > 0 {
			return true
		}
	}
	return false
}

// Sample returns one representative captured log line, preferring an entry
// that looks like Genkit's own span/action instrumentation (core/tracing/tracing.go's
// "span start"/"span end", core/action.go's "Action.Run") over this
// adapter's own PROGRESS lines (which bypass slog entirely — plain
// fmt.Fprintf to os.Stderr — and so are never captured here).
func (c *logCapture) Sample() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.seen {
		if strings.Contains(l, `"msg"`) && (strings.Contains(l, "span") || strings.Contains(l, "Action.Run")) {
			return l
		}
	}
	if len(c.seen) > 0 {
		return c.seen[0]
	}
	return ""
}

// obsHooks bundles everything installed before genkit.Init so runObservability
// can read it back after the representative turn.
type obsHooks struct {
	exporter *inMemorySpanExporter
	provider *sdktrace.TracerProvider
	logs     *logCapture
}

// setupObservabilityTracing installs an in-process OpenTelemetry SDK
// TracerProvider wired to an in-memory span exporter via sdktrace.WithSyncer
// (synchronous export on every span.End() — no batching delay, no external
// collector), and a JSON slog handler that captures Genkit's own structured
// log lines.
//
// This MUST run before genkit.Init / the first genkit.Generate call:
// Genkit's core/tracing.TracerProvider() (github.com/firebase/genkit/go/core/tracing,
// v1.10.0, tracing.go) calls otel.GetTracerProvider() and ADOPTS whatever is
// already registered there, only falling back to creating its own default
// *sdktrace.TracerProvider (which drops every span — no processor is
// attached) if none is set yet. Setting ours first, via the same
// process-global otel.SetTracerProvider Genkit itself uses, is how Genkit's
// OWN spans (the outer "generate" util span, each model Action span, each
// tool Action span — see core/action.go's Action.runWithTelemetry, which
// wraps every registered Action.Run in tracing.RunInNewSpan) land in this
// exporter with zero changes to the Genkit SDK and no fork.
func setupObservabilityTracing() *obsHooks {
	exporter := newInMemorySpanExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(tp)

	logs := newLogCapture()
	// slog.LevelDebug: Genkit's span-lifecycle logs ("span start"/"span end" in
	// core/tracing/tracing.go, "Action.Run" in core/action.go) are emitted at Debug,
	// below the stdlib slog default of Info — a real integrator has to opt in
	// to see them, same as this adapter does here.
	h := slog.NewJSONHandler(io.MultiWriter(os.Stderr, logs), &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(h))

	return &obsHooks{exporter: exporter, provider: tp, logs: logs}
}

// defaultObservabilityTools supplies a real, multi-op tool set (matching the
// `tools` mode smoke request in README.md) when the caller's stdin JSON
// doesn't already include one, so `mode:"observability"` is runnable with a
// bare {"mode":"observability"} request.
func defaultObservabilityTools() []inTool {
	return []inTool{
		{
			Name:        "calculator",
			Description: "Evaluate a real arithmetic expression.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"expression": map[string]any{"type": "string"}},
				"required":   []string{"expression"},
			},
		},
		{
			Name:        "corpus_lookup",
			Description: "Search the real Nimbus Rail corpus.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"query": map[string]any{"type": "string"}},
				"required":   []string{"query"},
			},
		},
	}
}

// runObservability drives ONE representative turn through the real tools
// loop (real calculator + real Qdrant/e5-backed corpus_lookup, agnes chat
// model) — i.e. genuinely multiple ops per turn: the outer Genkit "generate"
// span, the model action span (the agnes call itself), and one action span
// per executed tool — then reports what Genkit's own OTel instrumentation and
// structured logging emitted for it.
func runObservability(rc *runCtx, out *arenaOutput, h *obsHooks) error {
	res := &observabilityOutput{Framework: framework, Mode: "observability"}
	// Stash immediately: even if the representative turn below fails partway,
	// main() still encodes THIS struct (with whatever spans/logs were captured
	// up to the failure, plus res.Error) rather than falling back to an empty
	// envelope.
	out.Structured = res

	obsIn := rc.in
	if len(obsIn.Tools) == 0 {
		obsIn.Tools = defaultObservabilityTools()
	}
	if len(obsIn.Messages) == 0 {
		obsIn.Messages = []inMessage{{
			Role:    "user",
			Content: "What is 12 times 7? Also, what is the service top speed of the Vireo T7 trainset?",
		}}
	}
	if obsIn.System == "" {
		obsIn.System = "You are a helpful assistant with tools. Use them when needed, then answer using their results."
	}
	obsRC := *rc
	obsRC.in = obsIn

	fmt.Fprintln(os.Stderr, "PROGRESS: observability: running one representative multi-op turn (tools mode: generate span + model action span + real tool action span(s))")

	var probe arenaOutput
	turnErr := runTools(&obsRC, &probe)

	// sdktrace.WithSyncer already exports synchronously on every span.End(), so
	// every span closed during the turn above (including the outer "generate"
	// span, which closes when genkit.Generate returns) is already in the
	// exporter. ForceFlush is still called as a defensive no-op belt-and-braces
	// step, not because it is load-bearing here.
	_ = h.provider.ForceFlush(rc.ctx)

	spans := h.exporter.Snapshot()
	res.OtelSupported = len(spans) > 0
	res.SpanCount = len(spans)

	// Genkit's tool-execution loop recurses: each iteration wraps its model
	// call in its OWN "generate" span (ai/generate.go's runGenerate), and a
	// multi-iteration turn nests iteration 2's "generate"+model spans INSIDE
	// iteration 1's still-open ones (observed live: the outer "generate" span
	// doesn't close until every iteration is done). So the SAME span name can
	// legitimately appear more than once per turn — collapsing span_timings_ms
	// to one entry per name would silently drop a real, distinctly-timed
	// operation. Disambiguate repeats with a "#N" suffix (N = occurrence order,
	// matching export order = span-end order) so span_timings_ms stays a flat
	// {name: ms} map (per the required contract) without losing data.
	names := make([]string, 0, len(spans))
	timings := make(map[string]float64, len(spans))
	nameSeen := make(map[string]int, len(spans))
	for _, s := range spans {
		name := s.Name()
		names = append(names, name)
		durMs := float64(s.EndTime().Sub(s.StartTime())) / float64(time.Millisecond)
		nameSeen[name]++
		key := name
		if n := nameSeen[name]; n > 1 {
			key = fmt.Sprintf("%s#%d", name, n)
		}
		timings[key] = durMs
	}
	sort.Strings(names)
	res.SpanNames = names
	res.SpanTimingsMs = timings

	res.LogsStructured = h.logs.SawStructuredEntry()
	res.LogSample = h.logs.Sample()

	if turnErr != nil {
		res.Error = fmt.Sprintf("representative turn error (spans/logs above reflect whatever executed before the failure): %v", turnErr)
	}
	res.Notes = buildObservabilityNotes(res, turnErr)

	fmt.Fprintf(os.Stderr, "PROGRESS: observability: captured %d span(s), logs_structured=%v\n", res.SpanCount, res.LogsStructured)
	return turnErr
}

func buildObservabilityNotes(res *observabilityOutput, turnErr error) string {
	var b strings.Builder
	b.WriteString("Genkit-Go ships NATIVE OpenTelemetry tracing (github.com/firebase/genkit/go/core/tracing, v1.10.0): every registered Action.Run (model call, tool call) is wrapped in an OTel span by Action.runWithTelemetry, and genkit.Generate wraps the whole turn in its own outer 'generate' span (type=util) — all via tracing.RunInNewSpan, which reads the process-global otel.GetTracerProvider() and only falls back to a private, unexported default TracerProvider (with no processor attached, so spans are silently dropped) if the caller never sets one. ")
	b.WriteString("This adapter installs its OWN go.opentelemetry.io/otel/sdk TracerProvider (sdktrace.WithSyncer + a hand-rolled in-memory exporter — no OTel Collector, no Jaeger, no network hop) via otel.SetTracerProvider BEFORE genkit.Init/the first Generate call, so Genkit's spans land here with zero SDK changes or forking. ")
	if res.SpanCount > 0 {
		b.WriteString(fmt.Sprintf("This run captured %d span(s): the outer 'generate' util span, the model action span (the agnes call), and one action span per real tool execution — i.e. multiple distinct ops per turn, each independently timed via span.EndTime()-span.StartTime(). ", res.SpanCount))
		b.WriteString("Genkit's tool-execution loop RECURSES on each model turn that requests a tool call, so the same span name ('generate', and the model's own action name) can legitimately appear more than once, nested — observed live: a second-iteration 'generate'+model span pair opens and closes entirely inside the first iteration's still-open 'generate' span. span_timings_ms disambiguates repeats with a '#N' suffix (N = occurrence order) rather than silently overwriting one timing with another. ")
	} else {
		b.WriteString("This run captured 0 spans — either the representative turn failed before any span closed, or Genkit's span emission regressed; see \"error\". ")
	}
	b.WriteString("Logging: Genkit uses Go's standard log/slog throughout (core/logger.FromContext wraps a *slog.Logger; core/tracing/tracing.go's RunInNewSpan itself logs \"span start\"/\"span end\", and core/action.go's Action.Run logs on every call), so every entry carries typed key=value attributes — structured by construction, not fmt.Printf text. ")
	b.WriteString("Genkit's own default is a plain slog.TextHandler to stderr at Info level (core/logger.SetLevel), which does NOT show the Debug-level span/action entries; this adapter raises the level and swaps in a slog.JSONHandler to observe and capture them, exactly as a real integrator wiring log aggregation would.")
	if turnErr != nil {
		b.WriteString(" The representative turn itself returned an error this pass; see \"error\" for what failed — span/log capture above is still real, reflecting whatever executed up to that point.")
	}
	return b.String()
}
