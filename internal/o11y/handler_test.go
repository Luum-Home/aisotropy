package o11y

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/o11y/keys"
)

// fixedRecord builds a record with a pinned timestamp and zero PC so two
// handlers fed the same record produce comparable bytes.
func fixedRecord(msg string, attrs ...slog.Attr) slog.Record {
	r := slog.NewRecord(time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC), slog.LevelInfo, msg, 0)
	r.AddAttrs(attrs...)
	return r
}

// TestCtxHandlerNoMetadataByteEquivalent: with no ctx metadata the wrapper's
// output is byte-identical to the unwrapped handler's.
func TestCtxHandlerNoMetadataByteEquivalent(t *testing.T) {
	var wrapped, plain bytes.Buffer
	wh := NewCtxHandler(slog.NewJSONHandler(&wrapped, nil))
	ph := slog.NewJSONHandler(&plain, nil)

	ctx := context.Background()
	rec := fixedRecord("hello", slog.String("k", "v"), slog.Int("n", 7))
	if err := wh.Handle(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := ph.Handle(ctx, fixedRecord("hello", slog.String("k", "v"), slog.Int("n", 7))); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wrapped.Bytes(), plain.Bytes()) {
		t.Fatalf("no-metadata output diverged:\nwrapped: %s\nplain:   %s", wrapped.String(), plain.String())
	}
}

// TestCtxHandlerAppendsTelemetryAndIdentity: a ctx carrying metadata emits
// the telemetry+identity bucket fields on the JSON line.
func TestCtxHandlerAppendsTelemetryAndIdentity(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewCtxHandler(slog.NewJSONHandler(&buf, nil)))

	ctx := keys.With(context.Background(), keys.TraceID, "0af7651916cd43dd8448eb211c80319c")
	ctx = keys.With(ctx, keys.SpanID, "b7ad6b7169203331")
	ctx = keys.With(ctx, keys.RequestID, "req-1")
	ctx = keys.With(ctx, keys.SessionID, "session-42")
	ctx = keys.With(ctx, keys.TenantID, "acme")
	ctx = keys.With(ctx, keys.UserID, "alice")

	logger.InfoContext(ctx, "chat request completed", "attempts", 1)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("output is not one JSON line: %v (%s)", err, buf.String())
	}
	want := map[string]string{
		"trace_id":   "0af7651916cd43dd8448eb211c80319c",
		"span_id":    "b7ad6b7169203331",
		"request_id": "req-1",
		"session_id": "session-42",
		"tenant_id":  "acme",
		"user_id":    "alice",
	}
	for k, v := range want {
		if line[k] != v {
			t.Fatalf("field %q = %v, want %q (line: %s)", k, line[k], v, buf.String())
		}
	}
	if line["attempts"] != float64(1) {
		t.Fatalf("call-site attr lost: attempts = %v", line["attempts"])
	}
}

// TestCtxHandlerDedupLastWins: a call-site attr colliding with a ctx field is
// dropped in favor of the ctx value, and the key appears exactly once.
func TestCtxHandlerDedupLastWins(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewCtxHandler(slog.NewJSONHandler(&buf, nil)))

	ctx := keys.With(context.Background(), keys.TenantID, "ctx-tenant")
	logger.InfoContext(ctx, "msg", "tenant_id", "callsite-tenant")

	out := buf.String()
	if got := strings.Count(out, `"tenant_id"`); got != 1 {
		t.Fatalf("tenant_id appears %d times, want exactly 1: %s", got, out)
	}
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line["tenant_id"] != "ctx-tenant" {
		t.Fatalf("dedup is not last-wins: tenant_id = %v, want \"ctx-tenant\"", line["tenant_id"])
	}
}

// TestCtxHandlerServiceExecutionNotAppended: only the telemetry+identity
// buckets ride onto log lines at this layer; service/execution metadata in
// ctx stays off the record.
func TestCtxHandlerServiceExecutionNotAppended(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewCtxHandler(slog.NewJSONHandler(&buf, nil)))

	ctx := keys.With(context.Background(), keys.ServiceModule, "api")
	ctx = keys.With(ctx, keys.ExecutionJobID, "job-1")
	ctx = keys.With(ctx, keys.TenantID, "acme")
	logger.InfoContext(ctx, "msg")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if _, ok := line["service_module"]; ok {
		t.Fatalf("service bucket must not be appended to log lines: %s", buf.String())
	}
	if _, ok := line["execution_job_id"]; ok {
		t.Fatalf("execution bucket must not be appended to log lines: %s", buf.String())
	}
	if line["tenant_id"] != "acme" {
		t.Fatalf("identity bucket missing: %s", buf.String())
	}
}

// TestCtxHandlerWithAttrsAndGroupStayWrapped: derived loggers (With /
// WithGroup) keep hydrating from ctx.
func TestCtxHandlerWithAttrsAndGroupStayWrapped(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewCtxHandler(slog.NewJSONHandler(&buf, nil))).With("component", "test")

	ctx := keys.With(context.Background(), keys.TraceID, "t1")
	logger.InfoContext(ctx, "msg")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line["trace_id"] != "t1" {
		t.Fatalf("derived logger lost ctx hydration: %s", buf.String())
	}
	if line["component"] != "test" {
		t.Fatalf("pre-bound attr lost: %s", buf.String())
	}
}
