package llm

import (
	"context"
	"fmt"
)

// Reflection loop (output repair) — Phase 1.
//
// docs/components/gollem-orchestrator.md describes this mechanism as
// Gollem's responsibility (github.com/fugue-labs/gollem): force structured
// output against the Go struct and retry with the exact typing error if
// validation fails, up to N bounded attempts.
//
// TODO(phase-2): replace this homegrown loop with real Gollem. The API was
// investigated with `go doc` (package github.com/fugue-labs/gollem/core,
// v0.5.1) and the exact primitive the docs describe exists:
//
//	func RunWithReflection[T any](
//	    ctx context.Context, agent *Agent[T], prompt string,
//	    validator func(ctx context.Context, output T) (feedback string, err error),
//	    opts ...ReflectOption,
//	) (*ReflectResult[T], error)
//
// It was not integrated in this phase because it additionally requires
// wrapping Client (this package) behind the core.Model interface
// (Request/RequestStream/ModelName) and building an Agent[T] with its own
// schema derivation — non-trivial overhead for a walking skeleton that, in
// this environment, cannot even exercise the real Anthropic provider
// (ANTHROPIC_API_KEY is missing). Priority is given to making it compile and
// the SSE work (see the task instructions); Gollem remains a Phase 2 upgrade
// over this same Reflect/GenerateFunc/ValidateFunc interface, which already
// models the same contract (generate -> validate -> retry with the exact
// error).
//
// This minimal loop is functionally equivalent for the current use case:
// generic over T, an explicit retry cap (never infinite), and feedback =
// the exact validation error from the previous iteration.

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
