package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	einochatopenai "github.com/cloudwego/eino-ext/components/model/openai"
)

// runStream drives Eino's native streaming call (BaseChatModel.Stream,
// distinct from Generate) against agnes, recording time-to-first-chunk and
// concatenating the streamed deltas into the final reply. Token usage is
// read from whichever chunk carries it — the acl openai client already
// requests `stream_options.include_usage=true` on every streaming call, so
// a final usage-only chunk is expected.
func runStream(ctx context.Context, env *arenaEnv, in adapterInput, out *adapterOutput) error {
	wireMsgs, err := buildMessages(in)
	if err != nil {
		return fmt.Errorf("building messages: %w", err)
	}
	messages, err := toSchemaMessages(wireMsgs)
	if err != nil {
		return fmt.Errorf("converting messages: %w", err)
	}

	chatModel, err := newChatModel(ctx, env, &einochatopenai.ChatModelConfig{})
	if err != nil {
		return err
	}

	start := time.Now()
	stream, err := chatModel.Stream(ctx, messages)
	if err != nil {
		return fmt.Errorf("Eino Stream call: %w", err)
	}
	defer stream.Close()

	var (
		content          strings.Builder
		gotFirstChunk    bool
		promptTokens     = -1
		completionTokens = -1
	)
	for {
		chunk, recvErr := stream.Recv()
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) {
				break
			}
			out.LatencyMS = time.Since(start).Milliseconds()
			return fmt.Errorf("receiving stream chunk: %w", recvErr)
		}
		if !gotFirstChunk {
			gotFirstChunk = true
			out.TTFTMS = time.Since(start).Milliseconds()
		}
		content.WriteString(chunk.Content)
		if chunk.ResponseMeta != nil && chunk.ResponseMeta.Usage != nil {
			promptTokens = chunk.ResponseMeta.Usage.PromptTokens
			completionTokens = chunk.ResponseMeta.Usage.CompletionTokens
		}
	}
	out.LatencyMS = time.Since(start).Milliseconds()

	if !gotFirstChunk {
		return errors.New("Eino Stream call: no chunks received")
	}

	out.Reply = content.String()
	out.InputTokens = promptTokens
	out.OutputTokens = completionTokens
	return nil
}
