package main

import (
	"context"
	"fmt"
	"time"

	einochatopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
)

// runQA is the original single-shot mode: one Generate call against agnes,
// optionally with tools bound (single-shot capture, not executed — see
// runTools for real multi-step tool execution).
func runQA(ctx context.Context, env *arenaEnv, in adapterInput, out *adapterOutput) error {
	wireMsgs, err := buildMessages(in)
	if err != nil {
		return fmt.Errorf("building messages: %w", err)
	}
	messages, err := toSchemaMessages(wireMsgs)
	if err != nil {
		return fmt.Errorf("converting messages: %w", err)
	}

	tools, err := buildTools(in.Tools)
	if err != nil {
		return fmt.Errorf("building tools: %w", err)
	}

	chatModel, err := newChatModel(ctx, env, &einochatopenai.ChatModelConfig{})
	if err != nil {
		return err
	}

	var runner model.BaseChatModel = chatModel
	if len(tools) > 0 {
		bound, err := chatModel.WithTools(tools)
		if err != nil {
			return fmt.Errorf("binding tools: %w", err)
		}
		runner = bound
	}

	start := time.Now()
	reply, err := runner.Generate(ctx, messages)
	out.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		return fmt.Errorf("Eino Generate call: %w", err)
	}

	out.Reply = reply.Content
	out.InputTokens, out.OutputTokens = usageOf(reply)
	out.ToolCalls = toolCallsToOutput(reply.ToolCalls)
	return nil
}
