// Package o11y is ADR-0068 Layer 2: auto-hydration of the typed tag
// vocabulary (internal/o11y/keys) into every signal. This file provides the
// slog side — a Handler wrapper that appends the telemetry+identity buckets
// from ctx to every record logged with a *Context call, so
// logger.InfoContext(ctx, ...) is enough for trace↔log correlation with no
// per-call field plumbing. The span side (boundary bridge) lives in
// internal/api, where identity already enters ctx at the Huma boundary.
package o11y

import (
	"context"
	"log/slog"

	"github.com/Luum-Home/aisotropy/internal/o11y/keys"
)

// CtxHandler wraps an inner slog.Handler and, for records whose ctx carries
// keys metadata, appends the telemetry and identity bucket fields to the
// record before delegating. Behavior-preserving by construction: a ctx with
// no metadata (any non-request log line, and every log line before the
// wrapper is installed) delegates the record UNCHANGED — byte-identical
// output to the unwrapped handler.
//
// Dedup is last-wins at the record level: a call-site attr whose key collides
// with a ctx-provided field (e.g. an existing `"tenant_id", in.TenantID`
// pair) is dropped in favor of the ctx value, so a line never carries the
// same key twice. Attrs pre-bound via Logger.With/WithAttrs live inside the
// inner handler's state and are out of this wrapper's reach — a known,
// accepted limitation (the codebase logs identity as per-call attrs, not
// pre-bound ones).
type CtxHandler struct {
	inner slog.Handler
}

// NewCtxHandler wraps inner. A nil inner panics early (programmer error) the
// same way slog.New(nil) would on first use.
func NewCtxHandler(inner slog.Handler) *CtxHandler {
	if inner == nil {
		panic("o11y.NewCtxHandler: nil inner handler")
	}
	return &CtxHandler{inner: inner}
}

// Enabled delegates to the inner handler unchanged.
func (h *CtxHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle appends the ctx metadata's telemetry+identity fields (stable bucket
// order) and delegates. See the type doc for the no-metadata fast path and
// the last-wins dedup contract.
func (h *CtxHandler) Handle(ctx context.Context, r slog.Record) error {
	meta := keys.All(ctx)
	if len(meta) == 0 {
		return h.inner.Handle(ctx, r)
	}

	// Collect the fields to append in stable bucket order so output ordering
	// is deterministic (test-assertable, diff-friendly).
	added := make(map[string]struct{}, len(keys.TelemetryBucket)+len(keys.IdentityBucket))
	attrs := make([]slog.Attr, 0, len(keys.TelemetryBucket)+len(keys.IdentityBucket))
	for _, bucket := range [][]keys.Key{keys.TelemetryBucket, keys.IdentityBucket} {
		for _, k := range bucket {
			if v, ok := meta[k]; ok {
				attrs = append(attrs, slog.String(string(k), v))
				added[string(k)] = struct{}{}
			}
		}
	}
	if len(attrs) == 0 {
		return h.inner.Handle(ctx, r)
	}

	// Rebuild the record keeping only non-colliding call-site attrs, then
	// append the ctx fields (last-wins dedup).
	nr := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		if _, dup := added[a.Key]; !dup {
			nr.AddAttrs(a)
		}
		return true
	})
	nr.AddAttrs(attrs...)
	return h.inner.Handle(ctx, nr)
}

// WithAttrs wraps the inner handler's WithAttrs result so ctx hydration keeps
// applying to derived loggers.
func (h *CtxHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &CtxHandler{inner: h.inner.WithAttrs(attrs)}
}

// WithGroup wraps the inner handler's WithGroup result. Note: for records
// logged under a group, appended ctx fields land inside that group's
// namespace (standard slog wrapper semantics).
func (h *CtxHandler) WithGroup(name string) slog.Handler {
	return &CtxHandler{inner: h.inner.WithGroup(name)}
}
