package main

import (
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/schema"
	jsonschema "github.com/eino-contrib/jsonschema"
)

// toSchemaMessages converts the adapter's wire-format messages into Eino's
// native []*schema.Message.
func toSchemaMessages(msgs []inputMessage) ([]*schema.Message, error) {
	out := make([]*schema.Message, 0, len(msgs))
	for i, m := range msgs {
		var role schema.RoleType
		switch m.Role {
		case "system":
			role = schema.System
		case "user":
			role = schema.User
		case "assistant":
			role = schema.Assistant
		default:
			return nil, fmt.Errorf("messages[%d]: unsupported role %q", i, m.Role)
		}
		out = append(out, &schema.Message{Role: role, Content: m.Content})
	}
	return out, nil
}

// buildTools converts the adapter's wire-format tool definitions (JSON-Schema
// parameters as a raw JSON object) into Eino's native *schema.ToolInfo, using
// schema.NewParamsOneOfByJSONSchema so any valid JSON Schema is accepted
// as-is without lossy re-modeling through Eino's simplified ParameterInfo
// form.
func buildTools(defs []inputTool) ([]*schema.ToolInfo, error) {
	if len(defs) == 0 {
		return nil, nil
	}
	tools := make([]*schema.ToolInfo, 0, len(defs))
	for i, d := range defs {
		info := &schema.ToolInfo{Name: d.Name, Desc: d.Description}
		if len(d.Parameters) > 0 && string(d.Parameters) != "null" {
			s := &jsonschema.Schema{}
			if err := json.Unmarshal(d.Parameters, s); err != nil {
				return nil, fmt.Errorf("tools[%d] (%s): parsing parameters JSON Schema: %w", i, d.Name, err)
			}
			info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(s)
		}
		tools = append(tools, info)
	}
	return tools, nil
}

// toolCallsToOutput converts the model's native tool_calls into the
// adapter's wire-format output, capturing raw (non-JSON) arguments instead
// of silently dropping them.
func toolCallsToOutput(calls []schema.ToolCall) []outputToolCall {
	out := make([]outputToolCall, 0, len(calls))
	for _, tc := range calls {
		args := map[string]any{}
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				args = map[string]any{"raw": tc.Function.Arguments}
			}
		}
		out = append(out, outputToolCall{Name: tc.Function.Name, Arguments: args})
	}
	return out
}

func usageOf(msg *schema.Message) (inputTokens, outputTokens int) {
	if msg == nil || msg.ResponseMeta == nil || msg.ResponseMeta.Usage == nil {
		return -1, -1
	}
	return msg.ResponseMeta.Usage.PromptTokens, msg.ResponseMeta.Usage.CompletionTokens
}
