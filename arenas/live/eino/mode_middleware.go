package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	einochatopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ---- "middleware" mode ----
//
// Eino has no single "middleware" primitive the way an HTTP framework does.
// It has TWO distinct native seams that, together, cover "intercept a chat
// call": github.com/cloudwego/eino/callbacks (OnStart/OnEnd/OnError — the
// same seam mode_tools.go's usageToolTracker and observability.go's
// obsRecorder already drive) and a plain model.BaseChatModel decorator (any
// type satisfying Generate/Stream can wrap a real one). This mode uses BOTH,
// for a documented, honest reason:
//
//   - callbacks.Handler is proven OBSERVE-ONLY by Eino's own doc comment on
//     callbacks.Handler (github.com/cloudwego/eino/callbacks/interface.go):
//     "do NOT mutate the Input or Output values. All downstream nodes and
//     handlers share the same pointer ... Mutations cause data races in
//     concurrent graph execution." So callbacks CANNOT be used to inject the
//     inbound sentinel — that would be mutating shared Input, exactly what
//     the doc forbids.
//   - A model.BaseChatModel decorator (mwModel below) CAN: it fully owns the
//     message slice it hands to the wrapped model's Generate call, so
//     cloning + mutating a copy before that call is safe and is Eino's
//     documented pattern for a model-level interceptor (the same shape
//     ToolCallingChatModel.WithTools itself uses — "returns a new instance,
//     does not mutate the receiver," per components/model/interface.go).
//
// So: INBOUND goes through mwModel (a model-wrapper), and OUTBOUND goes
// through Eino's native callbacks.Handler — attached via
// callbacks.InitCallbacks, the documented mechanism for running a component
// STANDALONE outside a compose.Graph (callbacks.EnsureRunInfo's own doc
// comment gives this exact recipe). The middleware.mechanism field in the
// wire output states both honestly rather than picking one label that would
// misrepresent the other half.
type middlewareOutput struct {
	Text       string         `json:"text"`
	Middleware middlewareInfo `json:"middleware"`
}

type middlewareInfo struct {
	Mechanism     string `json:"mechanism"`
	InboundFired  bool   `json:"inbound_fired"`
	OutboundFired bool   `json:"outbound_fired"`
	Invocations   int    `json:"invocations"`
	Supported     bool   `json:"supported"`
}

// mwSentinel is injected into the system prompt (or, if the request carries
// no system message, into a new leading one) before the wrapped model ever
// sees it — the INBOUND-side proof that mwModel actually intercepted the
// call, not just observed it.
const mwSentinel = "\n[ARENA_MW]"

// mwModel is the inbound-interception half: a model.BaseChatModel decorator
// wrapping the real (or, under ARENA_STUB=1, fake — see stub.go) chat model
// returned by newChatModel(). It never mutates the caller's message slice in
// place (that slice may be shared) — it builds and hands the wrapped model a
// fresh, mutated copy, then delegates.
type mwModel struct {
	inner        model.BaseChatModel
	inboundFired *bool
}

func newMWModel(inner model.BaseChatModel, fired *bool) *mwModel {
	return &mwModel{inner: inner, inboundFired: fired}
}

// injectSentinel returns a NEW message slice (the input slice and its
// schema.Message elements are never mutated in place) with mwSentinel
// appended to the system message's content, or, if no system message is
// present, a new leading system message containing just the sentinel.
func (m *mwModel) injectSentinel(in []*schema.Message) []*schema.Message {
	out := make([]*schema.Message, len(in))
	copy(out, in)

	for i, msg := range out {
		if msg.Role == schema.System {
			clone := *msg
			clone.Content += mwSentinel
			out[i] = &clone
			*m.inboundFired = true
			return out
		}
	}

	sentinelMsg := &schema.Message{Role: schema.System, Content: mwSentinel}
	out = append([]*schema.Message{sentinelMsg}, out...)
	*m.inboundFired = true
	return out
}

func (m *mwModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.inner.Generate(ctx, m.injectSentinel(in), opts...)
}

func (m *mwModel) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return m.inner.Stream(ctx, m.injectSentinel(in), opts...)
}

// typer mirrors the (unexported, structural) identity method both
// einochatopenai.ChatModel and stubChatModel implement (GetType() string),
// used here only to label the outbound callbacks.RunInfo honestly for
// whichever implementation newChatModel() actually returned.
type typer interface{ GetType() string }

// runMiddleware drives ONE qa-shaped turn through mwModel (inbound) with a
// native callbacks.Handler (outbound) attached, and writes the mode's OWN
// wire contract (middlewareOutput, not adapterOutput — same "this mode
// answers a different question" pattern mode_observability.go already
// established) as exactly one JSON object to stdout. Like
// runObservability, it owns the full stdout write and process exit code and
// never returns to main()'s generic writeOutput(out).
func runMiddleware(ctx context.Context, env *arenaEnv, in adapterInput) {
	out := middlewareOutput{
		Middleware: middlewareInfo{
			Mechanism: "eino callbacks (outbound, native OnStart/OnEnd — observe-only by Eino's own contract) + model-wrapper (inbound, since callbacks cannot mutate shared Input)",
			Supported: true,
		},
	}

	runErr := func() error {
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

		wrapped := newMWModel(chatModel, &out.Middleware.InboundFired)

		invocations := 0
		runInfoType := "unknown"
		if t, ok := chatModel.(typer); ok {
			runInfoType = t.GetType()
		}

		outboundHandler := callbacks.NewHandlerBuilder().
			OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, cbIn callbacks.CallbackInput) context.Context {
				if info != nil && info.Component == components.ComponentOfChatModel {
					invocations++
				}
				return ctx
			}).
			OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, cbOut callbacks.CallbackOutput) context.Context {
				if info != nil && info.Component == components.ComponentOfChatModel {
					if mo := model.ConvCallbackOutput(cbOut); mo != nil && mo.Message != nil {
						out.Middleware.OutboundFired = true
					}
				}
				return ctx
			}).
			Build()

		// Standalone (non-graph) attachment point for a native
		// callbacks.Handler — this call, wrapped.Generate below, is a
		// direct method call, not a compose.Graph node invocation, so
		// there is no graph layer to thread compose.WithCallbacks through.
		// callbacks.EnsureRunInfo's own doc comment gives this exact
		// recipe for that situation ("standalone callers must set it up
		// themselves").
		ctx = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{
			Type:      runInfoType,
			Component: components.ComponentOfChatModel,
			Name:      "middleware",
		}, outboundHandler)

		reply, err := wrapped.Generate(ctx, messages)
		if err != nil {
			return fmt.Errorf("Eino Generate call: %w", err)
		}

		out.Text = reply.Content
		out.Middleware.Invocations = invocations
		return nil
	}()

	if runErr != nil {
		out.Middleware.Supported = false
		fmt.Fprintln(os.Stderr, "eino-live-arena: middleware: "+runErr.Error())
	}

	enc := json.NewEncoder(os.Stdout)
	if encErr := enc.Encode(out); encErr != nil {
		fmt.Fprintln(os.Stderr, "eino-live-arena: failed to encode middleware JSON: "+encErr.Error())
		fmt.Println(`{"text":"","middleware":{"mechanism":"","inbound_fired":false,"outbound_fired":false,"invocations":0,"supported":false}}`)
		os.Exit(1)
	}
	if runErr != nil {
		os.Exit(1)
	}
	os.Exit(0) // success: stdout contract already fully written — never fall through to main()'s generic writeOutput(out)
}
