package llm

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// ReflectionBackend selects which reflection loop engine to use: real Gollem
// (Phase 2, see gollem.go) or the Phase 1 homegrown loop (Reflect[T], see
// reflect.go). ADR-0008 (docs/decisions/0008-reflection-loop-gollem-deferral.md)
// requires keeping Reflect[T] as a fallback, not removing it.
type ReflectionBackend string

const (
	// ReflectionBackendGollem uses core.Agent[T] + core.RunWithReflection[T]
	// (gollem.go). It is the Phase 2 default.
	ReflectionBackendGollem ReflectionBackend = "gollem"
	// ReflectionBackendOwn uses the homegrown Reflect[T] loop (reflect.go),
	// functionally equivalent, without the Gollem dependency.
	ReflectionBackendOwn ReflectionBackend = "own"
)

// reflectionBackendEnvVar explicitly forces a backend ("gollem"|"own").
// Unset, the default is Gollem, with automatic fallback to Reflect[T] if
// Gollem fails (see ReflectAuto).
const reflectionBackendEnvVar = "AISOTROPY_REFLECTION_BACKEND"

// ReflectionBackendFromEnv reads AISOTROPY_REFLECTION_BACKEND. Any value
// other than "own" (case-insensitive) resolves to Gollem — i.e. Gollem is
// the default and the homegrown backend must be explicitly requested to
// bypass it.
func ReflectionBackendFromEnv() ReflectionBackend {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(reflectionBackendEnvVar)), "own") {
		return ReflectionBackendOwn
	}
	return ReflectionBackendGollem
}

// ReflectAuto runs the reflection loop with the backend resolved by
// ReflectionBackendFromEnv, with automatic fallback: if Gollem was requested
// (or defaulted into) and GollemReflect returns an error —schema retries
// exhausted, the model does not cooperate, or any failure not attributable
// to ctx cancellation— it retries once with the homegrown Reflect[T],
// without spending the retry budget twice over the same ctx error.
//
// decode translates the raw text of a Reflect[T] iteration (the fallback
// path) into type T — the same contract as ValidateFunc[T]. business is the
// business validation over the already-decoded T, with the shape expected
// by core.RunWithReflection (also reused here for the homegrown path):
// "" => accepted, non-empty => request correction with that feedback.
//
// usage carries the real provider-reported token counts for the run (see
// ReflectResult.Usage): populated from core.ReflectResult.Usage on the
// Gollem path, left at its zero value on the homegrown Reflect[T] fallback
// path (which has no usage-carrying return slot — documented, not a bug).
// This is what lets callers (e.g. internal/api.Register's /chat route)
// record real cost to the shared costmeter.Meter for the reflection path,
// the same way chat_stream.go already does for the streaming path.
func ReflectAuto[T any](
	ctx context.Context,
	client Client,
	system, prompt string,
	decode ValidateFunc[T],
	business func(ctx context.Context, out T) (feedback string, err error),
	maxRetries int,
) (out T, attempts int, backend ReflectionBackend, usage Usage, err error) {
	backend = ReflectionBackendFromEnv()

	if backend == ReflectionBackendGollem {
		res, gerr := GollemReflect[T](ctx, client, system, prompt, business, maxRetries)
		if gerr == nil {
			result := ReflectResult[T]{
				Output:   res.Output,
				Attempts: res.Iterations,
				Usage:    Usage{InputTokens: res.Usage.InputTokens, OutputTokens: res.Usage.OutputTokens},
			}
			return result.Output, result.Attempts, ReflectionBackendGollem, result.Usage, nil
		}
		if ctx.Err() != nil {
			// Client cancellation: no point retrying with the homegrown
			// backend, it will cut at the same point.
			var zero T
			return zero, 0, ReflectionBackendGollem, Usage{}, gerr
		}

		slog.WarnContext(ctx, "gollem reflection failed, falling back to homegrown Reflect[T] (ADR-0008 fallback)",
			"err", gerr, "model", client.Name())
	}

	gen := func(ctx context.Context, p, feedback string) (string, error) {
		full := p
		if feedback != "" {
			full = p + "\n\n" + feedback
		}
		// The homegrown Reflect[T] loop (GenerateFunc) has no Usage-carrying
		// return slot — real usage accounting for this fallback path is
		// covered by the caller's own Client (e.g. via WithOTel), not here.
		raw, _, err := generateFull(ctx, client, system, full)
		return raw, err
	}

	validate := func(raw string) (T, error) {
		v, derr := decode(raw)
		if derr != nil {
			return v, derr
		}
		if business == nil {
			return v, nil
		}
		fb, berr := business(ctx, v)
		if berr != nil {
			return v, berr
		}
		if fb != "" {
			return v, fmt.Errorf("%s", fb)
		}
		return v, nil
	}

	result, rerr := Reflect[T](ctx, gen, validate, prompt, maxRetries)
	return result.Output, result.Attempts, ReflectionBackendOwn, result.Usage, rerr
}
