// mode_observability.go — the `observability` mode: measures aisotropy's
// NATIVE OpenTelemetry span emission (internal/llm/otel.go's WithOTel
// decorator around the streaming chat path, ADR-0010) for ONE representative
// App.Turn call, in-process, with zero code changes to internal/llm or
// aisotropy/app.go.
//
// Investigation (see README.md's "observability" section for the full
// writeup): aisotropy's App does NOT read the process-global
// otel.GetTracerProvider() (unlike, e.g., Genkit-Go's core/tracing/tracing.go — see
// the sibling genkit-go adapter's observability.go), and the public façade
// (aisotropy.Option, aisotropy/app.go) exposes no WithTracerProvider builder
// option either. Every App.startRuntime call builds its OWN
// *sdktrace.TracerProvider from scratch via internal/llm/otel.go's
// NewTracerProvider, choosing between a no-op provider and a REAL
// go.opentelemetry.io/otel/sdk/trace provider batched over OTLP/HTTP
// (otlptracehttp), gated purely on the standard
// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT / OTEL_EXPORTER_OTLP_ENDPOINT env vars —
// there is no sdktrace.WithSyncer / in-memory-exporter lever reachable from
// this adapter without editing internal/llm/otel.go itself, which is out of
// scope (this adapter is additive-only, per the prune-evidence-gate /
// architecture rules: touch only arenas/live/aisotropy/).
//
// So the only real, in-process (same OS process, loopback-only, NO external
// collector binary/container, no network hop leaving the host) way to observe
// the spans aisotropy actually emits is to point OTEL_EXPORTER_OTLP_ENDPOINT
// at an OTLP/HTTP receiver THIS adapter stands up on 127.0.0.1 before
// building the App — otlpReceiver below. It decodes the REAL OTLP/HTTP wire
// protocol (protobuf, optionally gzip-compressed,
// go.opentelemetry.io/proto/otlp/collector/trace/v1) exactly as
// otlptracehttp.Exporter emits it — mirrored, for wire compatibility, from
// otlptracehttp's own test-only mock collector
// (go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp@v1.44.0's
// mock_collector_test.go) — so no span is fabricated, filtered, or
// synthesized: every span reported below is exactly what NewTracerProvider's
// real batched OTLP exporter sent over the wire for this run.
package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// observabilityOutput is the observability-mode stdout contract — it does NOT
// reuse the shared `result` envelope every other mode fills in (see
// emitFinal in main.go): {framework,mode,otel_supported,span_count,
// span_names,span_timings_ms,logs_structured,log_sample,notes[,error]}.
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

// ---- in-process OTLP/HTTP trace receiver (no external collector) ----

// capturedSpan is the minimal projection of an OTLP tracepb.Span this mode
// needs: name + start/end, both REAL provider-reported nanosecond timestamps
// off the wire, never derived or estimated.
type capturedSpan struct {
	name      string
	startNano uint64
	endNano   uint64
}

// otlpReceiver is a loopback-only, in-process HTTP server (net/http/httptest,
// same OS process, 127.0.0.1) that terminates aisotropy's OWN OTLP/HTTP
// batched exporter. It is not a collector binary or container — it is ~60
// lines of stdlib net/http plus the OTel-owned generated protobuf types
// (go.opentelemetry.io/proto/otlp), decoding exactly the wire format
// otlptracehttp emits (see the file doc comment above for the mock-collector
// provenance of this wire contract).
type otlpReceiver struct {
	server *httptest.Server

	mu    sync.Mutex
	spans []capturedSpan
}

func newOTLPReceiver() *otlpReceiver {
	r := &otlpReceiver{}
	r.server = httptest.NewServer(http.HandlerFunc(r.handle))
	return r
}

// endpoint returns the bare "http://127.0.0.1:PORT" base URL — passed as
// OTEL_EXPORTER_OTLP_ENDPOINT, which otlptracehttp itself appends "/v1/traces"
// to (the generic env var's documented behavior, distinct from the
// signal-specific OTEL_EXPORTER_OTLP_TRACES_ENDPOINT, which would need the
// full path supplied).
func (r *otlpReceiver) endpoint() string { return r.server.URL }

func (r *otlpReceiver) close() { r.server.Close() }

func (r *otlpReceiver) handle(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/v1/traces" || req.Method != http.MethodPost {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	defer func() { _ = req.Body.Close() }()

	var reader io.Reader = req.Body
	if req.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(req.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer func() { _ = gz.Close() }()
		reader = gz
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	var expReq coltracepb.ExportTraceServiceRequest
	if err := proto.Unmarshal(raw, &expReq); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	r.record(&expReq)

	respBytes, err := proto.Marshal(&coltracepb.ExportTraceServiceResponse{})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respBytes)
}

func (r *otlpReceiver) record(req *coltracepb.ExportTraceServiceRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				r.spans = append(r.spans, capturedSpan{
					name:      sp.GetName(),
					startNano: sp.GetStartTimeUnixNano(),
					endNano:   sp.GetEndTimeUnixNano(),
				})
			}
		}
	}
}

// snapshot returns a defensive copy of every span exported so far.
func (r *otlpReceiver) snapshot() []capturedSpan {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]capturedSpan, len(r.spans))
	copy(out, r.spans)
	return out
}

// ---- structured-log capture ----

// logCapture installs itself (via io.MultiWriter) as one of the sinks for the
// process-wide slog.Default() handler, so aisotropy's own structured
// log/slog calls (aisotropy/app.go's slog.Info/slog.Error sites — including
// the otel-tracer-shutdown-failure log line itself) are captured in memory as
// real emitted lines, in addition to still streaming to stderr for live
// PROGRESS visibility, without touching aisotropy source.
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

// sawStructuredEntry reports whether at least one captured line parses as a
// non-empty JSON object — i.e. genuinely structured (typed key/value fields),
// not free-text.
func (c *logCapture) sawStructuredEntry() bool {
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

// sample returns one representative captured log line, preferring an entry
// that carries a "msg" field (a real slog record) over this adapter's own
// PROGRESS lines (which bypass slog entirely — plain fmt.Fprintf to
// os.Stderr — and so are never captured here).
func (c *logCapture) sample() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.seen {
		if strings.Contains(l, `"msg"`) {
			return l
		}
	}
	if len(c.seen) > 0 {
		return c.seen[0]
	}
	return ""
}

// ---- env helpers ----

// setEnv sets key to value and returns a restore func that puts back
// whatever was there before (unset if it was unset before this call).
func setEnv(key, value string) func() {
	prev, had := os.LookupEnv(key)
	_ = os.Setenv(key, value)
	return func() {
		if had {
			_ = os.Setenv(key, prev)
		} else {
			_ = os.Unsetenv(key)
		}
	}
}

// unsetEnv unsets key and returns a restore func that puts back whatever
// value was there before (a no-op if it was already unset).
func unsetEnv(key string) func() {
	prev, had := os.LookupEnv(key)
	_ = os.Unsetenv(key)
	return func() {
		if had {
			_ = os.Setenv(key, prev)
		}
	}
}

// defaultObservabilityMessages supplies a representative single-turn request
// when the caller's stdin JSON doesn't already include one, so
// `mode:"observability"` is runnable with a bare {"mode":"observability"}
// request, matching the sibling eino/genkit-go adapters' convention.
func defaultObservabilityMessages() []inMessage {
	return []inMessage{{
		Role:    "user",
		Content: "In one short paragraph, explain why railway signalling favors fail-safe defaults.",
	}}
}

// runObservability drives ONE representative turn through aisotropy's real
// App.Turn (the same in-process chat surface `qa` mode uses — full pipeline
// parity, see mode_qa.go), with the in-process OTLP receiver and structured
// log capture wired in first, then reports what aisotropy's own OTel
// instrumentation and structured logging actually emitted for it.
func runObservability(req *request, out *result) error {
	res := &observabilityOutput{Framework: frameworkName, Mode: modeObservability}
	// Stash immediately: even if the representative turn below fails partway,
	// emitFinal (main.go) still encodes THIS struct — with whatever spans/logs
	// were captured up to the failure, plus res.Error — instead of an empty
	// envelope.
	out.Structured = res

	agnes, err := newProviderClient()
	if err != nil {
		res.Error = err.Error()
		return err
	}

	receiver := newOTLPReceiver()
	defer receiver.close()

	// internal/llm/otel.go's NewTracerProvider reads these two standard OTel
	// env vars itself (OTEL_EXPORTER_OTLP_TRACES_ENDPOINT takes precedence over
	// OTEL_EXPORTER_OTLP_ENDPOINT when both are set — see its doc comment)
	// inside otlptracehttp.New(ctx). Point the generic one at our loopback
	// receiver and make sure the signal-specific one isn't already set in the
	// caller's environment, restoring both afterward.
	restoreEndpoint := setEnv("OTEL_EXPORTER_OTLP_ENDPOINT", receiver.endpoint())
	defer restoreEndpoint()
	restoreTracesEndpoint := unsetEnv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	defer restoreTracesEndpoint()

	// Install a JSON slog handler in front of the process-wide slog.Default()
	// BEFORE the App boots, so every entry aisotropy's façade logs is captured
	// verbatim, in addition to still streaming to stderr.
	prevLogger := slog.Default()
	logs := newLogCapture()
	slog.SetDefault(slog.New(slog.NewJSONHandler(io.MultiWriter(os.Stderr, logs), &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prevLogger)

	messages := req.Messages
	if len(messages) == 0 {
		messages = defaultObservabilityMessages()
	}
	turn, err := lastUserMessage(messages)
	if err != nil {
		res.Error = err.Error()
		return err
	}
	system := req.System
	if system == "" {
		system = "You are a helpful, concise assistant."
	}

	app := newApp(agnes, system)
	sess := sessionID(req)

	logf("observability: running one representative App.Turn (OTel-instrumented streaming chat path)")
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	turnOut, turnErr := app.Turn(ctx, aisotropy.ChatInput{
		TenantID:  arenaTenant,
		UserID:    arenaUser,
		Prompt:    turn,
		SessionID: sess,
		ProductID: productID,
	})
	cancel()
	out.LatencyMs = time.Since(start).Milliseconds()
	if turnErr == nil {
		out.Reply = turnOut.Reply
	}
	setUsage(out, app)

	// app.Close() flushes internal/llm's batched OTLP exporter — App.Close (via
	// aisotropy/app.go's rt.closeFn) invokes the SAME shutdownTracing func
	// startRuntime built the tracer with. This MUST happen before reading the
	// receiver's snapshot below: the batch span processor's default export
	// interval (5s) would otherwise make span capture flaky/non-deterministic.
	closeErr := app.Close()

	spans := receiver.snapshot()
	res.OtelSupported = len(spans) > 0
	res.SpanCount = len(spans)

	// Disambiguate a span name that legitimately fires more than once with a
	// "#N" suffix (matches the sibling eino/genkit-go adapters' convention) so
	// span_timings_ms never silently drops a real, distinctly-timed span to a
	// map-key collision.
	names := make([]string, 0, len(spans))
	timings := make(map[string]float64, len(spans))
	seen := make(map[string]int, len(spans))
	for _, sp := range spans {
		names = append(names, sp.name)
		seen[sp.name]++
		key := sp.name
		if n := seen[sp.name]; n > 1 {
			key = fmt.Sprintf("%s#%d", sp.name, n)
		}
		timings[key] = float64(sp.endNano-sp.startNano) / float64(time.Millisecond)
	}
	sort.Strings(names)
	res.SpanNames = names
	res.SpanTimingsMs = timings

	res.LogsStructured = logs.sawStructuredEntry()
	res.LogSample = logs.sample()

	res.Notes = buildObservabilityNotes(res, turnErr, closeErr)

	logf("observability: captured %d span(s) via the in-process OTLP receiver, logs_structured=%v", res.SpanCount, res.LogsStructured)

	if turnErr != nil {
		res.Error = fmt.Sprintf("representative turn error (spans/logs above reflect whatever executed before the failure): %v", turnErr)
		return turnErr
	}
	if closeErr != nil {
		res.Error = fmt.Sprintf("app.Close (tracer flush) error: %v", closeErr)
		return closeErr
	}
	return nil
}

func buildObservabilityNotes(res *observabilityOutput, turnErr, closeErr error) string {
	var b strings.Builder
	b.WriteString("aisotropy emits OpenTelemetry tracing NATIVELY (internal/llm/otel.go, ADR-0010) -- no bridge, no third-party instrumentation package, no auto-instrumentation agent: WithOTel is a hand-written decorator over the internal llm.Client interface that starts one real go.opentelemetry.io/otel/trace span per StreamChat/CallTools call, using a small, locally-owned gen_ai.* attribute vocabulary (gen_ai.system, gen_ai.request.model, gen_ai.operation.name, gen_ai.usage.input_tokens/output_tokens, plus aisotropy.ttft_ms/aisotropy.duration_ms) deliberately name-compatible with the OTel GenAI semantic-convention draft rather than importing the still-unstable go.opentelemetry.io/otel/semconv package. ")
	b.WriteString("Mechanism actually used to capture spans (investigated against the code, not assumed): aisotropy's App does not read the process-global otel.GetTracerProvider(), and the public facade exposes no WithTracerProvider builder option -- every App.startRuntime call builds its OWN *sdktrace.TracerProvider from scratch (internal/llm/otel.go's NewTracerProvider), choosing between a no-op provider and a real batched OTLP/HTTP exporter (otlptracehttp) purely from the standard OTEL_EXPORTER_OTLP_ENDPOINT/OTEL_EXPORTER_OTLP_TRACES_ENDPOINT env vars -- there is no sdktrace.WithSyncer/in-memory-exporter lever reachable without editing internal/llm/otel.go, out of scope for this additive-only adapter. So otel_supported here is backed by a real OTLP/HTTP receiver this adapter stands up in-process (net/http/httptest, loopback-only, no external collector binary/container) that decodes the actual OTLP wire protocol (protobuf, optional gzip) exactly as otlptracehttp emits it, with OTEL_EXPORTER_OTLP_ENDPOINT pointed at it before the App boots. ")
	if res.SpanCount > 0 {
		b.WriteString(fmt.Sprintf("This run captured %d span(s) from the ONE representative App.Turn call: the \"chat\" span WithOTel starts around the streaming StreamChat call the model gateway routes through, closed and exported once app.Close() flushed the batched OTLP exporter (App.Close -> rt.closeFn -> the same shutdownTracing func startRuntime built the tracer with). ", res.SpanCount))
	} else {
		b.WriteString("This run captured 0 spans -- either the representative turn failed before the span closed, the batched exporter had not flushed by the time app.Close() returned, or OTel emission regressed; see \"error\". ")
	}
	b.WriteString("Complementary trail: aisotropy also ships a hash-chained, tamper-EVIDENT audit sink (internal/audit/hashchain.go, ADR-0056) -- each record commits SHA-256(prev_hash || canonical(event)), so any edit/removal/reorder of a past identity+request-metadata record breaks the chain (independently checkable via internal/audit/cmd/audit-verify). That is a DIFFERENT concern from tracing (integrity of the request log, not latency/attribute observability) and is not wired into this observability-mode run, but it complements OTel spans as a second, independent observability signal aisotropy ships natively. ")
	b.WriteString("Structured logging: aisotropy's facade logs via the standard log/slog package (slog.Info/slog.Error calls throughout aisotropy/app.go, including the otel-tracer-shutdown-failure log site itself) using whatever handler is installed as slog.Default() at call time -- it ships no handler of its own. This adapter installs a slog.JSONHandler in front of the process-wide default BEFORE booting the App (the same technique a real integrator wiring log aggregation would use), so every entry aisotropy emits is captured as real, typed JSON, never fabricated.")
	if turnErr != nil {
		b.WriteString(fmt.Sprintf(" The representative turn itself returned an error this pass: %v -- span/log capture above still reflects whatever executed up to that point.", turnErr))
	}
	if closeErr != nil {
		b.WriteString(fmt.Sprintf(" app.Close() (which flushes the OTLP batch exporter) returned an error this pass: %v.", closeErr))
	}
	return b.String()
}
