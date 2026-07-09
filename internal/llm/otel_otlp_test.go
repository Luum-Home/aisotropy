package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// capturedExportRequest records what the fake OTLP collector observed about
// one incoming HTTP request, without needing to decode the OTLP/protobuf
// payload itself — decoding it is unnecessary to prove the export path is
// wired end to end (method, path, and a non-empty body already prove
// NewTracerProvider's otlptracehttp exporter actually posted a real export).
type capturedExportRequest struct {
	method  string
	path    string
	bodyLen int
}

// fakeOTLPCollector is an httptest.Server standing in for a real OTLP/HTTP
// collector: it accepts any POST and returns 200 with an empty body (an
// empty byte string is a valid protobuf encoding of an
// ExportTraceServiceResponse with all-default fields), while recording every
// request it received for the test to assert on.
type fakeOTLPCollector struct {
	mu       sync.Mutex
	requests []capturedExportRequest
}

func (f *fakeOTLPCollector) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, capturedExportRequest{method: r.Method, path: r.URL.Path, bodyLen: len(body)})
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
}

func (f *fakeOTLPCollector) Requests() []capturedExportRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capturedExportRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

// TestOTLPHTTPExportPathSendsSpansToConfiguredEndpoint proves, without a
// real collector, that NewTracerProvider's OTLP/HTTP export path
// (otlptracehttp, otel.go) actually posts span data over the network when
// OTEL_EXPORTER_OTLP_ENDPOINT is configured — the "live" half of otel.go's
// contract that TestWithOTel_* (otel_test.go) cannot exercise, since those
// tests inject an in-memory tracetest.SpanRecorder instead of going through
// NewTracerProvider at all.
//
// OTEL_EXPORTER_OTLP_ENDPOINT (the general, non-signal-specific env var) is
// used rather than OTEL_EXPORTER_OTLP_TRACES_ENDPOINT: per the OTel spec
// (confirmed by reading otlptracehttp's vendored
// internal/otlpconfig/envconfig.go), the *_ENDPOINT variant treats the value
// as a base URL and appends "/v1/traces" itself, while the signal-specific
// *_TRACES_ENDPOINT variant would require the path to already be present.
// Using the base-URL form lets this test assert on the exact path the SDK
// derives, rather than guessing it.
func TestOTLPHTTPExportPathSendsSpansToConfiguredEndpoint(t *testing.T) {
	collector := &fakeOTLPCollector{}
	srv := httptest.NewServer(http.HandlerFunc(collector.handler))
	defer srv.Close()

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)

	ctx := context.Background()
	shutdown, tracer, _ := NewTracerProvider(ctx)

	client := WithOTel(&StubClient{}, tracer)
	tokens, err := client.StreamChat(ctx, "sys", "prompt")
	if err != nil {
		t.Fatalf("StreamChat returned error: %v", err)
	}
	drainTokens(tokens)

	if err := shutdown(ctx); err != nil {
		t.Fatalf("tracer provider shutdown (which flushes the batch exporter) returned error: %v", err)
	}

	requests := collector.Requests()
	if len(requests) == 0 {
		t.Fatal("expected the OTLP/HTTP exporter to POST at least one export request to the fake collector, got none — the OTLP export path is not wired end to end")
	}
	for _, req := range requests {
		if req.method != http.MethodPost {
			t.Errorf("expected an exported span to arrive as POST, got %s %s", req.method, req.path)
		}
		if req.path != "/v1/traces" {
			t.Errorf("expected the export request path to be /v1/traces (per otlptracehttp's ENDPOINT auto-append), got %q", req.path)
		}
		if req.bodyLen == 0 {
			t.Errorf("expected a non-empty OTLP export body, got an empty body for %s", req.path)
		}
	}
}

// TestWithOTel_AttachesTenantAndUserAttributesFromContext verifies the
// context.go + otel.go enrichment this change adds: when the ctx passed to
// StreamChat carries tenant/user (via llm.WithTenant/llm.WithUser — what
// internal/api/chat_stream.go now does after withIdentity), the resulting
// span carries tenant_id/user_id alongside the existing gen_ai.* attributes.
func TestWithOTel_AttachesTenantAndUserAttributesFromContext(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tracer := tp.Tracer("test")

	stub := &StubClient{}
	wrapped := WithOTel(stub, tracer)

	ctx := WithTenant(context.Background(), "acme")
	ctx = WithUser(ctx, "alice")

	tokens, err := wrapped.StreamChat(ctx, "sys", "hello")
	if err != nil {
		t.Fatalf("StreamChat returned error: %v", err)
	}
	drainTokens(tokens)

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("expected exactly 1 recorded span, got %d", len(ended))
	}
	attrs := ended[0].Attributes()

	tenant, ok := attrValue(attrs, attrTenantID)
	if !ok || tenant.AsString() != "acme" {
		t.Errorf("tenant_id = %v (ok=%v), want %q", tenant, ok, "acme")
	}
	user, ok := attrValue(attrs, attrUserID)
	if !ok || user.AsString() != "alice" {
		t.Errorf("user_id = %v (ok=%v), want %q", user, ok, "alice")
	}
}

// TestWithOTel_OmitsTenantAndUserAttributesWithoutContext verifies the
// enrichment is opt-in: a ctx with no tenant/user set (the pre-existing
// behavior every other otel_test.go case relies on) produces a span with
// neither attribute present, rather than empty-string attributes.
func TestWithOTel_OmitsTenantAndUserAttributesWithoutContext(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tracer := tp.Tracer("test")

	wrapped := WithOTel(&StubClient{}, tracer)

	tokens, err := wrapped.StreamChat(context.Background(), "sys", "hello")
	if err != nil {
		t.Fatalf("StreamChat returned error: %v", err)
	}
	drainTokens(tokens)

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("expected exactly 1 recorded span, got %d", len(ended))
	}
	attrs := ended[0].Attributes()

	if _, ok := attrValue(attrs, attrTenantID); ok {
		t.Error("expected no tenant_id attribute when ctx carries no tenant")
	}
	if _, ok := attrValue(attrs, attrUserID); ok {
		t.Error("expected no user_id attribute when ctx carries no user")
	}
}
