package api

import (
	"context"
	"testing"

	"github.com/go-chi/chi/v5/middleware"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/Luum-Home/aisotropy/internal/o11y/keys"
)

// recordedSpanCtx starts a REAL (recorded, sampled) span via a provider wired
// to a tracetest.SpanRecorder and returns its ctx + a cleanup that shuts the
// provider down — the ADR-0066 testing-kit pattern.
func recordedSpanCtx(t *testing.T) context.Context {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("provider shutdown: %v", err)
		}
	})
	ctx, span := provider.Tracer("bridge-test").Start(context.Background(), "boundary")
	t.Cleanup(func() { span.End() })
	return ctx
}

// TestStampTelemetryFromRecordedSpan: with a valid recorded span in ctx, the
// bridge stamps its exact trace_id/span_id plus request/session/identity.
func TestStampTelemetryFromRecordedSpan(t *testing.T) {
	ctx := recordedSpanCtx(t)
	sc := trace.SpanContextFromContext(ctx)

	got := stampTelemetry(ctx, "acme", "alice", "session-42")

	if v, ok := keys.Value(got, keys.TraceID); !ok || v != sc.TraceID().String() {
		t.Fatalf("trace_id = (%q, %v), want %q", v, ok, sc.TraceID().String())
	}
	if v, ok := keys.Value(got, keys.SpanID); !ok || v != sc.SpanID().String() {
		t.Fatalf("span_id = (%q, %v), want %q", v, ok, sc.SpanID().String())
	}
	if v, ok := keys.Value(got, keys.RequestID); !ok || v == "" {
		t.Fatalf("request_id must be generated when chi's middleware is absent, got (%q, %v)", v, ok)
	}
	if v, _ := keys.Value(got, keys.SessionID); v != "session-42" {
		t.Fatalf("session_id = %q", v)
	}
	if v, _ := keys.Value(got, keys.TenantID); v != "acme" {
		t.Fatalf("tenant_id = %q", v)
	}
	if v, _ := keys.Value(got, keys.UserID); v != "alice" {
		t.Fatalf("user_id = %q", v)
	}
}

// TestStampTelemetryNoSpan: without an active span, no trace_id/span_id are
// stamped (never a bogus zero id), while request_id/identity still are.
func TestStampTelemetryNoSpan(t *testing.T) {
	got := stampTelemetry(context.Background(), "acme", "alice", "")

	if _, ok := keys.Value(got, keys.TraceID); ok {
		t.Fatal("trace_id must not be stamped without a valid span context")
	}
	if _, ok := keys.Value(got, keys.SpanID); ok {
		t.Fatal("span_id must not be stamped without a valid span context")
	}
	if _, ok := keys.Value(got, keys.SessionID); ok {
		t.Fatal("empty session must not stamp session_id")
	}
	if v, ok := keys.Value(got, keys.RequestID); !ok || v == "" {
		t.Fatal("request_id must always be present after the bridge")
	}
	if v, _ := keys.Value(got, keys.TenantID); v != "acme" {
		t.Fatalf("tenant_id = %q", v)
	}
}

// TestStampTelemetryReusesChiRequestID: when chi's RequestID middleware ran,
// its id is reused verbatim rather than generating a second one.
func TestStampTelemetryReusesChiRequestID(t *testing.T) {
	ctx := context.WithValue(context.Background(), middleware.RequestIDKey, "chi-req-7")

	got := stampTelemetry(ctx, "acme", "alice", "")

	if v, _ := keys.Value(got, keys.RequestID); v != "chi-req-7" {
		t.Fatalf("request_id = %q, want the chi middleware id \"chi-req-7\"", v)
	}
}

// TestNewRequestIDShape: generated ids are 32 hex chars and unique per call.
func TestNewRequestIDShape(t *testing.T) {
	a, b := newRequestID(), newRequestID()
	if len(a) != 32 || len(b) != 32 {
		t.Fatalf("generated request ids must be 32 hex chars, got %q / %q", a, b)
	}
	if a == b {
		t.Fatal("two generated request ids collided")
	}
}
