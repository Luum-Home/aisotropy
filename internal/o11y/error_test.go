package o11y

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"testing"
)

// stackErr is a fake error implementing the optional StackTrace capability
// with real program counters, so formatStack resolves genuine frames.
type stackErr struct {
	msg string
	pcs []uintptr
}

func (e *stackErr) Error() string         { return e.msg }
func (e *stackErr) StackTrace() []uintptr { return e.pcs }

// newStackErr captures the caller's stack at creation time — the pattern the
// helper is designed for (capture at creation, format only when logged).
func newStackErr(msg string) *stackErr {
	var pcs [8]uintptr
	n := runtime.Callers(2, pcs[:])
	return &stackErr{msg: msg, pcs: pcs[:n]}
}

// TestErrorPlain: a plain error gets the "error" field and NO "stack_trace".
func TestErrorPlain(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	Error(context.Background(), logger, errors.New("boom"), "operation failed", "op", "hydrate")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line["level"] != "ERROR" {
		t.Fatalf("level = %v, want ERROR", line["level"])
	}
	if line["error"] != "boom" {
		t.Fatalf("error field = %v, want \"boom\"", line["error"])
	}
	if line["op"] != "hydrate" {
		t.Fatalf("kv pair lost: op = %v", line["op"])
	}
	if _, ok := line["stack_trace"]; ok {
		t.Fatalf("plain error must not carry stack_trace: %s", buf.String())
	}
}

// TestErrorStackTrace: an error implementing StackTrace() []uintptr yields a
// formatted stack_trace field whose first frame names this test file's
// constructor call site.
func TestErrorStackTrace(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	Error(context.Background(), logger, newStackErr("kaput"), "operation failed")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	frames, ok := line["stack_trace"].([]any)
	if !ok || len(frames) == 0 {
		t.Fatalf("stack_trace missing or empty: %s", buf.String())
	}
	first, _ := frames[0].(string)
	if !strings.Contains(first, "TestErrorStackTrace") || !strings.Contains(first, "error_test.go") {
		t.Fatalf("first frame does not point at the capture site: %q", first)
	}
}

// TestErrorWrappedStackTrace: the capability is matched through the error
// chain (errors.As), so a wrapped stack-carrying error still yields frames.
func TestErrorWrappedStackTrace(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	wrapped := fmt.Errorf("outer: %w", newStackErr("inner"))
	Error(context.Background(), logger, wrapped, "operation failed")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line["error"] != "outer: inner" {
		t.Fatalf("error field = %v", line["error"])
	}
	if _, ok := line["stack_trace"].([]any); !ok {
		t.Fatalf("wrapped stack-carrying error lost its stack_trace: %s", buf.String())
	}
}

// TestErrorNilErr: a nil err logs msg + kv only, with no error field.
func TestErrorNilErr(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	Error(context.Background(), logger, nil, "just a message", "k", "v")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if _, ok := line["error"]; ok {
		t.Fatalf("nil err must not emit an error field: %s", buf.String())
	}
	if line["k"] != "v" {
		t.Fatalf("kv pair lost: %s", buf.String())
	}
}
