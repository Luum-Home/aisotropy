package llm

import (
	"context"
	"fmt"
)

// Reflection loop (output repair) — the homegrown FALLBACK.
//
// The PRIMARY reflection path is now Gollem, and it shipped: GollemReflect
// (gollem.go) mounts core.Agent[T] + core.RunWithReflection[T] +
// core.WithMaxRetries[T], and ReflectAuto (reflect_auto.go) makes it the
// default. The fail-closed HTTP wiring (internal/api/chat.go) surfaces a 502 on
// retry exhaustion — never a silent unstructured fallback — which is exactly the
// constitutional structured-output gate. See ADR-0008 (Phase-2 outcome) and the
// gollem-exploitation audit (reflection slice), which verified this reuse is
// correct, not a reinvention.
//
// This Reflect[T] is the DEMOTED, intentionally-kept fallback: ReflectAuto routes
// here only when the gollem path errors for a non-ctx reason. It models the same
// contract as core.RunWithReflection — generate -> validate -> retry with the
// exact typing error, up to N bounded attempts — generic over T with an explicit
// retry cap (never infinite), feedback = the previous iteration's validation
// error. It is kept because it carries no gollem dependency and no core.Model
// wrapping requirement, so it still works when the gollem path cannot be
// constructed.

// GenerateFunc produces a raw response for a given prompt. feedback, if not
// empty, is the validation error from the previous iteration — it is
// appended to the prompt so the model can self-correct.
type GenerateFunc func(ctx context.Context, prompt, feedback string) (string, error)

// ValidateFunc validates/decodes a raw response against type T. Must return
// a readable error (it will be passed as feedback to the next attempt) if
// the response does not meet the contract.
type ValidateFunc[T any] func(raw string) (T, error)

// ReflectResult is the result of a reflection loop.
type ReflectResult[T any] struct {
	Output     T
	Attempts   int
	RawOutputs []string
	// Usage carries the real provider-reported token counts for the run, when
	// available (see ReflectAuto). The homegrown Reflect[T] loop below has no
	// usage-carrying return slot on its GenerateFunc contract, so this stays
	// the zero value on that path — a documented gap, not a bug: real usage
	// accounting for structured output is a Gollem-path capability (see
	// gollem.go's toModelResponse/generateFull).
	Usage Usage
}

// Reflect runs the minimal self-repair loop: invokes gen, validates the
// result with validate, and, if it fails, retries passing the exact error as
// feedback — up to maxRetries additional attempts (maxRetries=0 means "a
// single attempt, no retries"). Cuts short if ctx is cancelled, same as any
// other consumer of the request's ctx (see
// docs/architecture/request-lifecycle.md, step 5).
func Reflect[T any](ctx context.Context, gen GenerateFunc, validate ValidateFunc[T], prompt string, maxRetries int) (ReflectResult[T], error) {
	var zero T
	var lastErr error
	feedback := ""

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return ReflectResult[T]{}, err
		}

		raw, err := gen(ctx, prompt, feedback)
		if err != nil {
			return ReflectResult[T]{}, fmt.Errorf("reflect: generate attempt %d: %w", attempt+1, err)
		}

		out, verr := validate(raw)
		if verr == nil {
			return ReflectResult[T]{Output: out, Attempts: attempt + 1, RawOutputs: []string{raw}}, nil
		}

		lastErr = verr
		feedback = fmt.Sprintf("your previous response did not meet the expected contract: %s. Retry, correcting exactly that error.", verr)
	}

	return ReflectResult[T]{Output: zero, Attempts: maxRetries + 1}, fmt.Errorf("reflect: exhausted %d attempts, last error: %w", maxRetries+1, lastErr)
}
