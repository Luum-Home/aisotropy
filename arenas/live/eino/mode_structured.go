package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	einochatopenai "github.com/cloudwego/eino-ext/components/model/openai"
	jsonschema "github.com/eino-contrib/jsonschema"
)

// defaultStructuredSchema is used when the request does not supply its own
// "schema"/"schema_name". It is intentionally generic (works for any
// question) so the mode is exercisable without extra wiring.
const defaultStructuredSchemaName = "structured_answer"

var defaultStructuredSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "answer": {
      "type": "string",
      "description": "A direct, concise answer to the user's question."
    },
    "confidence": {
      "type": "string",
      "enum": ["high", "medium", "low"],
      "description": "How confident the model is in the answer."
    }
  },
  "required": ["answer", "confidence"],
  "additionalProperties": false
}`)

// runStructured binds an Eino native JSON-Schema response format
// (ChatModelConfig.ResponseFormat, forwarded to agnes as the OpenAI
// `response_format: {type: "json_schema", ...}` field) so the model's reply
// is a schema-validated JSON object rather than free text, then parses it
// into out.Structured.
//
// Because not every OpenAI-compatible provider enforces response_format
// strictly, the system prompt ALSO spells out the schema and instructs
// JSON-only output — belt and suspenders — and a parse failure degrades to
// out.Structured == nil (this mode is a SHOULD, not a MUST) rather than
// failing the whole call.
func runStructured(ctx context.Context, env *arenaEnv, in adapterInput, out *adapterOutput) error {
	schemaName := in.SchemaName
	if schemaName == "" {
		schemaName = defaultStructuredSchemaName
	}
	rawSchema := defaultStructuredSchema
	if len(in.Schema) > 0 {
		rawSchema = in.Schema
	}

	wireMsgs := make([]inputMessage, 0, len(in.Messages)+2)
	if in.System != "" {
		wireMsgs = append(wireMsgs, inputMessage{Role: "system", Content: in.System})
	}
	wireMsgs = append(wireMsgs, inputMessage{
		Role: "system",
		Content: fmt.Sprintf(
			"Respond with ONLY a single JSON object matching this JSON Schema (no prose, no markdown fences):\n%s",
			string(rawSchema),
		),
	})
	for i, m := range in.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			return fmt.Errorf("messages[%d]: unsupported role %q (want \"user\" or \"assistant\")", i, m.Role)
		}
		wireMsgs = append(wireMsgs, m)
	}

	messages, err := toSchemaMessages(wireMsgs)
	if err != nil {
		return fmt.Errorf("converting messages: %w", err)
	}

	schemaObj := &jsonschema.Schema{}
	if err := json.Unmarshal(rawSchema, schemaObj); err != nil {
		return fmt.Errorf("parsing structured output JSON Schema: %w", err)
	}

	chatModel, err := newChatModel(ctx, env, &einochatopenai.ChatModelConfig{
		ResponseFormat: &einochatopenai.ChatCompletionResponseFormat{
			Type: einochatopenai.ChatCompletionResponseFormatTypeJSONSchema,
			JSONSchema: &einochatopenai.ChatCompletionResponseFormatJSONSchema{
				Name:       schemaName,
				JSONSchema: schemaObj,
				Strict:     true,
			},
		},
	})
	if err != nil {
		return err
	}

	start := time.Now()
	reply, genErr := chatModel.Generate(ctx, messages)
	if genErr != nil {
		// Some OpenAI-compatible providers reject an unrecognized
		// response_format outright. Fall back to plain generation relying
		// solely on the prompt-level JSON instruction above, rather than
		// failing the whole call for a SHOULD-level feature.
		plainModel, plainErr := newChatModel(ctx, env, &einochatopenai.ChatModelConfig{})
		if plainErr != nil {
			return plainErr
		}
		reply, genErr = plainModel.Generate(ctx, messages)
	}
	out.LatencyMS = time.Since(start).Milliseconds()
	if genErr != nil {
		return fmt.Errorf("Eino Generate call (structured): %w", genErr)
	}

	out.Reply = reply.Content
	out.InputTokens, out.OutputTokens = usageOf(reply)

	var parsed any
	content := strings.TrimSpace(reply.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)
	if err := json.Unmarshal([]byte(content), &parsed); err == nil {
		out.Structured = parsed
	}
	return nil
}
