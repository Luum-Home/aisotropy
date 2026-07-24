// Package o11y — ADR-0068 Layer 3: transparent client decorators.
//
// This file provides an http.RoundTripper wrapper that turns every outbound
// HTTP call into a child span with OTel HTTP semantic-convention attributes,
// duration, and error classification — the "1 span → N spans" fan-out that
// makes rqlite, embed-server, and any outbound HTTP visible in traces.
//
// The wrapper is zero-config: wrap any *http.Client's Transport and every
// request inherits the active span from ctx. It is fail-open: tracing
// errors never break the request.
package o11y

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// TracingTransport wraps an inner http.RoundTripper and emits a child span
// for every request, using OTel HTTP semantic conventions
// (http.request.method, http.response.status_code, http.duration_ms,
// net.peer.name, etc.). The URL is redacted to scheme+host+path (no query
// string) to avoid leaking sensitive params into traces.
//
// If inner is nil, http.DefaultTransport is used. If the context carries no
// valid span, the wrapper is a pass-through (delegates without creating a
// span) — so installing it globally is safe even for non-traced code paths.
type TracingTransport struct {
	inner http.RoundTripper
}

// NewTracingTransport wraps inner. A nil inner falls back to
// http.DefaultTransport.
func NewTracingTransport(inner http.RoundTripper) *TracingTransport {
	if inner == nil {
		inner = http.DefaultTransport
	}
	return &TracingTransport{inner: inner}
}

// RoundTrip implements http.RoundTripper. It creates a child span from the
// request context, records the HTTP attributes, and classifies the result:
// 4xx/5xx → Error status with the HTTP status code; 2xx/3xx → OK.
func (t *TracingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	span := trace.SpanFromContext(ctx)

	// Fast path: no active span → delegate without creating one.
	if !span.SpanContext().IsValid() {
		return t.inner.RoundTrip(req)
	}

	// Redact the URL to scheme+host+path (no query string).
	redactedURL := redactURL(req.URL)

	attrs := []attribute.KeyValue{
		attribute.String("http.request.method", req.Method),
		attribute.String("http.url", redactedURL),
		attribute.String("net.peer.name", req.URL.Hostname()),
		attribute.String("server.address", req.URL.Hostname()),
	}
	if req.URL.Scheme != "" {
		attrs = append(attrs, attribute.String("url.scheme", req.URL.Scheme))
	}

	ctx, span = otel.Tracer("o11y").Start(ctx, "HTTP "+req.Method,
		trace.WithAttributes(attrs...),
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	start := time.Now()
	resp, err := t.inner.RoundTrip(req.WithContext(ctx))
	elapsed := time.Since(start)

	span.SetAttributes(attribute.Int("http.duration_ms", int(elapsed.Milliseconds())))

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return resp, err
	}

	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))

	if resp.StatusCode >= 400 {
		span.SetStatus(codes.Error, resp.Status)
	} else {
		span.SetStatus(codes.Ok, "")
	}

	return resp, err
}

// redactURL returns scheme://host/path with the query string stripped, so
// sensitive parameters never appear in trace attributes.
func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	redacted := &url.URL{
		Scheme: u.Scheme,
		Host:   u.Host,
		Path:   u.Path,
	}
	if redacted.Path == "" {
		redacted.Path = "/"
	}
	return redacted.String()
}

// isNotFound returns true when the HTTP status indicates a "not found"
// result — which ADR-0068 explicitly classifies as NOT an error in traces
// (the operation succeeded; the resource simply doesn't exist).
func isNotFound(statusCode int) bool {
	return statusCode == http.StatusNotFound
}

// hasQueryParams returns true when the URL carries query parameters, used
// by callers that want to log a redaction warning.
func hasQueryParams(u *url.URL) bool {
	return u != nil && u.RawQuery != ""
}

// sanitizeHost returns the host with any userinfo stripped, for use in
// net.peer.name attributes where the raw Host might carry credentials.
func sanitizeHost(host string) string {
	if host == "" {
		return ""
	}
	// url.Host can contain userinfo (user:pass@host:port); strip it.
	if at := strings.LastIndex(host, "@"); at != -1 {
		return host[at+1:]
	}
	return host
}
