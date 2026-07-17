// Command genkit-go-live-arena is an EXHAUSTIVE multi-mode live arena adapter
// for Google Genkit (Go), driven against real infrastructure end to end:
//
//   - chat: agnes-2.0-flash (OpenAI-compatible) via Genkit's official
//     compat_oai/openai plugin (github.com/firebase/genkit/go/plugins/compat_oai/openai);
//   - embeddings: a real OpenAI-compatible e5 server (EMBED_URL, default
//     http://127.0.0.1:8899, model "multilingual-e5-small-query"/"-passage");
//   - vector search: a real Qdrant collection "arena_rag" (QDRANT_URL, default
//     http://localhost:6333) via github.com/qdrant/go-client (Genkit-Go ships
//     no first-party Qdrant retriever/indexer plugin as of v1.10.0, so this
//     adapter drives Qdrant directly while still routing all generation
//     through Genkit — see README.md "Why go-client, not a Genkit plugin");
//   - durable memory: a real rqlite node over HTTP (RQLITE_URL, default
//     http://localhost:4001), so multi-turn state survives a process exit and
//     is recalled by a completely separate process invocation.
//
// One JSON object on stdin selects the mode; one JSON object on stdout is the
// uniform result envelope. All diagnostics go to stderr. See README.md for the
// exact per-mode contract and the rationale for each native-Genkit mechanism
// used (tool-execution loop, ai.WithOutputType structured output,
// ai.WithStreaming).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/compat_oai"
	oaiplugin "github.com/firebase/genkit/go/plugins/compat_oai/openai"
	"github.com/openai/openai-go/option"
)

const framework = "genkit-go"

// ---- Uniform arena contract types ----

type inMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type inTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// arenaInput is the single stdin JSON object. The base fields (system,
// messages, tools) are the SPEC.md single-shot contract every arenas/live/*
// adapter honors; mode, session_id and top_k are additive extensions specific
// to this exhaustive adapter and are ignored by the base single-shot runner
// (which never sets them, so mode defaults to "qa" and this adapter stays a
// drop-in replacement for the original single-shot binary).
type arenaInput struct {
	Mode      string      `json:"mode"`
	System    string      `json:"system"`
	Messages  []inMessage `json:"messages"`
	Tools     []inTool    `json:"tools,omitempty"`
	SessionID string      `json:"session_id,omitempty"`
	TopK      int         `json:"top_k,omitempty"`
}

type outToolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type retrievedDoc struct {
	DocID string  `json:"doc_id"`
	Title string  `json:"title"`
	Text  string  `json:"text"`
	Score float32 `json:"score"`
}

type arenaOutput struct {
	Framework    string         `json:"framework"`
	Mode         string         `json:"mode"`
	Model        string         `json:"model"`
	Reply        string         `json:"reply"`
	InputTokens  int            `json:"input_tokens"`
	OutputTokens int            `json:"output_tokens"`
	LatencyMs    int64          `json:"latency_ms"`
	ToolCalls    []outToolCall  `json:"tool_calls"`
	Retrieved    []retrievedDoc `json:"retrieved"`
	Structured   any            `json:"structured"`
	TTFTMs       int64          `json:"ttft_ms"`
	Error        string         `json:"error"`
}

// runCtx bundles everything a mode handler needs: the parsed request, the
// initialized Genkit instance + registered model, and env-derived config for
// the real infra endpoints. Built once in main() and passed to whichever mode
// handler is selected.
type runCtx struct {
	ctx   context.Context
	g     *genkit.Genkit
	model ai.Model
	in    arenaInput

	modelName string
	embedURL  string
	qdrantURL string
	rqliteURL string
}

func main() {
	model := envOr("OPENAI_MODEL", "agnes-2.0-flash")

	// The output is always emitted as a single JSON object on stdout, even on
	// failure. Fields are best-effort/zero-valued when an error is set.
	out := arenaOutput{
		Framework:    framework,
		Model:        model,
		InputTokens:  -1,
		OutputTokens: -1,
		ToolCalls:    []outToolCall{},
		Retrieved:    []retrievedDoc{},
	}

	if err := run(&out, model); err != nil {
		out.Error = err.Error()
		fmt.Fprintf(os.Stderr, "[genkit-go] error: %v\n", err)
	}

	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		// Last-ditch: emit a minimal valid JSON error object.
		fmt.Fprintf(os.Stdout, "{\"framework\":%q,\"mode\":%q,\"model\":%q,\"error\":\"failed to encode output\"}\n", framework, out.Mode, model)
		os.Exit(1)
	}
}

func run(out *arenaOutput, model string) error {
	// 1. Read and parse the single stdin JSON object.
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	var in arenaInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("parse stdin JSON: %w", err)
	}
	mode := in.Mode
	if mode == "" {
		mode = "qa"
	}
	out.Mode = mode
	fmt.Fprintf(os.Stderr, "PROGRESS: mode=%s\n", mode)

	// 2. Resolve the real chat endpoint from the environment. The API key is
	//    read from OPENAI_API_KEY and NEVER logged or echoed.
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("OPENAI_API_KEY is not set")
	}
	baseURL := os.Getenv("OPENAI_BASE_URL")
	if baseURL == "" {
		return fmt.Errorf("OPENAI_BASE_URL is not set")
	}
	fmt.Fprintf(os.Stderr, "PROGRESS: model=%s base_url=%s mode=%s\n", model, baseURL, mode)

	ctx := context.Background()

	// 3. Wire Genkit's official OpenAI-compatible plugin to agnes.
	oai := &oaiplugin.OpenAI{
		APIKey: apiKey,
		Opts:   []option.RequestOption{option.WithBaseURL(baseURL)},
	}
	g := genkit.Init(ctx, genkit.WithPlugins(oai))

	m := oai.DefineModel(model, ai.ModelOptions{
		Label:    "agnes/" + model,
		Supports: &compat_oai.Multimodal,
	})

	rc := &runCtx{
		ctx:       ctx,
		g:         g,
		model:     m,
		in:        in,
		modelName: model,
		embedURL:  envOr("EMBED_URL", "http://127.0.0.1:8899"),
		qdrantURL: envOr("QDRANT_URL", "http://localhost:6333"),
		rqliteURL: envOr("RQLITE_URL", "http://localhost:4001"),
	}

	switch mode {
	case "qa":
		return runQA(rc, out)
	case "rag":
		return runRAG(rc, out)
	case "tools":
		return runTools(rc, out)
	case "memory":
		return runMemory(rc, out)
	case "structured":
		return runStructured(rc, out)
	case "stream":
		return runStream(rc, out)
	default:
		return fmt.Errorf("unknown mode %q (want one of: qa, rag, tools, memory, structured, stream)", mode)
	}
}

// runQA is the original single-shot mode: one call to agnes via Genkit, tools
// (if any) captured single-shot via WithReturnToolRequests(true) — never
// executed. This is the SPEC.md base-contract behavior, unchanged, so this
// adapter remains a drop-in replacement for arenas/live/runner.
func runQA(rc *runCtx, out *arenaOutput) error {
	opts := buildBaseOpts(rc)

	if len(rc.in.Tools) > 0 {
		toolRefs := make([]ai.ToolRef, 0, len(rc.in.Tools))
		for _, t := range rc.in.Tools {
			schema := t.Parameters
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			def := genkit.DefineToolWithInputSchema[any](rc.g, t.Name, t.Description, schema,
				func(*ai.ToolContext, any) (any, error) {
					return nil, fmt.Errorf("qa mode: single-shot capture, tool %q not executed", t.Name)
				})
			toolRefs = append(toolRefs, def)
		}
		opts = append(opts, ai.WithTools(toolRefs...), ai.WithReturnToolRequests(true))
	}

	start := time.Now()
	resp, err := genkit.Generate(rc.ctx, rc.g, opts...)
	out.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		return fmt.Errorf("genkit.Generate: %w", err)
	}

	applyUsage(out, resp)
	for _, part := range resp.Message.Content {
		switch {
		case part.IsText():
			out.Reply += part.Text
		case part.IsToolRequest() && part.ToolRequest != nil:
			out.ToolCalls = append(out.ToolCalls, outToolCall{
				Name:      part.ToolRequest.Name,
				Arguments: toArgsMap(part.ToolRequest.Input),
			})
		}
	}
	return nil
}

// buildBaseOpts builds the common WithModel/WithSystem/WithMessages options
// shared by every mode.
func buildBaseOpts(rc *runCtx) []ai.GenerateOption {
	opts := []ai.GenerateOption{ai.WithModel(rc.model)}
	if rc.in.System != "" {
		opts = append(opts, ai.WithSystem(rc.in.System))
	}
	msgs := toGenkitMessages(rc.in.Messages)
	if len(msgs) > 0 {
		opts = append(opts, ai.WithMessages(msgs...))
	}
	return opts
}

func toGenkitMessages(in []inMessage) []*ai.Message {
	msgs := make([]*ai.Message, 0, len(in))
	for _, msg := range in {
		role := ai.RoleUser
		switch msg.Role {
		case "assistant", "model":
			role = ai.RoleModel
		case "system":
			role = ai.RoleSystem
		case "user", "":
			role = ai.RoleUser
		default:
			role = ai.RoleUser
		}
		msgs = append(msgs, &ai.Message{
			Role:    role,
			Content: []*ai.Part{ai.NewTextPart(msg.Content)},
		})
	}
	return msgs
}

func applyUsage(out *arenaOutput, resp *ai.ModelResponse) {
	if resp.Usage != nil {
		if resp.Usage.InputTokens > 0 {
			out.InputTokens = resp.Usage.InputTokens
		}
		if resp.Usage.OutputTokens > 0 {
			out.OutputTokens = resp.Usage.OutputTokens
		}
	}
}

// toArgsMap normalizes a ToolRequest.Input (which may arrive as a
// map[string]any or as a JSON string) into a map[string]any for the contract.
func toArgsMap(input any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	if m, ok := input.(map[string]any); ok {
		return m
	}
	if s, ok := input.(string); ok {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err == nil {
			return m
		}
	}
	var m map[string]any
	if b, err := json.Marshal(input); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
