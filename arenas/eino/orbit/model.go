package orbit

import (
	"context"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// StubChatModel is an offline, deterministic implementation of Eino's REAL
// model.ToolCallingChatModel interface. It lets the whole Orbit stack — chains,
// streaming, and the ReAct agent — run through Eino's real runtime with NO LLM
// API key.
//
// It is a fake *model*, not a fake framework: every type below
// (model.Option, schema.Message, model.ToolCallingChatModel) is imported from
// github.com/cloudwego/eino. Eino orchestrates; this struct just answers.
type StubChatModel struct {
	boundTools []*schema.ToolInfo
}

// Compile-time proof we satisfy the real interface.
var _ model.ToolCallingChatModel = (*StubChatModel)(nil)

// NewStubChatModel returns a keyless ChatModel usable anywhere Eino expects a
// model.BaseChatModel / model.ToolCallingChatModel.
func NewStubChatModel() *StubChatModel { return &StubChatModel{} }

// WithTools is the ToolCallingChatModel half of the interface. Eino's ReAct
// agent calls this to bind the tool schemas before the run.
func (m *StubChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &StubChatModel{boundTools: tools}, nil
}

// Generate is the real BaseChatModel entrypoint Eino invokes inside a chain or
// agent step. Behaviour:
//   - If a tool named "lookup_order" is bound, the newest turn is a user asking
//     about an order, and no tool result is present yet -> emit ONE tool call
//     (drives the ReAct loop through Eino's real tool node).
//   - Otherwise -> emit a final assistant answer. If a tool result is present it
//     is folded in; any support notes carried in the system prompt are echoed
//     (so working-memory content is observable in the reply).
func (m *StubChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	// Honour (but do not require) common options so real Eino option plumbing
	// is exercised.
	_ = model.GetCommonOptions(&model.Options{}, opts...)

	lastUser := lastUserContent(input)
	hasToolResult := containsRole(input, schema.Tool)
	wantsOrder := strings.Contains(strings.ToLower(lastUser), "order")

	if m.hasTool("lookup_order") && wantsOrder && !hasToolResult {
		return &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_lookup_order_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "lookup_order",
					Arguments: `{"order_id":"A-1001"}`,
				},
			}},
		}, nil
	}

	return schema.AssistantMessage(m.answer(input, lastUser, hasToolResult), nil), nil
}

// Stream is the streaming half of BaseChatModel. It chunks the same answer into
// a real *schema.StreamReader[*schema.Message], which is exactly what Eino
// returns from a compiled runnable's Stream(). Cheap, offline, no key.
func (m *StubChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	full := m.answer(input, lastUserContent(input), containsRole(input, schema.Tool))
	chunks := make([]*schema.Message, 0, 8)
	for _, w := range strings.Fields(full) {
		chunks = append(chunks, &schema.Message{Role: schema.Assistant, Content: w + " "})
	}
	if len(chunks) == 0 {
		chunks = append(chunks, &schema.Message{Role: schema.Assistant, Content: ""})
	}
	return schema.StreamReaderFromArray(chunks), nil
}

func (m *StubChatModel) answer(input []*schema.Message, lastUser string, hasToolResult bool) string {
	var b strings.Builder
	b.WriteString("[Orbit] ")

	// Fold in a tool result if the ReAct loop produced one.
	if hasToolResult {
		for _, msg := range input {
			if msg.Role == schema.Tool {
				b.WriteString("Order status: " + strings.TrimSpace(msg.Content) + ". ")
			}
		}
	}

	// Echo any working-memory notes injected by the app into the system prompt.
	// This is what makes a wrong-key working-memory read VISIBLE in the output.
	if notes := systemNotes(input); notes != "" {
		b.WriteString("Based on your history (" + notes + "), ")
	}

	if lastUser == "" {
		b.WriteString("how can I help?")
	} else {
		b.WriteString("you asked: " + strings.TrimSpace(lastUser))
	}
	return b.String()
}

func (m *StubChatModel) hasTool(name string) bool {
	for _, t := range m.boundTools {
		if t != nil && t.Name == name {
			return true
		}
	}
	return false
}

func lastUserContent(msgs []*schema.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == schema.User {
			return msgs[i].Content
		}
	}
	return ""
}

func containsRole(msgs []*schema.Message, role schema.RoleType) bool {
	for _, m := range msgs {
		if m.Role == role {
			return true
		}
	}
	return false
}

// systemNotes extracts the "notes:" payload the app appended to the system
// prompt (Orbit's developer-managed working memory).
func systemNotes(msgs []*schema.Message) string {
	const marker = "notes:"
	for _, m := range msgs {
		if m.Role != schema.System {
			continue
		}
		if idx := strings.Index(m.Content, marker); idx >= 0 {
			return strings.TrimSpace(m.Content[idx+len(marker):])
		}
	}
	return ""
}
