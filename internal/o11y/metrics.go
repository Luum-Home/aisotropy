// Package o11y — ADR-0068 Layer 4: OTLP-push MeterProvider with RED instruments.
//
// This file provides a MeterProvider that pushes metrics via OTLP HTTP
// (otlpmetrichttp, same endpoint discipline as traces — env-gated, no-op
// when unset). It creates RED instruments (counters + latency histograms)
// per architectural layer, with low-cardinality labels from the
// internal/o11y/keys vocabulary.
//
// Instrument-creation errors are logged and swallowed — telemetry never
// breaks a request.
package o11y

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	otelmetrichttp "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"

	"github.com/Luum-Home/aisotropy/internal/o11y/keys"
)

// MeterProvider is ADR-0068 Layer 4: an OTLP-push meter provider with RED
// instruments per architectural layer. It wraps the OTel SDK MeterProvider
// and exposes a per-layer Meter that creates counters and histograms with
// the correct naming convention and low-cardinality labels.
//
// Construct once at app startup via NewMeterProvider; pass the resulting
// *MeterProvider to otel.SetMeterProvider so all instrumented code shares
// it. When OTEL_EXPORTER_OTLP_ENDPOINT is unset, NewMeterProvider returns a
// no-op provider — telemetry is off, requests proceed normally.
type MeterProvider struct {
	provider *sdkmetric.MeterProvider
	// meters caches one meter per MeterScope so instrument creation is
	// idempotent across the process lifetime.
	meters map[keys.MeterScope]metric.Meter
	// serviceAttrs are the default labels every instrument carries.
	serviceAttrs []attribute.KeyValue
}

// NewMeterProvider builds an OTLP-push MeterProvider. It reads
// OTEL_EXPORTER_OTLP_ENDPOINT (same as traces) for the collector endpoint;
// when unset, returns a no-op provider that creates no-op instruments.
//
// The endpoint discipline matches traces (ADR-0065): OTLP HTTP push, not a
// Prometheus pull surface. The push interval defaults to 10s (overridable
// via OTEL_EXPORTER_OTLP_METRIC_PERIOD).
func NewMeterProvider(serviceName, serviceVersion string) *MeterProvider {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return &MeterProvider{
			provider:     sdkmetric.NewMeterProvider(),
			meters:       make(map[keys.MeterScope]metric.Meter),
			serviceAttrs: serviceAttrs(serviceName, serviceVersion),
		}
	}

	host := stripScheme(endpoint)

	exporter, err := otelmetrichttp.New(
		context.Background(),
		otelmetrichttp.WithEndpoint(host),
		otelmetrichttp.WithInsecure(),
	)
	if err != nil {
		slog.Warn("o11y: failed to create OTLP metrics exporter, metrics disabled",
			"error", err)
		return &MeterProvider{
			provider:     sdkmetric.NewMeterProvider(),
			meters:       make(map[keys.MeterScope]metric.Meter),
			serviceAttrs: serviceAttrs(serviceName, serviceVersion),
		}
	}

	period := 10 * time.Second
	if p := os.Getenv("OTEL_EXPORTER_OTLP_METRIC_PERIOD"); p != "" {
		if d, err := time.ParseDuration(p); err == nil {
			period = d
		}
	}

	reader := sdkmetric.NewPeriodicReader(exporter,
		sdkmetric.WithInterval(period),
	)

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(resource.NewWithAttributes(
			"service.name",
			semconv.ServiceName(serviceName),
		)),
	)

	return &MeterProvider{
		provider:     mp,
		meters:       make(map[keys.MeterScope]metric.Meter),
		serviceAttrs: serviceAttrs(serviceName, serviceVersion),
	}
}

// Meter returns the metric.Meter for the given architectural layer (api, llm,
// memory, facts, semantic, outbox). The meter is cached — calling Meter
// twice with the same scope returns the same meter.
func (mp *MeterProvider) Meter(scope keys.MeterScope) metric.Meter {
	if m, ok := mp.meters[scope]; ok {
		return m
	}
	m := mp.provider.Meter(string(scope))
	mp.meters[scope] = m
	return m
}

// ServiceAttrs returns the default labels (service_module, service_package,
// service_component) that every instrument should carry. These are set
// explicitly at module boundaries per ADR-0068 (no stack-walking).
func (mp *MeterProvider) ServiceAttrs() []attribute.KeyValue {
	return mp.serviceAttrs
}

// Stop flushes any pending metrics and shuts down the provider. Safe to call
// during graceful shutdown.
func (mp *MeterProvider) Stop(ctx context.Context) error {
	return mp.provider.Shutdown(ctx)
}

// serviceAttrs builds the default service labels from the process name and
// the provided service identity.
func serviceAttrs(name, version string) []attribute.KeyValue {
	return []attribute.KeyValue{
		semconv.ServiceName(name),
		semconv.ServiceVersion(version),
		attribute.String(string(keys.ServiceModule), "aisotropy"),
	}
}

// stripScheme removes the scheme (http:// or https://) from an endpoint URL,
// leaving host[:port] as otlpmetrichttp expects.
func stripScheme(endpoint string) string {
	if i := indexScheme(endpoint); i != -1 {
		return endpoint[i:]
	}
	return endpoint
}

// indexScheme returns the index of the first character after the scheme
// (e.g. "http://host:4318" → 7), or -1 if no scheme is present.
func indexScheme(s string) int {
	for i := 0; i+2 < len(s); i++ {
		if s[i] == ':' && s[i+1] == '/' && s[i+2] == '/' {
			return i + 3
		}
	}
	return -1
}

// Counter is a typed wrapper around an OTel Int64Counter that carries the
// service attributes by default. It is the primary RED counter instrument.
type Counter struct {
	counter metric.Int64Counter
	attrs   []attribute.KeyValue
}

// NewCounter creates a RED counter under the given meter with the given
// domain name (e.g. "chat_request") and the service attributes as defaults.
// The resulting counter's Add method accepts additional labels per call.
func NewCounter(meter metric.Meter, name string, serviceAttrs []attribute.KeyValue) *Counter {
	c, err := meter.Int64Counter(
		fmt.Sprintf("%s_total", name),
		metric.WithDescription(fmt.Sprintf("RED counter: %s", name)),
		metric.WithUnit("1"),
	)
	if err != nil {
		slog.Warn("o11y: failed to create counter, using no-op",
			"counter", name, "error", err)
		return &Counter{}
	}
	return &Counter{counter: c, attrs: serviceAttrs}
}

// Add increments the counter by n, adding the given key-value labels.
// The service attributes are always included.
func (c *Counter) Add(ctx context.Context, n int64, kv ...attribute.KeyValue) {
	if c.counter == nil {
		return
	}
	attrs := make([]attribute.KeyValue, 0, len(c.attrs)+len(kv))
	attrs = append(attrs, c.attrs...)
	attrs = append(attrs, kv...)
	c.counter.Add(ctx, n, metric.WithAttributes(attrs...))
}

// Histogram is a typed wrapper around an OTel Float64Histogram for latency
// measurements. It is the primary RED latency instrument.
type Histogram struct {
	hist  metric.Float64Histogram
	attrs []attribute.KeyValue
}

// NewHistogram creates a latency histogram under the given meter with the
// given name (e.g. "chat_request_duration") and the service attributes as
// defaults.
func NewHistogram(meter metric.Meter, name string, serviceAttrs []attribute.KeyValue) *Histogram {
	h, err := meter.Float64Histogram(
		fmt.Sprintf("%s_ms", name),
		metric.WithDescription(fmt.Sprintf("RED latency: %s", name)),
		metric.WithUnit("ms"),
	)
	if err != nil {
		slog.Warn("o11y: failed to create histogram, using no-op",
			"histogram", name, "error", err)
		return &Histogram{}
	}
	return &Histogram{hist: h, attrs: serviceAttrs}
}

// Record records a latency value in milliseconds, adding the given labels.
func (h *Histogram) Record(ctx context.Context, ms float64, kv ...attribute.KeyValue) {
	if h.hist == nil {
		return
	}
	attrs := make([]attribute.KeyValue, 0, len(h.attrs)+len(kv))
	attrs = append(attrs, h.attrs...)
	attrs = append(attrs, kv...)
	h.hist.Record(ctx, ms, metric.WithAttributes(attrs...))
}

// RED instruments for the API layer. These are the primary request-level
// metrics: chat_request_ok_total, chat_request_error_total, and
// chat_request_duration_ms.
type APIMetrics struct {
	OK       *Counter
	Error    *Counter
	Duration *Histogram
}

// NewAPIMetrics creates the RED instruments for the API layer.
func NewAPIMetrics(mp *MeterProvider) *APIMetrics {
	meter := mp.Meter(keys.MeterAPI)
	attrs := mp.ServiceAttrs()
	return &APIMetrics{
		OK:       NewCounter(meter, "chat_request_ok", attrs),
		Error:    NewCounter(meter, "chat_request_error", attrs),
		Duration: NewHistogram(meter, "chat_request_duration", attrs),
	}
}

// RED instruments for the LLM layer.
type LLMMetrics struct {
	CallsOK    *Counter
	CallsError *Counter
	TokensIn   *Counter
	TokensOut  *Counter
	TokensRsn  *Counter
	Duration   *Histogram
}

// NewLLMMetrics creates the RED instruments for the LLM layer.
func NewLLMMetrics(mp *MeterProvider) *LLMMetrics {
	meter := mp.Meter(keys.MeterLLM)
	attrs := mp.ServiceAttrs()
	return &LLMMetrics{
		CallsOK:    NewCounter(meter, "llm_calls_ok", attrs),
		CallsError: NewCounter(meter, "llm_calls_error", attrs),
		TokensIn:   NewCounter(meter, "llm_tokens_input", attrs),
		TokensOut:  NewCounter(meter, "llm_tokens_output", attrs),
		TokensRsn:  NewCounter(meter, "llm_tokens_reasoning", attrs),
		Duration:   NewHistogram(meter, "llm_call_duration", attrs),
	}
}


