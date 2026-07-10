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
	params := anthropic.MessageNewParams{
		Model:     a.model,
		MaxTokens: a.maxTokens,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	}
	if system != "" {
		params.System = []anthropic.TextBlockParam{{Text: system}}
	}

	stream := a.client.Messages.NewStreaming(ctx, params)
	out := make(chan Token)

	go func() {
		defer close(out)
		defer func() { _ = stream.Close() }()

		var usage Usage
		sawUsage := false

		for stream.Next() {
			if ctx.Err() != nil {
				return
			}

			text, emit := anthropicEventText(stream.Current(), &usage, &sawUsage)
			if !emit {
				continue
			}

			select {
			case <-ctx.Done():
				return
			case out <- Token{Text: text}:
			}
		}

		finishAnthropicStream(ctx, stream.Err(), out, usage, sawUsage)
	}()

	return out, nil
}

// anthropicEventText processes one Anthropic stream event: it folds any
// token-usage the event carries ("message_start" / "message_delta") into
// usage/sawUsage, and — for a "content_block_delta" text event — returns the
// text to emit. emit is false for every event that carries no emittable text
// (usage-only events, non-text deltas, and any other event type).
func anthropicEventText(event anthropic.MessageStreamEventUnion, usage *Usage, sawUsage *bool) (text string, emit bool) {
	switch event.Type {
	case "message_start":
		start := event.AsMessageStart()
		usage.InputTokens = int(start.Message.Usage.InputTokens)
		usage.OutputTokens = int(start.Message.Usage.OutputTokens)
		*sawUsage = true
		return "", false
	case "message_delta":
		delta := event.AsMessageDelta()
		usage.OutputTokens = int(delta.Usage.OutputTokens)
		*sawUsage = true
		return "", false
	case "content_block_delta":
		delta := event.AsContentBlockDelta().Delta
		if delta.Type != "text_delta" || delta.Text == "" {
			return "", false
		}
		return delta.Text, true
	default:
		return "", false
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
