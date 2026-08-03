package main

import (
	"context"
	"strings"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ---- ARENA_STUB=1 fake ChatModel ----
//
// stubChatModel is a local, network-free fake implementing Eino's NATIVE
// model.ToolCallingChatModel interface (Generate + Stream + WithTools) — the
// exact interface newChatModel() (clients.go) normally returns wrapping the
// real eino-ext/components/model/openai component. Setting ARENA_STUB=1
// swaps this in at that single choke point, so EVERY mode that calls
// newChatModel() runs with zero network calls to agnes: Generate/Stream
// deterministically ECHO the conversation back (prefixed so a stub reply is
// unmistakably synthetic, never confusable with a real agnes answer), with a
// synthesized schema.ResponseMeta.Usage so input_tokens/output_tokens are
// still populated on the wire (a word-count approximation, loudly fake —
// never a fabricated *real* provider number, and never silently -1 either).
//
// Tool-calling scope note: when tools are bound (WithTools) and the input
// does not yet carry a tool-result message, Generate emits a CANNED
// ToolCall for "calculate" (or the first bound tool, if not named
// "calculate") with a fixed, genuinely-evaluable expression — mirroring the
// Mastra adapter's stub (lib/stub-model.mjs), which takes the same approach
// for the same reason: mode_tools.go's `len(out.ToolCalls) == 0` check and
// mode_observability.go's scenario both depend on Eino's REAL ReAct loop
// (flow/agent/react) actually executing a tool, not on this fake "deciding"
// anything intelligently. Once the tool result comes back (a Tool-role
// message present in the next Generate call), the stub finishes with a
// plain text reply that echoes that real result — the tool itself (and its
// execution) is always genuine, only the DECISION to call it is canned.
// This keeps the run fully offline (zero network calls) while letting both
// modes exercise their real multi-step tool-execution code path under
// ARENA_STUB=1, the same way the other six adapters' stubs already do. See
// README.md#stub-mode.
//
// Callback wiring: this type manually calls the same
// github.com/cloudwego/eino/callbacks sequence
// (EnsureRunInfo -> OnStart -> OnEnd) the real vendored client calls
// internally (github.com/cloudwego/eino-ext/libs/acl/openai's
// Client.Generate) so that anything relying on Eino's native callback seam
// for a STANDALONE (non-graph) call — this adapter's own "middleware" mode
// (mode_middleware.go) in particular — observes the exact same
// OnStart/OnEnd firing whether the real model or this stub is behind
// newChatModel(). Graph-driven modes (tools, observability) do not need
// this: Eino's compose graph wraps every node's invocation with callbacks
// generically at the graph layer (compose/utils.go's
// invokeWithCallbacks/initGraphCallbacks — verified by reading that source,
// not assumed), independent of the node's own implementation, so those
// already fire correctly for ANY model.BaseChatModel dropped into a node,
// stub included. Stream() intentionally does NOT replicate the callback
// wiring — no existing or new mode reads chat-model callbacks off the
// streaming path (stream mode reads TTFT/usage directly off the returned
// StreamReader, not via callbacks), so that plumbing would be complexity
// with no caller.
type stubChatModel struct {
	tools []*schema.ToolInfo
}

// newStubChatModel constructs the ARENA_STUB=1 fake. Returned as the
// model.ToolCallingChatModel interface so it is a drop-in for whatever
// newChatModel() would otherwise have returned.
func newStubChatModel() model.ToolCallingChatModel {
	return &stubChatModel{}
}

// stubModelType is this fake's callbacks.RunInfo.Type / GetType() identity —
// the stub-mode analogue of einochatopenai.ChatModel's GetType() returning
// "OpenAI".
const stubModelType = "ArenaStub"

// stubEchoPrefix marks every stub reply as unmistakably synthetic — never
// confusable with a real agnes-2.0-flash answer in captured output.
const stubEchoPrefix = "[ARENA_STUB echo] "

func (m *stubChatModel) GetType() string { return stubModelType }

func (m *stubChatModel) IsCallbacksEnabled() bool { return true }

// reply deterministically echoes the last user message (falling back to the
// full transcript if no user-role message is present — e.g. a probe with
// only a system prompt), and synthesizes a token-usage estimate (word count,
// not a real provider count) so input_tokens/output_tokens are populated
// rather than left at -1.
func (m *stubChatModel) reply(in []*schema.Message) *schema.Message {
	// Echo the WHOLE conversation, not just the last user turn.
	//
	// On the integration lane (mock model, REAL stores) the plumbing dimensions
	// — memory, memory_scoped, isolation_leak, crash_recovery — are scored from
	// this reply: "was the fact re-hydrated?" is answerable only if a fact the
	// STORE returned appears in the text. That fact is never in the last user
	// message, which is the QUESTION; it is in the history Eino just rebuilt
	// from SQLite.
	//
	// Echoing only the question makes every plumbing cell fail identically
	// whether the store worked or not — worse than not measuring, because it
	// renders as a result. Verified on ai-sdk, where the same change moved
	// memory from 0/8 to 8/8 with zero billable calls.
	//
	// The NEGATIVE direction stays honest too: isolation_leak passes only when
	// another tenant's fact is ABSENT, so a leak into the prompt now surfaces in
	// the reply and fails the cell, which is the behaviour that dimension wants.
	var parts []string
	for _, msg := range in {
		if msg != nil && msg.Content != "" {
			parts = append(parts, msg.Content)
		}
	}
	whole := strings.Join(parts, " ")
	if whole == "" {
		for i := len(in) - 1; i >= 0; i-- {
			if in[i].Role == schema.User {
				whole = in[i].Content
				break
			}
		}
	}

	content := stubEchoPrefix + whole

	promptTokens := 0
	for _, msg := range in {
		promptTokens += len(strings.Fields(msg.Content))
	}
	completionTokens := len(strings.Fields(content))

	return &schema.Message{
		Role:    schema.Assistant,
		Content: content,
		ResponseMeta: &schema.ResponseMeta{
			FinishReason: "stop",
			Usage: &schema.TokenUsage{
				PromptTokens:     promptTokens,
				CompletionTokens: completionTokens,
				TotalTokens:      promptTokens + completionTokens,
			},
		},
	}
}

// stubToolCallID is fixed (not random) — deterministic output, same spirit
// as every other canned value this stub produces, and there is only ever
// one in-flight tool call per stub-driven ReAct step.
const stubToolCallID = "arena-stub-call-1"

// hasToolResultMessage returns the most recent Tool-role message in the
// conversation, or nil if none is present yet — i.e. nil means "this
// Generate call is deciding whether to call a tool", non-nil means "a bound
// tool already ran; time to answer using its real result."
func hasToolResultMessage(in []*schema.Message) *schema.Message {
	for i := len(in) - 1; i >= 0; i-- {
		if in[i].Role == schema.Tool {
			return in[i]
		}
	}
	return nil
}

// stubToolCallArgs returns canned, genuinely-evaluable arguments for the
// named tool. "calculate" (this adapter's one real tool, mode_tools.go) is
// special-cased with a real arithmetic expression so the tool actually
// computes something, not just echoes a placeholder — same convention the
// Mastra and genkit-go stubs use for the identical tool shape.
func stubToolCallArgs(name string) string {
	if name == "calculate" {
		return `{"expression":"2 + 2"}`
	}
	return "{}"
}

// toolCallMessage builds a canned ToolCall targeting "calculate" (or the
// first bound tool, if "calculate" is not among them) — see the type-level
// "Tool-calling scope note" above for why this fake decides deterministically
// rather than trying to reason about when to call a tool.
func (m *stubChatModel) toolCallMessage() *schema.Message {
	target := m.tools[0]
	for _, t := range m.tools {
		if t.Name == "calculate" {
			target = t
			break
		}
	}
	args := stubToolCallArgs(target.Name)
	return &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID:   stubToolCallID,
			Type: "function",
			Function: schema.FunctionCall{
				Name:      target.Name,
				Arguments: args,
			},
		}},
		ResponseMeta: &schema.ResponseMeta{
			FinishReason: "tool_calls",
			Usage: &schema.TokenUsage{
				PromptTokens:     len(strings.Fields(args)),
				CompletionTokens: len(strings.Fields(args)),
				TotalTokens:      2 * len(strings.Fields(args)),
			},
		},
	}
}

// toolResultReply finishes the turn with a plain text reply that echoes the
// REAL tool's output (tr.Content is whatever the actually-executed
// "calculate" tool actually returned — see mode_tools.go/newCalculateTool —
// never fabricated here), prefixed like every other stub reply so it is
// unmistakably synthetic.
func (m *stubChatModel) toolResultReply(tr *schema.Message) *schema.Message {
	content := stubEchoPrefix + "tool result: " + tr.Content
	tokens := len(strings.Fields(content))
	return &schema.Message{
		Role:    schema.Assistant,
		Content: content,
		ResponseMeta: &schema.ResponseMeta{
			FinishReason: "stop",
			Usage: &schema.TokenUsage{
				PromptTokens:     tokens,
				CompletionTokens: tokens,
				TotalTokens:      2 * tokens,
			},
		},
	}
}

// Generate mirrors the real vendored client's own callback sequence
// (EnsureRunInfo -> OnStart -> OnEnd, github.com/cloudwego/eino-ext/libs/acl/openai's
// Client.Generate) so a standalone (non-graph) caller that attaches a
// callbacks.Handler via callbacks.InitCallbacks — this adapter's
// "middleware" mode — observes an outbound firing under ARENA_STUB=1 exactly
// as it would against the real model.
func (m *stubChatModel) Generate(ctx context.Context, in []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	ctx = callbacks.EnsureRunInfo(ctx, stubModelType, components.ComponentOfChatModel)
	ctx = callbacks.OnStart(ctx, &model.CallbackInput{Messages: in, Tools: m.tools})

	var out *schema.Message
	switch {
	case len(m.tools) > 0 && hasToolResultMessage(in) == nil:
		out = m.toolCallMessage()
	case len(m.tools) > 0:
		out = m.toolResultReply(hasToolResultMessage(in))
	default:
		out = m.reply(in)
	}

	callbacks.OnEnd(ctx, &model.CallbackOutput{
		Message: out,
		TokenUsage: &model.TokenUsage{
			PromptTokens:     out.ResponseMeta.Usage.PromptTokens,
			CompletionTokens: out.ResponseMeta.Usage.CompletionTokens,
			TotalTokens:      out.ResponseMeta.Usage.TotalTokens,
		},
	})
	return out, nil
}

// Stream synthesizes a real (in-process) multi-chunk schema.StreamReader —
// one chunk per word plus a final usage-only chunk — so stream mode's
// time-to-first-chunk and concatenation logic are genuinely exercised
// against more than one chunk, not just a single-shot passthrough dressed up
// as streaming.
func (m *stubChatModel) Stream(_ context.Context, in []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	full := m.reply(in)
	words := strings.Fields(full.Content)
	if len(words) == 0 {
		words = []string{""}
	}

	sr, sw := schema.Pipe[*schema.Message](len(words) + 1)
	go func() {
		defer sw.Close()
		for i, w := range words {
			text := w
			if i > 0 {
				text = " " + w
			}
			sw.Send(&schema.Message{Role: schema.Assistant, Content: text}, nil)
		}
		// Final usage-only chunk, matching the real client's own
		// stream_options.include_usage=true convention (see clients.go /
		// README.md) so mode_stream.go's usage-from-last-chunk logic works
		// unchanged under ARENA_STUB=1.
		sw.Send(&schema.Message{Role: schema.Assistant, ResponseMeta: full.ResponseMeta}, nil)
	}()
	return sr, nil
}

// WithTools returns a new stub carrying the bound tools — mirroring
// model.ToolCallingChatModel's immutable-derivation contract (never mutates
// the receiver) — but, per the honest scope note above, binding tools does
// NOT make this fake capable of deciding to call one.
func (m *stubChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &stubChatModel{tools: tools}, nil
}
