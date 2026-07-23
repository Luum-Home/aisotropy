// Slim carry/read helpers for the vocabulary over plain context.Context —
// deliberately NOT a bespoke context-manager framework (ADR-0068 §Decision).
// The metadata is an immutable copy-on-write map: With never mutates the
// parent ctx's view, so a ctx handed to a goroutine is race-free by
// construction, matching how identity already travels (internal/api,
// internal/llm/context.go).
package keys

import "context"

// metadataCtxKey is unexported so only With can set the metadata and only
// Value/All can read it — the same pattern internal/api/chat.go and
// internal/llm/context.go use for their identity carriers.
type metadataCtxKey struct{}

// metadata is the immutable key→value view carried in ctx. Values are strings
// across the board (a bool like ExecutionIsAsync is carried as "true"/"false"
// by its setter's caller): every downstream signal — slog fields, span
// attributes, metric labels — renders strings anyway, and one value type
// keeps the helpers slim.
type metadata map[Key]string

// With returns a copy of ctx carrying k=v in the observability metadata.
// Copy-on-write: the parent ctx's metadata is never mutated. A no-op (returns
// ctx unchanged) when k is empty, so callers can pass through unconditionally.
func With(ctx context.Context, k Key, v string) context.Context {
	if k == "" {
		return ctx
	}
	prev, _ := ctx.Value(metadataCtxKey{}).(metadata)
	next := make(metadata, len(prev)+1)
	for pk, pv := range prev {
		next[pk] = pv
	}
	next[k] = v
	return context.WithValue(ctx, metadataCtxKey{}, next)
}

// Value returns the metadata value for k and whether it is set.
func Value(ctx context.Context, k Key) (string, bool) {
	m, _ := ctx.Value(metadataCtxKey{}).(metadata)
	v, ok := m[k]
	return v, ok
}

// All returns a copy of every metadata pair set on ctx — the iterate-all
// helper hydration loops (the slog ctx-handler, the boundary-span attribute
// loop) use. Returns nil when ctx carries no metadata, so callers can
// len-check without allocating.
func All(ctx context.Context) map[Key]string {
	m, _ := ctx.Value(metadataCtxKey{}).(metadata)
	if len(m) == 0 {
		return nil
	}
	out := make(map[Key]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
