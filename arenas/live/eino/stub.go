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
// Honest scope note (this is a PURE echo, not a tool-calling simulator): it
// never decides to call a bound tool on its own. Modes whose OWN correctness
// assertion requires the model to actually invoke a tool — mode_tools.go's
// `len(out.ToolCalls) == 0` check, and mode_observability.go's scenario that
// depends on the same ReAct loop — will surface that honestly as an `error`
// field under ARENA_STUB=1 rather than fabricating a tool decision. That is
// still a fully offline, zero-network run (the contract's rule 5, "errors
// are data, not crashes," covers exactly this); it is just not a PASSing run
// for those two modes' own internal assertions. See README.md#stub-mode.
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
	last := ""
	for i := len(in) - 1; i >= 0; i-- {
		if in[i].Role == schema.User {
			last = in[i].Content
			break
		}
	}
	if last == "" && len(in) > 0 {
		last = in[len(in)-1].Content
	}

	content := stubEchoPrefix + last

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

// Generate mirrors the real vendored client's own callback sequence
// (EnsureRunInfo -> OnStart -> OnEnd, github.com/cloudwego/eino-ext/libs/acl/openai's
// Client.Generate) so a standalone (non-graph) caller that attaches a
// callbacks.Handler via callbacks.InitCallbacks — this adapter's
// "middleware" mode — observes an outbound firing under ARENA_STUB=1 exactly
// as it would against the real model.
func (m *stubChatModel) Generate(ctx context.Context, in []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	ctx = callbacks.EnsureRunInfo(ctx, stubModelType, components.ComponentOfChatModel)
	ctx = callbacks.OnStart(ctx, &model.CallbackInput{Messages: in, Tools: m.tools})

	out := m.reply(in)

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
