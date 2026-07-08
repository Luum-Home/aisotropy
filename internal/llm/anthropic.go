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
		model:     anthropic.Model(model),
		maxTokens: 1024,
	}
}

func (a *AnthropicClient) Name() string { return "anthropic:" + string(a.model) }

// StreamChat opens a real stream via MessageService.NewStreaming and
// translates each text-type "content_block_delta" event into a Token.
// Respects ctx.Done(): upon cancellation, the SDK cuts the underlying HTTP
// stream (ssestream.Stream receives the same ctx) and the producer goroutine
// immediately stops consuming/emitting tokens.
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

		for stream.Next() {
			if ctx.Err() != nil {
				return
			}

			event := stream.Current()
			if event.Type != "content_block_delta" {
				continue
			}
			delta := event.AsContentBlockDelta().Delta
			if delta.Type != "text_delta" || delta.Text == "" {
				continue
			}

			select {
			case <-ctx.Done():
				return
			case out <- Token{Text: delta.Text}:
			}
		}

		if err := stream.Err(); err != nil && ctx.Err() == nil {
			// Propagate the mid-stream error instead of masking it as a clean Done.
			slog.ErrorContext(ctx, "anthropic stream error", "err", err)
			select {
			case <-ctx.Done():
			case out <- Token{Err: err}:
			}
			return
		}

		select {
		case <-ctx.Done():
		case out <- Token{Done: true}:
		}
	}()

	return out, nil
}
