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

// Usage carries provider-reported token counts for a completed stream. It is
// real accounting data from the provider (or, for StubClient, a deterministic
// stand-in that exercises the same plumbing) — never a proxy metric like the
// number of streamed deltas. See otel.go's attrGenAIInputTokens/
// attrGenAIOutputTokens for where this lands in observability, and
// gollem.go's GollemModel for where it lands in core.ModelResponse.Usage.
type Usage struct {
	// InputTokens is the provider-reported prompt/input token count.
	InputTokens int
	// OutputTokens is the provider-reported completion/output token count.
	OutputTokens int
	// ReasoningTokens is the provider-reported reasoning ("thinking") token
	// count, reported on its own field for cost VISIBILITY (ADR-0061 Piece 1b).
	//
	// INVARIANT — it is a BREAKDOWN OF OutputTokens, never an addition to it.
	// OutputTokens always carries the provider's complete completion count and
	// already includes these tokens:
	//
	//   - OpenAI-compat reads completion_tokens_details.reasoning_tokens, a
	//     DETAIL of completion_tokens, while OutputTokens gets completion_tokens
	//     whole (see openai_compat.go).
	//   - Anthropic bills thinking INSIDE output_tokens and surfaces no separate
	//     streaming count, so this stays 0 there.
	//
	// A consumer that ADDS this to OutputTokens double-counts — which is exactly
	// what the cost path did until it was centralised in internal/api's
	// turnCostUSD. Price OutputTokens; report ReasoningTokens.
	//
	// It is 0 on every non-reasoning provider and with reasoning OFF (the
	// default), so a text-only consumer is byte-for-byte unaffected.
	ReasoningTokens int
}

// Token is an incremental fragment of the model's response.
type Token struct {
	// Text is the fragment's text (delta, not accumulated).
	Text string
	// Reasoning is the fragment's reasoning/"thinking" delta (delta, not
	// accumulated), emitted by reasoning models (ADR-0061 Piece 1a). It is a
	// SEPARATE channel from Text — never mixed in — so every existing consumer
	// that appends only tok.Text is unaffected (zero-edit backward compat) and
	// answer text never contains chain-of-thought. It is "" on non-reasoning
	// providers, with reasoning OFF (the default), and on every answer-text
	// token. A reasoning delta is a mid-stream token exactly like a text delta:
	// Done/Err/Usage still land only on the terminal token.
	Reasoning string
	// Done indicates the stream finished normally (no more tokens).
	Done bool
	// Err, when non-nil, signals a mid-stream failure (e.g. a read error on the
	// provider's SSE body). It is delivered as the final Token before the
	// channel closes; consumers must surface it and stop, never treat a failed
	// stream as a clean finish.
	Err error
	// Usage, when non-nil, carries the provider-reported total token counts
	// for the completed stream. Only ever set on the terminal Token (Done or,
	// where the provider supplies it before an error, Err) — never on a
	// mid-stream delta, since providers report usage as a stream-final
	// summary, not incrementally per token.
	Usage *Usage
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
