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
//     is recalled by a completely separate process invocation;
//   - observability: Genkit's OWN native OpenTelemetry tracing and structured
//     (log/slog) logging, captured in-process (own sdktrace.TracerProvider +
//     in-memory span exporter, no external collector) across one
//     representative multi-op turn — see observability.go.
//
// One JSON object on stdin selects the mode; one JSON object on stdout is the
// uniform result envelope (mode=observability is the one exception — see
// observability.go's observabilityOutput doc comment). All diagnostics go to
// stderr. See README.md for the exact per-mode contract and the rationale for
// each native-Genkit mechanism used (tool-execution loop, ai.WithOutputType
// structured output, ai.WithStreaming, OTel tracing).
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
//
// TenantID/UserID/ProductID/Fact/Query/CrossTenantID are a further additive
// extension for the memory_scoped and isolation_leak modes (scoped_memory.go)
// — a compound, hand-rolled multi-tenant scope on top of the plain
// session_id-only memory mode. Ignored by every other mode, EXCEPT that
// crash_recovery mode (crash_recovery.go) also reuses the bare `Fact` field
// (not Tenant/User/Product/CrossTenantID) as the keyword its reply is
// expected to recall from an earlier, already-checkpointed turn.
type arenaInput struct {
	Mode      string      `json:"mode"`
	System    string      `json:"system"`
	Messages  []inMessage `json:"messages"`
	Tools     []inTool    `json:"tools,omitempty"`
	SessionID string      `json:"session_id,omitempty"`
	TopK      int         `json:"top_k,omitempty"`

	// memory_scoped / isolation_leak only.
	TenantID      string `json:"tenant_id,omitempty"`
	UserID        string `json:"user_id,omitempty"`
	ProductID     string `json:"product_id,omitempty"`
	Fact          string `json:"fact,omitempty"`
	Query         string `json:"query,omitempty"`
	CrossTenantID string `json:"cross_tenant_id,omitempty"`
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

// scopedRecallInfo is the memory_scoped-mode proof payload: whether a recall
// under the SAME compound scope hit the just-written fact, and whether a
// recall under a DIFFERENT tenant (same user/product/session) came back
// empty — see scoped_memory.go.
type scopedRecallInfo struct {
	SameScopeHit    bool   `json:"same_scope_hit"`
	CrossTenantMiss bool   `json:"cross_tenant_miss"`
	RecalledText    string `json:"recalled_text"`
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

	// memory_scoped only; nil (JSON null) for every other mode, matching the
	// Structured field's null-when-unused convention above.
	ScopedRecall *scopedRecallInfo `json:"scoped_recall"`
	// isolation_leak only; nil (JSON null) for every other mode. false = good
	// (no leak observed), true = the scoped store or the model's grounded
	// reply exposed tenant A's secret to tenant B's scope.
	LeakDetected *bool `json:"leak_detected"`

	// crash_recovery only; nil (JSON null) for every other mode — see
	// crash_recovery.go and README.md "crash_recovery".
	// Resumed: true if this invocation found any prior CHECKPOINTED turn in
	// rqlite (i.e. it picked up state left behind by an earlier, now-dead
	// process, not just its own fresh session).
	Resumed *bool `json:"resumed"`
	// PrecrashFactRecalled: true if `fact` (arenaInput.Fact) was supplied AND
	// this reply's text recalls it — proving the pre-crash checkpointed turn
	// actually reached the model, not merely that a row exists in rqlite. Nil
	// when the caller supplied no `fact` to check (e.g. a plain checkpoint
	// step, not the recovery assertion itself).
	PrecrashFactRecalled *bool `json:"precrash_fact_recalled"`
	// StateLost: the inverse of PrecrashFactRecalled when `fact` was
	// supplied — true means the pre-crash checkpointed fact was NOT
	// recovered. This does NOT track the immediately-preceding, deliberately
	// uncommitted turn (the one a crash test kills mid-flight) — that turn's
	// absence is the expected, correct behavior of a per-turn checkpoint
	// boundary, not a "loss" of durable state. See README.md.
	StateLost *bool `json:"state_lost"`
	// PriorTurnsLoaded: how many checkpointed turn rows this invocation
	// loaded from rqlite before generating — 0 for the very first turn of a
	// session, or if an immediately-prior turn was killed before it could
	// checkpoint.
	PriorTurnsLoaded *int `json:"prior_turns_loaded"`
	// Checkpointed: true once this invocation's own turn was durably written
	// to rqlite. Only ever false in the JSON emitted on the failure path
	// (Error non-empty) — a successful crash_recovery run always sets this,
	// since the checkpoint calls happen before any success return.
	Checkpointed *bool `json:"checkpointed"`

	Error string `json:"error"`
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

	// mode=observability and mode=middleware do NOT use the shared
	// arenaOutput envelope (see observability.go's observabilityOutput doc
	// comment and middleware.go's middlewareOutput doc comment) —
	// runObservability/runMiddleware stash their own struct in
	// out.Structured, which is encoded here instead of out itself.
	var encodeErr error
	switch out.Mode {
	case "observability":
		obs, ok := out.Structured.(*observabilityOutput)
		if !ok {
			// run() failed before reaching runObservability (e.g. missing
			// OPENAI_API_KEY, bad stdin JSON) — still emit a valid
			// observability-shaped envelope rather than the generic one.
			obs = &observabilityOutput{Framework: framework, Mode: "observability", Error: out.Error}
		}
		encodeErr = json.NewEncoder(os.Stdout).Encode(obs)
	case "middleware":
		mw, ok := out.Structured.(*middlewareOutput)
		if !ok {
			// run() failed before reaching runMiddleware — still emit a
			// valid middleware-shaped envelope rather than the generic one.
			mw = &middlewareOutput{Middleware: &middlewareInfo{Mechanism: "ai.ModelMiddleware", Supported: true}, Error: out.Error}
		}
		encodeErr = json.NewEncoder(os.Stdout).Encode(mw)
	default:
		encodeErr = json.NewEncoder(os.Stdout).Encode(out)
	}
	if encodeErr != nil {
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
	//    read from OPENAI_API_KEY and NEVER logged or echoed. ARENA_STUB=1
	//    (stub.go's stubMode) skips this requirement entirely — every mode
	//    runs offline against a locally DefineModel'd echo model instead, see
	//    step 3 below.
	var apiKey, baseURL string
	if !stubMode {
		apiKey = os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			return fmt.Errorf("OPENAI_API_KEY is not set (set ARENA_STUB=1 to run fully offline against a fake model instead)")
		}
		baseURL = os.Getenv("OPENAI_BASE_URL")
		if baseURL == "" {
			return fmt.Errorf("OPENAI_BASE_URL is not set (set ARENA_STUB=1 to run fully offline against a fake model instead)")
		}
	}
	fmt.Fprintf(os.Stderr, "PROGRESS: model=%s base_url=%s mode=%s stub=%v\n", model, baseURL, mode, stubMode)

	ctx := context.Background()

	// 2b. mode=observability installs its own OTel TracerProvider + slog
	//     capture BEFORE genkit.Init/the first Generate call — see
	//     observability.go's setupObservabilityTracing doc comment for why
	//     ordering matters (Genkit adopts whatever TracerProvider is already
	//     globally registered).
	var obs *obsHooks
	if mode == "observability" {
		obs = setupObservabilityTracing()
	}

	// 3. Wire the chat model. ARENA_STUB=1 registers a local, offline echo
	//    model (stub.go's stubModelFunc) via genkit.DefineModel under the
	//    SAME name every mode already expects (OPENAI_MODEL), so no mode
	//    handler needs to know or care which one is live — otherwise wire
	//    Genkit's official OpenAI-compatible plugin to the real agnes
	//    endpoint, unchanged from the original single-shot adapter.
	var g *genkit.Genkit
	var m ai.Model
	if stubMode {
		g = genkit.Init(ctx)
		m = genkit.DefineModel(g, model, &ai.ModelOptions{
			Label:    "stub/" + model,
			Supports: stubModelSupports,
		}, stubModelFunc)
		fmt.Fprintln(os.Stderr, "PROGRESS: ARENA_STUB=1 — using a locally genkit.DefineModel'd echo model + in-memory rqlite/qdrant/embed fakes (no network); see stub.go")
	} else {
		oai := &oaiplugin.OpenAI{
			APIKey: apiKey,
			Opts:   []option.RequestOption{option.WithBaseURL(baseURL)},
		}
		g = genkit.Init(ctx, genkit.WithPlugins(oai))
		m = oai.DefineModel(model, ai.ModelOptions{
			Label:    "agnes/" + model,
			Supports: &compat_oai.Multimodal,
		})
	}

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
	case "memory_scoped":
		return runMemoryScoped(rc, out)
	case "isolation_leak":
		return runIsolationLeak(rc, out)
	case "crash_recovery":
		return runCrashRecovery(rc, out)
	case "structured":
		return runStructured(rc, out)
	case "stream":
		return runStream(rc, out)
	case "observability":
		return runObservability(rc, out, obs)
	case "middleware":
		return runMiddleware(rc, out)
	default:
		return fmt.Errorf("unknown mode %q (want one of: qa, rag, tools, memory, memory_scoped, isolation_leak, crash_recovery, structured, stream, observability, middleware)", mode)
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
