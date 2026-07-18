package llm

import (
	"context"
	"log/slog"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// AnthropicClient implements Client against Anthropic's Messages API using
// the official SDK (github.com/anthropics/anthropic-sdk-go), with the SDK's
// native SSE streaming (MessageService.NewStreaming).
//
// It was NOT possible to verify this path end-to-end in this environment: no
// ANTHROPIC_API_KEY is available. The build was verified (go build/vet), but
// the actual call to the Anthropic API remains unexercised — see the
// verification report.
type AnthropicClient struct {
	client    anthropic.Client
	model     anthropic.Model
	maxTokens int64

	// reasoning gates extended-thinking (ADR-0061): when false (the default) the
	// request carries no Thinking param and the demux discards any thinking
	// deltas, so the client behaves byte-for-byte as before. Set via
	// EnableReasoning at composition when AISOTROPY_REASONING is on.
	reasoning       bool
	reasoningEffort string
}

// anthropicThinkingBudget maps a reasoning-effort level to Anthropic's
// thinking `budget_tokens`. "off" (or reasoning disabled) yields 0 = no
// thinking. The budget must be < MaxTokens; enableReasoning bumps maxTokens so
// the answer still has room after the thinking budget.
func anthropicThinkingBudget(effort string) int64 {
	switch effort {
	case ReasoningEffortHigh:
		return 8192
	case ReasoningEffortMedium:
		return 4096
	case ReasoningEffortLow:
		return 1024
	default: // off
		return 0
	}
}

// EnableReasoning turns on extended thinking at the given effort ("high" |
// "medium" | "low" | "off"). It also raises maxTokens above the thinking budget
// when needed (Anthropic requires MaxTokens > budget_tokens). "off" is a no-op
// that leaves reasoning disabled. Called at composition (registry.go) only when
// AISOTROPY_REASONING is set — reasoning stays OFF by default.
func (a *AnthropicClient) EnableReasoning(effort string) {
	effort = normalizeReasoningEffort(effort)
	if effort == ReasoningEffortOff {
		a.reasoning = false
		return
	}
	a.reasoning = true
	a.reasoningEffort = effort
	if budget := anthropicThinkingBudget(effort); a.maxTokens <= budget {
		a.maxTokens = budget + 1024 // leave room for the answer after thinking
	}
}

// NewAnthropicClient creates an AnthropicClient with the given API key and
// model. If model is "", uses DefaultAnthropicModel.
func NewAnthropicClient(apiKey, model string) *AnthropicClient {
	if model == "" {
		model = DefaultAnthropicModel
	}
	return &AnthropicClient{
		client:    anthropic.NewClient(option.WithAPIKey(apiKey)),
		model:     model,
		maxTokens: 1024,
	}
}

func (a *AnthropicClient) Name() string { return "anthropic:" + a.model }

// StreamChat opens a real stream via MessageService.NewStreaming and
// translates each text-type "content_block_delta" event into a Token. Also
// captures real token usage: input_tokens from the "message_start" event's
// embedded Message.Usage, and output_tokens from each "message_delta"
// event's Usage (cumulative — the last one received before the stream ends
// is the final count), attached to the terminal Token. Respects ctx.Done():
// upon cancellation, the SDK cuts the underlying HTTP stream (ssestream.Stream
// receives the same ctx) and the producer goroutine immediately stops
// consuming/emitting tokens.
func (a *AnthropicClient) StreamChat(ctx context.Context, system, prompt string) (<-chan Token, error) {
	return a.streamChat(ctx, system, prompt, ReasoningState{})
}

// streamChat is the shared body of StreamChat and (via a future native
// tool-calling round-trip) the reasoning carry-forward path: it re-attaches any
// prior signed thinking blocks natively (buildAnthropicThinkingBlocks) so the
// signature survives, sets the Thinking request param when reasoning is on, and
// demuxes thinking deltas onto Token.Reasoning. prior is empty on the ordinary
// StreamChat path, so that path is unchanged.
func (a *AnthropicClient) streamChat(ctx context.Context, system, prompt string, prior ReasoningState) (<-chan Token, error) {
	// Re-attach prior signed thinking blocks as native content blocks BEFORE the
	// user text, so Anthropic accepts continued reasoning (flattening to text
	// would lose the signature — ADR-0061 Piece 2). Empty prior => just the user
	// message, byte-for-byte as before.
	blocks := buildAnthropicThinkingBlocks(prior)
	blocks = append(blocks, anthropic.NewTextBlock(prompt))

	params := anthropic.MessageNewParams{
		Model:     a.model,
		MaxTokens: a.maxTokens,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(blocks...),
		},
	}
	if system != "" {
		params.System = []anthropic.TextBlockParam{{Text: system}}
	}
	if a.reasoning {
		if budget := anthropicThinkingBudget(a.reasoningEffort); budget > 0 {
			params.Thinking = anthropic.ThinkingConfigParamOfEnabled(budget)
		}
	}

	stream := a.client.Messages.NewStreaming(ctx, params)
	out := make(chan Token)
	reasoningEnabled := a.reasoning

	go func() {
		defer close(out)
		defer func() { _ = stream.Close() }()

		var usage Usage
		sawUsage := false

		for stream.Next() {
			if ctx.Err() != nil {
				return
			}

			tok, emit := anthropicEventToken(stream.Current(), &usage, &sawUsage, reasoningEnabled)
			if !emit {
				continue
			}

			select {
			case <-ctx.Done():
				return
			case out <- tok:
			}
		}

		finishAnthropicStream(ctx, stream.Err(), out, usage, sawUsage)
	}()

	return out, nil
}

// buildAnthropicThinkingBlocks re-materializes a prior ReasoningState's carried
// blocks as native Anthropic content blocks (ADR-0061 Piece 2): a signed
// thinking block round-trips verbatim via NewThinkingBlock (signature preserved
// so Anthropic accepts continued reasoning); a redacted block round-trips via
// NewRedactedThinkingBlock, never decoded. Text-kind blocks (from stateless
// providers) are skipped here — they have no Anthropic-native form and are not
// what Anthropic requires. An empty state yields nil (no blocks prepended).
func buildAnthropicThinkingBlocks(prior ReasoningState) []anthropic.ContentBlockParamUnion {
	if prior.IsEmpty() {
		return nil
	}
	var blocks []anthropic.ContentBlockParamUnion
	for _, b := range prior.Blocks {
		switch b.kind {
		case reasoningKindThinking:
			blocks = append(blocks, anthropic.NewThinkingBlock(b.signature, b.thinking))
		case reasoningKindRedacted:
			blocks = append(blocks, anthropic.NewRedactedThinkingBlock(b.redacted))
		}
	}
	return blocks
}

// anthropicEventToken processes one Anthropic stream event: it folds any
// token-usage the event carries ("message_start" / "message_delta") into
// usage/sawUsage, returns a Token{Text} for a text delta, and — when reasoning
// is enabled — a Token{Reasoning} for a thinking delta. emit is false for every
// event that carries nothing to emit (usage-only events, non-text/non-thinking
// deltas, thinking deltas while reasoning is off, and any other event type).
// The signed thinking/redacted blocks needed for carry-forward are captured by
// the caller's accumulator, not here (this function is stateless per event).
func anthropicEventToken(event anthropic.MessageStreamEventUnion, usage *Usage, sawUsage *bool, reasoningEnabled bool) (tok Token, emit bool) {
	switch event.Type {
	case "message_start":
		start := event.AsMessageStart()
		usage.InputTokens = int(start.Message.Usage.InputTokens)
		usage.OutputTokens = int(start.Message.Usage.OutputTokens)
		*sawUsage = true
		return Token{}, false
	case "message_delta":
		delta := event.AsMessageDelta()
		usage.OutputTokens = int(delta.Usage.OutputTokens)
		*sawUsage = true
		return Token{}, false
	case "content_block_delta":
		delta := event.AsContentBlockDelta().Delta
		switch {
		case delta.Type == "text_delta" && delta.Text != "":
			return Token{Text: delta.Text}, true
		case reasoningEnabled && delta.Type == "thinking_delta" && delta.Thinking != "":
			// Reasoning delta: a SEPARATE channel from Text. It is streamed for
			// display but NEVER accumulated into the answer (ADR-0061 Piece 1a).
			return Token{Reasoning: delta.Thinking}, true
		default:
			return Token{}, false
		}
	default:
		return Token{}, false
	}
}

// finishAnthropicStream emits the terminal Token(s) once the stream's event
// loop has exited: a genuine mid-stream error (not just ctx cancellation)
// becomes an error Token instead of being masked as a clean Done; otherwise
// it emits Done, attaching the accumulated Usage when the stream reported any
// (message_start / message_delta events were seen).
func finishAnthropicStream(ctx context.Context, streamErr error, out chan<- Token, usage Usage, sawUsage bool) {
	if streamErr != nil && ctx.Err() == nil {
		// Propagate the mid-stream error instead of masking it as a clean Done.
		slog.ErrorContext(ctx, "anthropic stream error", "err", streamErr)
		select {
		case <-ctx.Done():
		case out <- Token{Err: streamErr}:
		}
		return
	}

	done := Token{Done: true}
	if sawUsage {
		u := usage
		done.Usage = &u
	}
	select {
	case <-ctx.Done():
	case out <- done:
	}
}
