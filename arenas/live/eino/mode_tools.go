package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	einochatopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	toolcomp "github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
)

const toolsSystemPrompt = `You are a careful assistant. For any arithmetic in
the user's request, you MUST call the "calculate" tool to compute the exact
value instead of doing mental math yourself — never guess a numeric result.
After the tool returns, answer the user in one sentence that states the
computed result.`

type calcInput struct {
	Expression string `json:"expression" jsonschema:"required" jsonschema_description:"A plain arithmetic expression to evaluate, e.g. '(12 + 30) * 7 / 2'."`
}

type calcOutput struct {
	Result float64 `json:"result"`
}

// runTools drives Eino's real ReAct agent (flow/agent/react) with a real,
// actually-executed "calculate" tool. Unlike qa mode's single-shot tool_call
// capture, this is a genuine multi-step loop: the model emits a tool call,
// the ReAct graph executes the real evaluator, feeds the result back to the
// model, and the model produces a final answer that uses it.
func runTools(ctx context.Context, env *arenaEnv, in adapterInput, out *adapterOutput) error {
	wireMsgs, err := buildMessages(in)
	if err != nil {
		return fmt.Errorf("building messages: %w", err)
	}
	if in.System == "" {
		wireMsgs = append([]inputMessage{{Role: "system", Content: toolsSystemPrompt}}, wireMsgs...)
	}
	messages, err := toSchemaMessages(wireMsgs)
	if err != nil {
		return fmt.Errorf("converting messages: %w", err)
	}

	calcTool, err := toolutils.InferTool(
		"calculate",
		"Evaluates a plain arithmetic expression (+ - * / parentheses) and returns the exact numeric result.",
		func(ctx context.Context, in calcInput) (calcOutput, error) {
			v, err := evalArithmetic(in.Expression)
			if err != nil {
				return calcOutput{}, fmt.Errorf("evaluating %q: %w", in.Expression, err)
			}
			return calcOutput{Result: v}, nil
		},
	)
	if err != nil {
		return fmt.Errorf("building calculate tool: %w", err)
	}

	chatModel, err := newChatModel(ctx, env, &einochatopenai.ChatModelConfig{})
	if err != nil {
		return err
	}

	agentInst, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: chatModel,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: []toolcomp.BaseTool{calcTool},
		},
		MaxStep: 6,
	})
	if err != nil {
		return fmt.Errorf("constructing Eino ReAct agent: %w", err)
	}

	tracker := newUsageToolTracker()

	start := time.Now()
	reply, err := agentInst.Generate(ctx, messages, agent.WithComposeOptions(compose.WithCallbacks(tracker.handler())))
	out.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		return fmt.Errorf("Eino ReAct agent Generate: %w", err)
	}

	out.Reply = reply.Content
	out.InputTokens, out.OutputTokens = tracker.totals()
	out.ToolCalls = tracker.calls()
	if len(out.ToolCalls) == 0 {
		return errors.New("Eino ReAct agent completed without executing the calculate tool — expected a multi-step tool call for an arithmetic request")
	}
	return nil
}

// usageToolTracker aggregates token usage across every internal ChatModel
// call the ReAct graph makes (there are at least two: the tool-call turn and
// the final-answer turn) and captures each real tool execution's arguments
// and result, via Eino's native callbacks — not by re-deriving anything from
// the final message alone, which would only reflect the LAST model call.
type usageToolTracker struct {
	mu               sync.Mutex
	promptTokens     int
	completionTokens int
	haveUsage        bool
	pending          map[string]*outputToolCall // toolCallID -> in-flight entry
	done             []outputToolCall
}

func newUsageToolTracker() *usageToolTracker {
	return &usageToolTracker{pending: map[string]*outputToolCall{}}
}

func (t *usageToolTracker) addUsage(u *model.TokenUsage) {
	if u == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.promptTokens += u.PromptTokens
	t.completionTokens += u.CompletionTokens
	t.haveUsage = true
}

func (t *usageToolTracker) totals() (int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.haveUsage {
		return -1, -1
	}
	return t.promptTokens, t.completionTokens
}

func (t *usageToolTracker) calls() []outputToolCall {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]outputToolCall, len(t.done))
	copy(out, t.done)
	return out
}

func (t *usageToolTracker) startTool(ctx context.Context, info *callbacks.RunInfo, in *toolcomp.CallbackInput) {
	if in == nil {
		return
	}
	callID := compose.GetToolCallID(ctx)
	entry := &outputToolCall{Name: info.Name}
	if in.ArgumentsInJSON != "" {
		args := map[string]any{}
		if err := json.Unmarshal([]byte(in.ArgumentsInJSON), &args); err == nil {
			entry.Arguments = args
		} else {
			entry.Arguments = map[string]any{"raw": in.ArgumentsInJSON}
		}
	}
	t.mu.Lock()
	t.pending[callID] = entry
	t.mu.Unlock()
}

func (t *usageToolTracker) endTool(ctx context.Context, out *toolcomp.CallbackOutput) {
	if out == nil {
		return
	}
	callID := compose.GetToolCallID(ctx)
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.pending[callID]
	if !ok {
		entry = &outputToolCall{}
	}
	entry.Result = out.Response
	t.done = append(t.done, *entry)
	delete(t.pending, callID)
}

func (t *usageToolTracker) handler() callbacks.Handler {
	return callbacks.NewHandlerBuilder().
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			if info == nil {
				return ctx
			}
			switch info.Component {
			case components.ComponentOfChatModel:
				if mo := model.ConvCallbackOutput(output); mo != nil {
					t.addUsage(mo.TokenUsage)
				}
			case components.ComponentOfTool:
				t.endTool(ctx, toolcomp.ConvCallbackOutput(output))
			}
			return ctx
		}).
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			if info != nil && info.Component == components.ComponentOfTool {
				t.startTool(ctx, info, toolcomp.ConvCallbackInput(input))
			}
			return ctx
		}).
		OnEndWithStreamOutputFn(func(ctx context.Context, info *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
			defer output.Close()
			isChatModel := info != nil && info.Component == components.ComponentOfChatModel
			for {
				chunk, err := output.Recv()
				if err != nil {
					return ctx // io.EOF (normal end) or a stream error — either way, done
				}
				if isChatModel {
					if mo := model.ConvCallbackOutput(chunk); mo != nil && mo.TokenUsage != nil {
						t.addUsage(mo.TokenUsage)
					}
				}
			}
		}).
		Build()
}
