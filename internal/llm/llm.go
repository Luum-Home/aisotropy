// Package llm defines Aisotropy's LLM client abstraction (Phase 1).
//
// The Client interface is intentionally narrow: a single streaming method.
// Every chat endpoint consumes incremental tokens — never a full response
// buffered in memory — to minimize TTFT (time-to-first-token) and to be able
// to cut token spend as soon as ctx is cancelled (see
// docs/architecture/request-lifecycle.md, step 6).
//
// There are two implementations:
//   - AnthropicClient (anthropic.go): real streaming via the official SDK
//     github.com/anthropics/anthropic-sdk-go against the Messages API.
//   - StubClient (stub.go): emits a canned word-by-word stream, no
//     credentials needed, to verify the SSE path end-to-end in dev/CI.
//
// FromEnv picks the implementation based on whether ANTHROPIC_API_KEY is set.
package llm

import (
	"context"
	"os"
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
}

// Client is the interface any LLM provider must implement.
//
// StreamChat must:
//   - return a channel that closes when the stream ends (success, error sent
//     as the last Token with Done=true, or ctx cancellation);
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

// FromEnv builds the Client to use based on the environment, by precedence:
//
//  1. OPENROUTER_API_KEY  -> OpenRouter (OpenAI-compatible). Preferred path:
//     a single integration covers OpenRouter, OpenAI, and —via model ids
//     like "google/gemini-..."— Google/Gemini too. Model: OPENROUTER_MODEL /
//     AISOTROPY_MODEL / DefaultOpenRouterModel.
//  2. OPENAI_API_KEY (+ optional OPENAI_BASE_URL) -> direct OpenAI-compatible.
//  3. ANTHROPIC_API_KEY -> AnthropicClient (official SDK).
//  4. no credentials -> StubClient (allows exercising the SSE path
//     end-to-end in dev/CI without a key).
func FromEnv() Client {
	if key := os.Getenv("OPENROUTER_API_KEY"); key != "" {
		return NewOpenRouterClient(key, firstNonEmpty(
			os.Getenv("OPENROUTER_MODEL"), os.Getenv("AISOTROPY_MODEL")))
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		return NewOpenAICompatClient(key,
			firstNonEmpty(os.Getenv("OPENAI_MODEL"), os.Getenv("AISOTROPY_MODEL")),
			os.Getenv("OPENAI_BASE_URL"), "openai")
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		return NewAnthropicClient(key, firstNonEmpty(os.Getenv("ANTHROPIC_MODEL"), DefaultAnthropicModel))
	}
	return NewStubClient()
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
