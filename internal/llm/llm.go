// Package llm defines Aisotropy's LLM client abstraction (Phase 1+).
//
// The Client interface is intentionally narrow: a single streaming method.
// Every chat endpoint consumes incremental tokens — never a full response
// buffered in memory — to minimize TTFT (time-to-first-token) and to be able
// to cut token spend as soon as ctx is cancelled (see
// docs/architecture/request-lifecycle.md, step 6).
//
// The package is provider-agnostic and supports multiple simultaneous
// provider connections (see ADR-0009). Implementations of Client:
//   - AnthropicClient (anthropic.go): real streaming via the official SDK
//     github.com/anthropics/anthropic-sdk-go against the Messages API.
//   - OpenAICompatClient (openai_compat.go): any provider that speaks the
//     OpenAI /chat/completions protocol with SSE streaming — covers
//     OpenRouter, OpenAI, and OpenAI-compatible gateways in general.
//   - StubClient (stub.go): emits a canned word-by-word stream, no
//     credentials needed, to verify the SSE path end-to-end in dev/CI.
//
// Registry (registry.go) holds several named Client connections at once.
// FromEnv keeps returning a single Client for backward compatibility, built
// from RegistryFromEnv().Default().
package llm

import (
	"context"
)

// DefaultAnthropicModel is the default model: cheap, suitable for the Phase 1
// walking skeleton.
const DefaultAnthropicModel = "claude-haiku-4-5-20251001"

// Token is an incremental fragment of the model's response.
type Token struct {
	// Text is the fragment's text (delta, not accumulated).
	Text string
	// Done indicates the stream finished normally (no more tokens).
	Done bool
	// Err, when non-nil, signals a mid-stream failure (e.g. a read error on the
	// provider's SSE body). It is delivered as the final Token before the
	// channel closes; consumers must surface it and stop, never treat a failed
	// stream as a clean finish.
	Err error
}

// Client is the interface any LLM provider must implement.
//
// StreamChat must:
//   - return a channel that closes when the stream ends (success signalled by
//     a Token with Done=true, a mid-stream failure by a Token with non-nil Err,
//     or ctx cancellation);
//   - respect ctx.Done() and stop emitting/consuming tokens as soon as it is
//     cancelled — this is the only spend-cutoff mechanism (see
//     docs/vision/principles.md -> isolation via ctx).
type Client interface {
	// StreamChat opens a response stream for the given prompt, with the
	// given system prompt. The returned channel closes automatically when
	// the stream ends (normally, on error, or on ctx cancellation).
	StreamChat(ctx context.Context, system, prompt string) (<-chan Token, error)

	// Name identifies the implementation (for logs/observability).
	Name() string
}

// firstNonEmpty returns the first non-empty string (or "" if all are empty).
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
