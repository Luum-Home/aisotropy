// Command strengths STEELMANS Google ADK Go (google.golang.org/adk/v2 v2.0.0):
// it EXECUTES, against the real framework and fully offline, the capabilities
// where ADK is genuinely strong and aisotropy — a single-runtime, not a
// multi-agent orchestration toolkit — is not trying to compete.
//
//	go run ./cmd/strengths
//
// Three strengths, each grounded in the pinned v2 source and run to exit 0:
//
//  1. Multi-agent delegation via the BUILT-IN transfer_to_agent tool.
//     Symbols: llmagent.Config.SubAgents (agent/llmagent/llmagent.go:194) +
//     the framework-injected "transfer_to_agent" tool
//     (internal/llminternal/agent_transfer.go: transferAgentName). A coordinator
//     routes to a specialist sub-agent in-process; one runner.Run returns both
//     the transfer event AND the sub-agent's answer.
//
//  2. The full callback pipeline (Before/After Model + Before/After Tool).
//     Symbols: llmagent.Config.Before/AfterModelCallbacks (llmagent.go:228/237)
//     and Before/AfterToolCallbacks (llmagent.go:316/322). We record the order
//     the framework fires them around a real tool-using turn.
//
//  3. gen_ai OTel semantic-convention spans, emitted by the framework with no
//     ADK config. Symbols: internal/telemetry/node_tracing.go ("invoke_agent
//     <name>") + the global tracer. We capture them with an in-memory recorder.
//
// Only the model is faked (via the framework's own model.LLM seam); every agent,
// runner, callback, transfer, and span is the real ADK code path. No API key.
package main

import (
	"context"
	"fmt"
	"iter"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

func main() {
	line := strings.Repeat("=", 74)
	fmt.Println(line)
	fmt.Println("ADK Go (google.golang.org/adk/v2 v2.0.0) — STEELMAN: what it does well")
	fmt.Println(line)
	fmt.Println("Real llmagent + runner + session.Service + built-in tools + callbacks +")
	fmt.Println("OTel telemetry. Only the model is faked via the model.LLM seam. No API key.")

	ctx := context.Background()
	ok := true
	ok = strengthMultiAgent(ctx) && ok
	ok = strengthCallbackPipeline(ctx) && ok
	ok = strengthOTelSpans(ctx) && ok

	fmt.Println()
	fmt.Println(strings.Repeat("-", 74))
	fmt.Println("Honest framing: these are real ADK strengths. ADK is Google's agent")
	fmt.Println("framework with first-class MULTI-AGENT orchestration + gen_ai observability.")
	fmt.Println("aisotropy is a single-runtime cognitive engine, NOT a multi-agent toolkit —")
	fmt.Println("its narrow edge is isolation-by-default (all 4 scope axes bound from ctx as")
	fmt.Println("an unforgeable Scope), not agent-to-agent delegation. Different tools.")
	fmt.Println(strings.Repeat("-", 74))

	if !ok {
		fmt.Println("\nResult: FAILED — a strength did not execute as claimed.")
		os.Exit(1)
	}
	fmt.Println("\nResult: PASS — all three strengths executed against the real ADK stack, offline.")
}

// ---------------------------------------------------------------------------
// Fake models (offline model.LLM implementations).
// ---------------------------------------------------------------------------

// routerModel is a coordinator model: it emits the BUILT-IN transfer_to_agent
// FunctionCall to route to a specialist by keyword. If it is re-entered after a
// transfer already happened (defensive guard), it finishes with plain text.
type routerModel struct{ calls atomic.Int64 }

func (m *routerModel) Name() string { return "fake-router" }
func (m *routerModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls.Add(1)
		var lastUser string
		transferred := false
		for _, c := range req.Contents {
			if c == nil {
				continue
			}
			for _, p := range c.Parts {
				if p == nil {
					continue
				}
				if p.FunctionResponse != nil && p.FunctionResponse.Name == "transfer_to_agent" {
					transferred = true
				}
				if p.Text != "" && c.Role == genai.RoleUser {
					lastUser = p.Text
				}
			}
		}
		if transferred {
			yield(&model.LLMResponse{Content: &genai.Content{Role: genai.RoleModel,
				Parts: []*genai.Part{{Text: "coordinator: handed off, nothing more to do."}}}}, nil)
			return
		}
		target := "billing_agent"
		if strings.Contains(strings.ToLower(lastUser), "refund") {
			target = "refunds_agent"
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: genai.RoleModel,
			Parts: []*genai.Part{genai.NewPartFromFunctionCall("transfer_to_agent", map[string]any{"agent_name": target})}}}, nil)
	}
}

// voiceModel is a specialist sub-agent model: it answers in a fixed voice so the
// author of the final response is observable end to end.
type voiceModel struct {
	voice string
	calls atomic.Int64
}

func (m *voiceModel) Name() string { return "fake-" + m.voice }
func (m *voiceModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls.Add(1)
		var lastUser string
		for _, c := range req.Contents {
			if c == nil {
				continue
			}
			for _, p := range c.Parts {
				if p != nil && p.Text != "" && c.Role == genai.RoleUser {
					lastUser = p.Text
				}
			}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: genai.RoleModel,
			Parts: []*genai.Part{{Text: "[" + m.voice + "] resolved: " + lastUser}}}}, nil)
	}
}

// toolThenTextModel emits a get_status FunctionCall the first time, then a text
// answer once it sees the tool's FunctionResponse — driving the real tool loop
// so the tool callbacks fire.
type toolThenTextModel struct{ steps atomic.Int64 }

func (m *toolThenTextModel) Name() string { return "fake-tooluser" }
func (m *toolThenTextModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.steps.Add(1)
		sawResponse := false
		for _, c := range req.Contents {
			if c == nil {
				continue
			}
			for _, p := range c.Parts {
				if p != nil && p.FunctionResponse != nil {
					sawResponse = true
				}
			}
		}
		if !sawResponse {
			yield(&model.LLMResponse{Content: &genai.Content{Role: genai.RoleModel,
				Parts: []*genai.Part{genai.NewPartFromFunctionCall("get_status", map[string]any{"id": "svc-1"})}}}, nil)
			return
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: genai.RoleModel,
			Parts: []*genai.Part{{Text: "status retrieved via tool."}}}}, nil)
	}
}

// ---------------------------------------------------------------------------
// Strength 1 — multi-agent delegation (built-in transfer_to_agent).
// ---------------------------------------------------------------------------

func strengthMultiAgent(ctx context.Context) bool {
	hdr("STRENGTH 1 — Multi-agent delegation (built-in transfer_to_agent tool)")

	billing, err := llmagent.New(llmagent.Config{
		Name: "billing_agent", Model: &voiceModel{voice: "billing"},
		Description: "Handles invoices and billing questions."})
	must(err)
	refunds, err := llmagent.New(llmagent.Config{
		Name: "refunds_agent", Model: &voiceModel{voice: "refunds"},
		Description: "Handles refunds and chargebacks."})
	must(err)

	router := &routerModel{}
	coordinator, err := llmagent.New(llmagent.Config{
		Name: "coordinator", Model: router,
		Description: "Front desk; routes to a specialist.",
		SubAgents:   []agent.Agent{billing, refunds}, // BUILT-IN: injects transfer_to_agent
	})
	must(err)

	r, err := runner.New(runner.Config{AppName: "orbit", Agent: coordinator,
		SessionService: session.InMemoryService(), AutoCreateSession: true})
	must(err)

	// One runner.Run on the coordinator; the model asks to transfer to refunds.
	msg := &genai.Content{Role: genai.RoleUser,
		Parts: []*genai.Part{genai.NewPartFromText("I need a refund on invoice 42")}}
	sawTransfer, transferTarget, finalAuthor, finalText := "", "", "", ""
	for ev, e := range r.Run(ctx, "acme|alice", "s1", msg, agent.RunConfig{}) {
		if e != nil {
			fmt.Println("  run error:", e)
			return false
		}
		if ev == nil || ev.LLMResponse.Content == nil {
			continue
		}
		for _, p := range ev.LLMResponse.Content.Parts {
			if p == nil {
				continue
			}
			if p.FunctionCall != nil && p.FunctionCall.Name == "transfer_to_agent" {
				sawTransfer = "transfer_to_agent"
				if v, ok := p.FunctionCall.Args["agent_name"].(string); ok {
					transferTarget = v
				}
			}
			if p.Text != "" {
				finalAuthor, finalText = ev.Author, p.Text
			}
		}
	}

	fmt.Printf("  coordinator emitted: %s(agent_name=%q)\n", sawTransfer, transferTarget)
	fmt.Printf("  final response author=%q text=%q\n", finalAuthor, finalText)

	pass := sawTransfer == "transfer_to_agent" &&
		transferTarget == "refunds_agent" &&
		finalAuthor == "refunds_agent" &&
		strings.Contains(finalText, "[refunds]")
	verdict("built-in transfer routed in-process to the refunds specialist (single runner.Run)", pass)
	return pass
}

// ---------------------------------------------------------------------------
// Strength 2 — the full callback pipeline.
// ---------------------------------------------------------------------------

func strengthCallbackPipeline(ctx context.Context) bool {
	hdr("STRENGTH 2 — Callback pipeline (Before/After Model + Before/After Tool)")

	var mu sync.Mutex
	var order []string
	rec := func(name string) { mu.Lock(); order = append(order, name); mu.Unlock() }

	statusTool, err := functiontool.New(
		functiontool.Config{Name: "get_status", Description: "status by id"},
		func(_ agent.Context, a struct {
			ID string `json:"id"`
		}) (struct {
			State string `json:"state"`
		}, error) {
			return struct {
				State string `json:"state"`
			}{State: "green"}, nil
		})
	must(err)

	ag, err := llmagent.New(llmagent.Config{
		Name: "pipeline", Model: &toolThenTextModel{}, Tools: []tool.Tool{statusTool},
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{
			func(agent.Context, *model.LLMRequest) (*model.LLMResponse, error) {
				rec("BeforeModel")
				return nil, nil
			}},
		AfterModelCallbacks: []llmagent.AfterModelCallback{
			func(agent.Context, *model.LLMResponse, error) (*model.LLMResponse, error) {
				rec("AfterModel")
				return nil, nil
			}},
		BeforeToolCallbacks: []llmagent.BeforeToolCallback{
			func(agent.Context, tool.Tool, map[string]any) (map[string]any, error) {
				rec("BeforeTool")
				return nil, nil
			}},
		AfterToolCallbacks: []llmagent.AfterToolCallback{
			func(agent.Context, tool.Tool, map[string]any, map[string]any, error) (map[string]any, error) {
				rec("AfterTool")
				return nil, nil
			}},
	})
	must(err)

	r, err := runner.New(runner.Config{AppName: "orbit", Agent: ag,
		SessionService: session.InMemoryService(), AutoCreateSession: true})
	must(err)

	msg := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{genai.NewPartFromText("check status")}}
	for _, e := range r.Run(ctx, "acme|alice", "s1", msg, agent.RunConfig{}) {
		if e != nil {
			fmt.Println("  run error:", e)
			return false
		}
	}

	fmt.Printf("  fire order: %s\n", strings.Join(order, " -> "))
	// The tool loop must fire both model callbacks around each model call and
	// both tool callbacks around the tool execution, tool bracketed by models.
	has := func(s string) bool {
		for _, o := range order {
			if o == s {
				return true
			}
		}
		return false
	}
	pass := has("BeforeModel") && has("AfterModel") && has("BeforeTool") && has("AfterTool") &&
		idx(order, "BeforeTool") > idx(order, "BeforeModel") &&
		idx(order, "AfterTool") > idx(order, "BeforeTool")
	verdict("all four model/tool callbacks fired, tool bracketed inside the model loop", pass)
	return pass
}

// ---------------------------------------------------------------------------
// Strength 3 — gen_ai OTel semantic-convention spans (no ADK config).
// ---------------------------------------------------------------------------

func strengthOTelSpans(ctx context.Context) bool {
	hdr("STRENGTH 3 — gen_ai OTel spans (framework-emitted, self-hostable)")

	spanRec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRec))
	otel.SetTracerProvider(tp) // ADK reads otel.GetTracerProvider() — no ADK config
	defer func() { _ = tp.Shutdown(ctx) }()

	ag, err := llmagent.New(llmagent.Config{Name: "traced", Model: &voiceModel{voice: "obs"}})
	must(err)
	r, err := runner.New(runner.Config{AppName: "orbit", Agent: ag,
		SessionService: session.InMemoryService(), AutoCreateSession: true})
	must(err)

	msg := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{genai.NewPartFromText("trace me")}}
	for _, e := range r.Run(ctx, "acme|alice", "s1", msg, agent.RunConfig{}) {
		if e != nil {
			fmt.Println("  run error:", e)
			return false
		}
	}
	_ = tp.ForceFlush(ctx)

	var spanNames []string
	genAIAttrs := 0
	sawInvokeAgent := false
	for _, s := range spanRec.Ended() {
		spanNames = append(spanNames, s.Name())
		if strings.HasPrefix(s.Name(), "invoke_agent") {
			sawInvokeAgent = true
		}
		for _, a := range s.Attributes() {
			if strings.HasPrefix(string(a.Key), "gen_ai.") {
				genAIAttrs++
			}
		}
	}
	fmt.Printf("  captured spans: %v\n", spanNames)
	fmt.Printf("  gen_ai.* attributes across spans: %d\n", genAIAttrs)
	pass := sawInvokeAgent && genAIAttrs > 0
	verdict("framework emitted invoke_agent + gen_ai.* spans via the global tracer", pass)
	return pass
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func hdr(s string) {
	fmt.Println()
	fmt.Println(strings.Repeat("-", 74))
	fmt.Println(s)
	fmt.Println(strings.Repeat("-", 74))
}

func verdict(what string, pass bool) {
	mark := "ok  "
	if !pass {
		mark = "FAIL"
	}
	fmt.Printf("  [%s] %s\n", mark, what)
}

func idx(ss []string, v string) int {
	for i, s := range ss {
		if s == v {
			return i
		}
	}
	return -1
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
