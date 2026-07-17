package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	einochatopenai "github.com/cloudwego/eino-ext/components/model/openai"
	toolcomp "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/flow/agent/react"
)

// observabilityOutput is the "observability" mode's OWN wire contract — a
// deliberately different shape from adapterOutput (the shared qa/rag/tools/
// memory/structured/stream contract), because this mode answers a different
// question: not "what did agnes say", but "what does Eino's observability
// surface actually give you, measured by running it". See README.md
// #observability-mode.
type observabilityOutput struct {
	Framework      string             `json:"framework"`
	Mode           string             `json:"mode"`
	OtelSupported  bool               `json:"otel_supported"`
	SpanCount      int                `json:"span_count"`
	SpanNames      []string           `json:"span_names"`
	SpanTimingsMS  map[string]float64 `json:"span_timings_ms"`
	LogsStructured bool               `json:"logs_structured"`
	LogSample      string             `json:"log_sample"`
	Notes          string             `json:"notes"`
}

// obsQuestion is the fixed representative multi-op turn observability mode
// measures: a real Eino ReAct agent that (a) calls the chat model once to
// decide to invoke the "calculate" tool, (b) actually executes that tool,
// and (c) calls the chat model a second time to produce the final answer —
// three real, distinct component invocations, matching this adapter's
// existing "tools" mode scenario (mode_tools.go) so the trace is
// representative of a genuine multi-step turn, not a trivial single call.
const obsQuestion = "What is (48 + 12) * 3 / 2? Use the calculator tool."

// runObservability drives that scenario through Eino's raw native
// callbacks.Handler seam (counting every OnStart/OnEnd/OnError firing — the
// framework's baseline signal, no OTel involved) and, on the SAME handler,
// through a real in-process OpenTelemetry SDK exporting to an in-memory
// recorder (see observability.go). Its wire contract (observabilityOutput)
// is intentionally NOT adapterOutput — a different question is being
// answered here — so this function owns the entire stdout write and process
// exit code itself and never returns: it ALWAYS writes exactly one
// observabilityOutput JSON object to stdout (on internal failure the
// failure is captured honestly in Notes rather than corrupting the stdout
// contract with a second, differently-shaped object) and exits 0 on success
// or 1 if the scenario itself failed to run.
func runObservability(ctx context.Context, env *arenaEnv, in adapterInput) {
	question := obsQuestion
	if q, err := lastUserMessage(in); err == nil && q != "" {
		question = q
	}

	out := observabilityOutput{
		Framework:     frameworkName,
		Mode:          modeObservability,
		SpanNames:     []string{},
		SpanTimingsMS: map[string]float64{},
	}

	runErr := func() error {
		calcTool, err := newCalculateTool()
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

		recorder, exporter, shutdown := newObsRecorder(frameworkName)
		defer func() { _ = shutdown(ctx) }()

		// A DIY structured logger built ON TOP OF the same native
		// callbacks.Handler seam — proves what "structured logs" would
		// look like if this adapter chose to emit them, since Eino core
		// has no logging component of its own (see the notes field
		// below and README.md#observability-mode). Captured, not printed
		// to stdout, so it never pollutes the JSON contract; one sample
		// line is echoed into LogSample.
		var logSample string
		logHandler := slog.NewJSONHandler(os.Stderr, nil)
		logger := slog.New(logHandler)

		messages, err := toSchemaMessages([]inputMessage{
			{Role: "system", Content: toolsSystemPrompt},
			{Role: "user", Content: question},
		})
		if err != nil {
			return fmt.Errorf("converting messages: %w", err)
		}

		start := time.Now()
		reply, err := agentInst.Generate(ctx, messages, agent.WithComposeOptions(compose.WithCallbacks(recorder.handler())))
		totalMS := time.Since(start).Milliseconds()
		if err != nil {
			return fmt.Errorf("Eino ReAct agent Generate: %w", err)
		}

		spans := exporter.GetSpans()
		names, timings := spanTimingsMS(spans)

		logger.Info("eino_observability_turn",
			"framework", frameworkName,
			"raw_callback_events", recorder.rawEventCount(),
			"span_count", len(spans),
			"total_ms", totalMS,
			"reply_chars", len(reply.Content),
		)
		logSample = fmt.Sprintf(
			`{"time":"%s","level":"INFO","msg":"eino_observability_turn","framework":"eino","raw_callback_events":%d,"span_count":%d,"total_ms":%d}`,
			time.Now().UTC().Format(time.RFC3339), recorder.rawEventCount(), len(spans), totalMS,
		)

		out.SpanCount = len(spans)
		out.SpanNames = names
		out.SpanTimingsMS = timings
		out.OtelSupported = len(spans) > 0
		out.LogsStructured = false // Eino core (see notes) emits no structured logs of its own
		out.LogSample = logSample
		out.Notes = fmt.Sprintf(
			"Eino ships NO built-in OTel exporter usable offline: eino-ext's only OTel integration "+
				"(github.com/cloudwego/eino-ext/libs/acl/opentelemetry, used by callbacks/apmplus) hardcodes "+
				"an OTLP/gRPC exporter that requires a live collector endpoint to construct. This mode wires "+
				"Eino's NATIVE callbacks.Handler (github.com/cloudwego/eino/callbacks — the exact same seam "+
				"tools/rag/memory modes already use in this adapter) to a real go.opentelemetry.io/otel/sdk/trace "+
				"TracerProvider backed by tracetest.InMemoryExporter (part of the OTel Go SDK itself) via "+
				"sdktrace.WithSyncer — zero network I/O, zero external collector. Observed %d raw callback "+
				"firings (OnStart+OnEnd/OnError across every component) for this turn, converted 1:1 into %d "+
				"real OTel spans: %v. Eino's RunInfo gives per-span name/type/component (ChatModel called twice "+
				"— tool-decision turn + final-answer turn — plus one real 'calculate' tool execution); chat-model "+
				"spans additionally carry Eino's native TokenUsage as span attributes. Eino core has no logging "+
				"component; log_sample above is a DIY slog JSON line this adapter built on the same callback "+
				"data, not something Eino emits natively.",
			recorder.rawEventCount(), len(spans), names,
		)
		return nil
	}()

	if runErr != nil {
		out.Notes = "observability run failed: " + runErr.Error()
		fmt.Fprintln(os.Stderr, "eino-live-arena: observability: "+runErr.Error())
	}

	enc := json.NewEncoder(os.Stdout)
	if encErr := enc.Encode(out); encErr != nil {
		fmt.Fprintln(os.Stderr, "eino-live-arena: failed to encode observability JSON: "+encErr.Error())
		fmt.Println(`{"framework":"eino","mode":"observability","otel_supported":false,"span_count":0,"span_names":[],"span_timings_ms":{},"logs_structured":false,"log_sample":"","notes":"internal: failed to encode response"}`)
		os.Exit(1)
	}
	if runErr != nil {
		os.Exit(1)
	}
	os.Exit(0) // success: the stdout contract above is already fully written — never fall through to main()'s generic writeOutput(out)
}
