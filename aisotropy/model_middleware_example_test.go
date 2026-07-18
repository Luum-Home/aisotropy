package aisotropy

// model_middleware_example_test.go is the ADR-0062 worked example
// (docs/decisions/0062-bounded-model-middleware.md's implementation plan item
// 4): runnable godoc showing WithModelMiddleware end to end. It runs against
// the credential-less stub LLM (internal/llm.StubClient via fastStub), so the
// output is deterministic and no ANTHROPIC_API_KEY is required — the same
// convention example_test.go's Example functions already follow.
//
// Two middlewares are shown:
//
//   - loggingMiddleware: a trivial, self-contained middleware (no external
//     dependency needed for something this small).
//   - redactingMiddleware: a middleware that ADAPTS gollem's existing
//     core.RedactPII message interceptor (github.com/fugue-labs/gollem/core,
//     already a direct dependency of this module — see internal/llm/gollem.go's
//     gollemGuardrailOptions, which attaches the SAME core.RedactPII to every
//     Gollem Agent[T] this runtime builds) rather than hand-rolling a second
//     regex redactor. See docs/decisions/0062-bounded-model-middleware.md's
//     "Reuse of gollem primitives" section for why this ADR's seam is a
//     Client-level decorator (StreamFunc-shaped) instead of gollem's own
//     Agent[T]-scoped core.WithAgentMiddleware, and why bridging INTO gollem's
//     existing interceptor library (rather than reimplementing it) is the
//     right way to reuse it from here.

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/fugue-labs/gollem/core"
)

// loggingMiddleware logs one line per model call (never the prompt content
// itself — see ModelMiddleware's "no secret leak" contract in
// internal/llm/middleware.go) and forwards unchanged.
type loggingMiddleware struct {
	logger *slog.Logger
}

func (m loggingMiddleware) WrapStream(ctx context.Context, system, prompt string, next StreamFunc) (<-chan Token, error) {
	m.logger.Info("model call", "prompt_len", len(prompt), "system_len", len(system))
	return next(ctx, system, prompt)
}

// redactingMiddleware adapts gollem's core.RedactPII — a
// core.MessageInterceptor operating on []core.ModelMessage — into a
// ModelMiddleware operating on this seam's plain (system, prompt string)
// shape: it wraps prompt in the minimal single-UserPromptPart message gollem's
// interceptor expects, runs the REAL core.RedactPII logic (regex match +
// replace), and unwraps the (possibly redacted) result before calling next.
// This is a bridge, not a reimplementation — the actual redaction rule lives
// in gollem, verbatim.
type redactingMiddleware struct {
	interceptor core.MessageInterceptor
}

// newRedactingMiddleware builds a redactingMiddleware over gollem's own PII
// regex/replacement, exactly as internal/llm/gollem.go's
// gollemGuardrailOptions configures it for the built-in Agent[T] guardrail
// (email addresses -> "[REDACTED]").
func newRedactingMiddleware(pattern, replacement string) redactingMiddleware {
	return redactingMiddleware{interceptor: core.RedactPII(pattern, replacement)}
}

func (m redactingMiddleware) WrapStream(ctx context.Context, system, prompt string, next StreamFunc) (<-chan Token, error) {
	msgs := []core.ModelMessage{
		core.ModelRequest{Parts: []core.ModelRequestPart{core.UserPromptPart{Content: prompt}}},
	}
	result := m.interceptor(ctx, msgs)
	if result.Action == core.MessageModify {
		if req, ok := result.Messages[0].(core.ModelRequest); ok {
			for _, part := range req.Parts {
				if up, ok := part.(core.UserPromptPart); ok {
					prompt = up.Content
				}
			}
		}
	}
	return next(ctx, system, prompt)
}

// ExampleWithModelMiddleware builds an App with two chained middlewares — a
// logger (outermost: registered last) and a PII redactor bridged from
// gollem's core.RedactPII (innermost: registered first) — and runs one turn.
// Registration order is innermost-first (see WithModelMiddleware's doc): the
// redactor is registered before the logger, so it sits closer to the base
// client and the logger observes the ALREADY-redacted prompt on the way in.
func ExampleWithModelMiddleware() {
	redactor := newRedactingMiddleware(`[\w.+\-]+@[\w\-]+(?:\.[\w\-]+)+`, "[REDACTED]")
	logger := loggingMiddleware{logger: slog.New(slog.NewTextHandler(io.Discard, nil))} // quiet: this is a doc example, not a log demo

	app := New(
		WithLLM(fastStub()),
		WithRateLimit(1e9, 1e9),
		WithModelMiddleware(redactor), // innermost: closest to the base client
		WithModelMiddleware(logger),   // outermost: runs first
	)
	defer func() { _ = app.Close() }()

	out, err := app.Turn(context.Background(), ChatInput{
		TenantID:  "acme",
		UserID:    "alice",
		SessionID: "s1",
		Prompt:    "contact me at alice@example.com",
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(out.TenantID)

	// Output:
	// acme
}
