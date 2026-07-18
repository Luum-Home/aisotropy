package llm

import (
	"context"
	"encoding/json"
	"fmt"

	anthropic "github.com/anthropics/anthropic-sdk-go"
)

// AnthropicClient implements ToolCaller (native tool-calling, ADR-0012 /
// ADR-0063) over the official anthropic-sdk-go's native Messages `tool_use`
// API — no dependency swap (ADR-0009's official-SDK choice preserved, and
// ADR-0010's "no Gollem-native Model owning the HTTP call" guardrail not
// crossed). It does NOT implement ReasoningToolCaller: signed-thinking-block
// carry-forward across native tool steps is a deferred follow-up (ADR-0063), so
// reasoning simply is not carried between Anthropic native tool steps (graceful
// degradation, identical to any ToolCaller that is not a ReasoningToolCaller).
var _ ToolCaller = (*AnthropicClient)(nil)

// CallTools implements ToolCaller (ADR-0012) over Anthropic's native Messages
// `tool_use` API: it makes a single NON-streaming request (Messages.New)
// advertising the offered tools as native ToolParam tools and returns the tool
// calls the model chose. A tool-call reply is a small structured object, not a
// token stream, so — exactly like OpenAICompatClient.CallTools — this buffers
// the one response instead of streaming. It honors ctx cancellation via the
// request context (the SDK threads ctx into the HTTP call).
//
// The tools passed in MUST already be scope-filtered by the caller, and the
// caller MUST re-validate each returned ToolCall.Name against the offered set
// before executing it — this method only relays what the provider returned (see
// the ToolCaller security note; RunToolAgent / chat_agent.go's native loop
// enforce the re-check, and only offered tools are ever advertised here, so a
// tool the model was never offered has no path to execution: governance by
// absence).
func (a *AnthropicClient) CallTools(ctx context.Context, system, prompt string, toolSpecs []ToolSpec) (ToolCallResult, error) {
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
	if len(toolSpecs) > 0 {
		params.Tools = anthropicToolParams(toolSpecs)
	}

	msg, err := a.client.Messages.New(ctx, params)
	if err != nil {
		return ToolCallResult{}, fmt.Errorf("anthropic: tool call: %w", err)
	}

	var res ToolCallResult
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			res.Message += block.Text
		case "tool_use":
			// block.Input is the model's exact argument JSON (json.RawMessage).
			// Args: flattened string view (compat / map executor). RawArgs: the
			// provider's exact JSON, preserved so a typed tool can decode nested /
			// typed fields the flattening drops (ADR-0016 lever #1) — identical
			// contract to the OpenAI path.
			raw := string(block.Input)
			res.Calls = append(res.Calls, ToolCall{
				Name:    block.Name,
				Args:    parseToolArgs(raw),
				RawArgs: rawToolArgs(raw),
			})
		}
	}
	return res, nil
}

// anthropicToolParams translates the provider-agnostic ToolSpecs into the SDK's
// native tool envelope ([]anthropic.ToolUnionParam). Only Name / Description /
// InputSchema cross the seam — no Anthropic tool-envelope type leaks into the
// llm.ToolCaller contract (ADR-0012 / ADR-0009 boundary held).
func anthropicToolParams(specs []ToolSpec) []anthropic.ToolUnionParam {
	tools := make([]anthropic.ToolUnionParam, 0, len(specs))
	for _, t := range specs {
		tp := anthropic.ToolParam{
			Name:        t.Name,
			InputSchema: anthropicToolInputSchema(t.Parameters),
		}
		if t.Description != "" {
			tp.Description = anthropic.String(t.Description)
		}
		tools = append(tools, anthropic.ToolUnionParam{OfTool: &tp})
	}
	return tools
}

// anthropicToolInputSchema turns a ToolSpec's JSON-Schema bytes into the SDK's
// ToolInputSchemaParam. When the spec declares a Parameters schema (ADR-0016
// lever #1: struct-as-contract for tool inputs) its `properties`/`required` are
// advertised verbatim so the model knows the exact argument shape. When it does
// not (untyped tool), it falls back to the permissive empty-object schema the
// ADR-0012 minimal seam used — mirroring OpenAICompatClient.buildToolCallPayload.
func anthropicToolInputSchema(params json.RawMessage) anthropic.ToolInputSchemaParam {
	schema := anthropic.ToolInputSchemaParam{Properties: map[string]any{}}
	if len(params) > 0 {
		var parsed struct {
			Properties any      `json:"properties"`
			Required   []string `json:"required"`
		}
		if err := json.Unmarshal(params, &parsed); err == nil {
			if parsed.Properties != nil {
				schema.Properties = parsed.Properties
			}
			schema.Required = parsed.Required
		}
	}
	return schema
}
