// Error ergonomics for ADR-0066 Layer 2: one helper that logs an error with
// the standard `error` field and, when the error carries its own program
// counters, a formatted `stack_trace` — the ONLY stack-trace path in the
// codebase. Per-call stack-walking anywhere else is deliberately rejected by
// the ADR (too costly); an error that wants a stack captures it at creation
// time by implementing StackTrace.
package o11y

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
)

// stackTracer is the optional error capability Error looks for: an error that
// captured its program counters at creation time (e.g. via runtime.Callers).
// Matched through the error chain with errors.As, so a wrapped stack-carrying
// error still yields its trace.
type stackTracer interface {
	StackTrace() []uintptr
}

// Error logs err at error level via logger, appending the standard fields the
// vocabulary prescribes on top of the caller's kv pairs:
//
//   - "error": err.Error()
//   - "stack_trace": formatted frames, ONLY when err (or an error in its
//     chain) implements StackTrace() []uintptr
//
// The ctx metadata (trace_id, tenant_id, ...) rides in via the CtxHandler as
// on any other *Context call — Error adds no correlation fields itself. A nil
// logger falls back to slog.Default(); a nil err logs msg with the kv pairs
// only (no "error" field), so call sites need no nil-guard.
func Error(ctx context.Context, logger *slog.Logger, err error, msg string, kv ...any) {
	if logger == nil {
		logger = slog.Default()
	}
	args := make([]any, 0, len(kv)+4)
	args = append(args, kv...)
	if err != nil {
		args = append(args, "error", err.Error())
		var st stackTracer
		if errors.As(err, &st) {
			if frames := formatStack(st.StackTrace()); len(frames) > 0 {
				args = append(args, "stack_trace", frames)
			}
		}
	}
	logger.ErrorContext(ctx, msg, args...)
}

// formatStack renders captured program counters as "function file:line"
// strings, one per resolvable frame. Unresolvable frames (a zero runtime.Frame
// from a bogus pc) are skipped rather than rendered empty.
func formatStack(pcs []uintptr) []string {
	if len(pcs) == 0 {
		return nil
	}
	frames := runtime.CallersFrames(pcs)
	out := make([]string, 0, len(pcs))
	for {
		frame, more := frames.Next()
		if frame.PC != 0 {
			out = append(out, fmt.Sprintf("%s %s:%d", frame.Function, frame.File, frame.Line))
		}
		if !more {
			break
		}
	}
	return out
}
