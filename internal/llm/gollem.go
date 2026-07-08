// Phase 2 (ADR-0008, docs/decisions/0008-reflection-loop-gollem-deferral.md):
// integrates Gollem (github.com/fugue-labs/gollem, v0.5.1) as the real
// orchestrator of the structured output + reflection loop, replacing the
// Phase 1 homegrown loop (Reflect[T], see reflect.go).
//
// Real API verified with `go doc github.com/fugue-labs/gollem/core` (v0.5.1):
//
//	type Model interface {
//	    Request(ctx context.Context, messages []ModelMessage, settings *ModelSettings, params *ModelRequestParameters) (*ModelResponse, error)
//	    RequestStream(ctx context.Context, messages []ModelMessage, settings *ModelSettings, params *ModelRequestParameters) (StreamedResponse, error)
//	    ModelName() string
//	}
//
//	func NewAgent[T any](model Model, opts ...AgentOption[T]) *Agent[T]
//	func RunWithReflection[T any](ctx context.Context, agent *Agent[T], prompt string,
//	    validator func(ctx context.Context, output T) (feedback string, err error),
//	    opts ...ReflectOption) (*ReflectResult[T], error)
//
// For a struct T (not string), Agent[T] derives a JSON Schema by reflection
// and by default uses OutputModeTool: it asks the model to "call" a
// synthetic tool (core.DefaultOutputToolName = "final_result") whose
// arguments are T's JSON. If the JSON does not validate against T, Agent
// itself retries internally by resending the exact error as a
// RetryPromptPart — this is, character for character, the "reflection loop /
// output repair" mechanism that docs/components/gollem-orchestrator.md
// attributes to Gollem, and that in Phase 1 we covered by hand with
// Reflect[T].
//
// GollemModel (this file) wraps our Client (StreamChat-only, see llm.go)
// behind core.Model so Agent[T] can invoke it. Our Client is lower-level
// than Gollem (it has no native tool-calling), so the adapter resolves
// "tool" mode with explicit prompt engineering: it appends the output tool's
// JSON Schema to the prompt and asks the model to respond EXCLUSIVELY with
// that arguments' JSON; the raw response is wrapped as
// core.ToolCallResponse(toolName, argsJSON) so the rest of the Agent[T]
// pipeline (parsing, validation, reflection loop) runs unchanged. This is
// exactly what ADR-0008 flags as pending verification against the real API
// (step 1 of the integration plan).
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/fugue-labs/gollem/core"
)

// GollemModel adapts an llm.Client (our narrow streaming interface) to
// Gollem's core.Model interface, so a core.Agent[T] can be mounted on top
// without re-implementing the provider's HTTP client.
//
// Known limitation (documented, not hidden): this adapter only covers the
// "structured output via prompting" path (OutputModeTool/Native via text).
// It does not support Gollem's native function-tool calling (core.FuncTool)
// because our Client does not expose that primitive — out of scope for
// Phase 2 (which only asks to replace the Phase 1 reflection loop).
type GollemModel struct {
	client Client
}

// NewGollemModel wraps client behind core.Model.
func NewGollemModel(client Client) *GollemModel {
	return &GollemModel{client: client}
}

// ModelName identifies the underlying provider (for logs/observability),
// reusing Client.Name().
func (m *GollemModel) ModelName() string { return m.client.Name() }

// Request implements core.Model: builds a single text prompt from Gollem's
// ModelMessage/params, invokes the underlying Client until the stream is
// exhausted, and wraps the raw response according to the requested output
// mode (tool/native vs free text).
func (m *GollemModel) Request(ctx context.Context, messages []core.ModelMessage, _ *core.ModelSettings, params *core.ModelRequestParameters) (*core.ModelResponse, error) {
	system, prompt := buildGollemPrompt(messages, params)

	raw, err := generateFull(ctx, m.client, system, prompt)
	if err != nil {
		return nil, fmt.Errorf("gollem model %q: %w", m.ModelName(), err)
	}

	return toModelResponse(raw, params), nil
}

// RequestStream implements core.Model in streaming mode: re-exposes the
// Client's Token values as Gollem's PartStartEvent/PartDeltaEvent/PartEndEvent,
// without extra buffering — each Token arrives as a TextPartDelta as soon as
// the Client emits it. RunWithReflection/Agent.Run (the path used by
// GollemReflect) do not exercise this method (they use Request), but it is
// correctly implemented to satisfy the core.Model interface in case a
// future phase uses Agent.RunStream.
func (m *GollemModel) RequestStream(ctx context.Context, messages []core.ModelMessage, _ *core.ModelSettings, params *core.ModelRequestParameters) (core.StreamedResponse, error) {
	system, prompt := buildGollemPrompt(messages, params)

	tokens, err := m.client.StreamChat(ctx, system, prompt)
	if err != nil {
		return nil, fmt.Errorf("gollem model %q: %w", m.ModelName(), err)
	}

	return &gollemStream{tokens: tokens, params: params}, nil
}

// gollemStream adapts our Client's <-chan Token to core.StreamedResponse.
type gollemStream struct {
	tokens <-chan Token
	params *core.ModelRequestParameters
	buf    strings.Builder
	state  gollemStreamState
}

type gollemStreamState int

const (
	gollemStreamNotStarted gollemStreamState = iota
	gollemStreamRunning
	gollemStreamDone
)

func (s *gollemStream) Next() (core.ModelResponseStreamEvent, error) {
	switch s.state {
	case gollemStreamNotStarted:
		s.state = gollemStreamRunning
		return core.PartStartEvent{Index: 0, Part: core.TextPart{Content: ""}}, nil
	case gollemStreamRunning:
		tok, ok := <-s.tokens
		if tok.Err != nil {
			s.state = gollemStreamDone
			return nil, tok.Err
		}
		if !ok || tok.Done {
			s.state = gollemStreamDone
			return core.PartEndEvent{Index: 0}, nil
		}
		s.buf.WriteString(tok.Text)
		return core.PartDeltaEvent{Index: 0, Delta: core.TextPartDelta{ContentDelta: tok.Text}}, nil
	default:
		return nil, io.EOF
	}
}

func (s *gollemStream) Response() *core.ModelResponse {
	return toModelResponse(s.buf.String(), s.params)
}

func (s *gollemStream) Usage() core.Usage { return core.Usage{} }

func (s *gollemStream) Close() error { s.state = gollemStreamDone; return nil }

// generateFull drains the Client's streaming channel and returns the full
// response as a single string — Gollem (Agent.Run, via Model.Request) needs
// the full response to be able to parse/validate against T before deciding
// whether to retry.
func generateFull(ctx context.Context, client Client, system, prompt string) (string, error) {
	tokens, err := client.StreamChat(ctx, system, prompt)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case tok, ok := <-tokens:
			if tok.Err != nil {
				return "", tok.Err
			}
			if !ok || tok.Done {
				return b.String(), nil
			}
			b.WriteString(tok.Text)
		}
	}
}

// buildGollemPrompt flattens Gollem's ModelMessage history (system prompt,
// user prompt, feedback from previous retries, previous model responses)
// into a single (system, prompt) pair — what Client.StreamChat accepts. It
// appends, at the end, the JSON Schema instruction when params requests
// structured output (see outputInstruction).
func buildGollemPrompt(messages []core.ModelMessage, params *core.ModelRequestParameters) (system, prompt string) {
	var sys strings.Builder
	var body strings.Builder

	for _, msg := range messages {
		switch m := msg.(type) {
		case core.ModelRequest:
			for _, part := range m.Parts {
				switch p := part.(type) {
				case core.SystemPromptPart:
					writeLine(&sys, p.Content)
				case core.UserPromptPart:
					writeLine(&body, p.Content)
				case core.RetryPromptPart:
					writeLine(&body, "[correction required] "+p.Content)
				case core.ToolReturnPart:
					writeLine(&body, fmt.Sprintf("[previous result from %s] %v", p.ToolName, p.Content))
				}
			}
		case core.ModelResponse:
			if text := m.TextContent(); text != "" {
				writeLine(&body, "[previous model response] "+text)
			}
			for _, tc := range m.ToolCalls() {
				writeLine(&body, fmt.Sprintf("[previous call to %s] %s", tc.ToolName, tc.ArgsJSON))
			}
		}
	}

	if instr := outputInstruction(params); instr != "" {
		writeLine(&body, instr)
	}

	return strings.TrimSpace(sys.String()), strings.TrimSpace(body.String())
}

func writeLine(b *strings.Builder, s string) {
	if s == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	b.WriteString(s)
}

// primaryOutputTool returns the definition of the synthetic output tool that
// Agent[T] builds for a struct T (OutputModeTool, the default mode for
// non-string types — see core/output.go, buildOutputSchema).
func primaryOutputTool(params *core.ModelRequestParameters) *core.ToolDefinition {
	if params == nil || len(params.OutputTools) == 0 {
		return nil
	}
	return &params.OutputTools[0]
}

// outputInstruction builds the "structured output via prompt" instruction
// that replaces native tool-calling: it describes to the model the exact
// JSON Schema that Agent[T] expects (from the output tool, or from
// OutputObject in native mode) and requires it to respond only with that
// JSON. Without this, our Client (with no real tool-calling) has no way to
// know which schema it must satisfy.
func outputInstruction(params *core.ModelRequestParameters) string {
	if params == nil {
		return ""
	}

	var schema core.Schema
	switch {
	case primaryOutputTool(params) != nil:
		schema = primaryOutputTool(params).ParametersSchema
	case params.OutputObject != nil:
		schema = params.OutputObject.JSONSchema
	default:
		return ""
	}

	schemaJSON, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return ""
	}

	return "Respond ONLY with a valid JSON object that exactly satisfies " +
		"this JSON Schema (no additional text, no explanation, no markdown " +
		"backticks):\n" + string(schemaJSON)
}

// toModelResponse wraps the raw text returned by the Client as the
// ModelResponse Agent[T] expects, according to the requested output mode:
//   - If there is an output tool (OutputModeTool, the default for struct T):
//     extracts the JSON object from the text (tolerating surrounding
//     prose/backticks) and wraps it as ToolCallResponse — the synthetic
//     "tool call" that Agent[T] will parse/validate against T.
//   - Otherwise, treats it as free text (OutputModeText/Native with T=string).
func toModelResponse(raw string, params *core.ModelRequestParameters) *core.ModelResponse {
	raw = strings.TrimSpace(raw)

	if tool := primaryOutputTool(params); tool != nil {
		return core.ToolCallResponse(tool.Name, extractJSONObject(raw))
	}

	return core.TextResponse(raw)
}

// extractJSONObject isolates the first balanced JSON object within s,
// tolerating that the model wrapped it in prose or markdown fences despite
// the explicit instruction not to. If no '{' is found, returns s as-is — so
// Gollem's parse error (and its retry with the exact message) remains the
// self-repair mechanism, instead of hiding the problem here.
func extractJSONObject(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	start := strings.IndexByte(s, '{')
	if start < 0 {
		return s
	}

	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case inString:
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
		case c == '"':
			inString = true
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return s[start:]
}

// ADR-0010 ("ADOPT NOW"): cost accounting + guardrails/interceptors, wired
// onto the same Agent[T] GollemReflect already builds via plain
// core.AgentOption[T] values — see docs/decisions/0010-gollem-adoption-
// scope-and-observability.md and the capability analysis at
// external/GOLLEM-ANALYSIS.md §5 items 1-3. Real API verified with
// `go doc github.com/fugue-labs/gollem/core {CostTracker,WithCostTracker,
// WithInputGuardrail,MaxPromptLength,WithMessageInterceptor,RedactPII,
// AuditLog}` (v0.5.1) — every symbol below exists exactly as named; none of
// it was invented.
const (
	// gollemMaxPromptChars is a defense-in-depth input guardrail
	// (core.WithInputGuardrail + core.MaxPromptLength). Huma's ChatRequest
	// already enforces `maxLength:2000` at the HTTP boundary
	// (internal/api/chat.go), but GollemReflect is a lower-level entry point
	// with its own (and potentially future) callers, so this is a second,
	// independent ceiling — not redundant validation of the same input.
	gollemMaxPromptChars = 100_000

	// gollemPIIPattern/gollemPIIReplacement configure core.RedactPII, the
	// message interceptor ADR-0010 identifies as directly serving the
	// constitutional "secrets/PII never logged" gate
	// (.claude/rules/constitutional-gates.md). Conservative first cut: email
	// addresses only, applied to user-authored prompt content before it
	// reaches the model (and before AuditLog below would otherwise log it
	// unredacted). Extending coverage (phone numbers, API-key-shaped
	// tokens, etc.) is future work, not invented here.
	gollemPIIPattern     = `[\w.+\-]+@[\w\-]+(?:\.[\w\-]+)+`
	gollemPIIReplacement = "[REDACTED]"
)

// gollemModelPricing is intentionally empty: the model-name key
// core.CostTracker.Record uses is Model.ModelName() — for GollemModel that
// is Client.Name() (e.g. "anthropic:claude-haiku-4-5-20251001",
// "openrouter:<model>", "stub"), a dynamic per-provider/per-model string
// sourced at runtime from RegistryFromEnv, not a static enum aisotropy can
// safely hardcode $/token for here. core.CostTracker records 0 cost for an
// unrecognized model name (accounting-only degrade, not an error — see
// core.CostTracker.Record's doc). Per ADR-0010, this wiring is the
// accounting half only; the pre-provider per-tenant circuit breaker
// (granular-control.md's cost axis) is explicitly out of scope here and
// must be built on BadgerDB, not on core.UsageQuota.
//
// Separately, and more immediately limiting: GollemModel.toModelResponse
// (below) never populates core.ModelResponse.Usage, because our narrow
// llm.Client (StreamChat-only, see llm.go) does not expose provider token
// counts at all. So even with real pricing configured, tracker.Record
// currently always sees a zero core.Usage for traffic that goes through
// GollemModel — the recorded cost is 0 until Client (or a provider-specific
// wrapper) starts surfacing input/output token counts. The wiring itself
// (this file) and the AgentOption attachment are correct and tested
// independently against core.NewTestModel (gollem_test.go), which does let
// a canned response carry non-zero Usage.
var gollemModelPricing = map[string]core.ModelPricing{}

// gollemGuardrailOptions returns the ADR-0010 "ADOPT NOW" AgentOptions
// (cost tracker + input guardrail + message interceptors) shared by every
// Agent[T] GollemReflect builds. Factored out of GollemReflect (rather than
// inlined) so gollem_test.go can attach the exact same options to a bare
// core.NewAgent[T] over core.NewTestModel and assert the wiring directly
// (cost recorded when Usage is present, PII actually redacted before the
// model sees it) without depending on GollemModel's Usage-less
// ModelResponse (see the gollemModelPricing comment above).
func gollemGuardrailOptions[T any](tracker *core.CostTracker) []core.AgentOption[T] {
	return []core.AgentOption[T]{
		core.WithCostTracker[T](tracker),
		core.WithInputGuardrail[T]("max-prompt-length", core.MaxPromptLength(gollemMaxPromptChars)),
		core.WithMessageInterceptor[T](core.RedactPII(gollemPIIPattern, gollemPIIReplacement)),
		core.WithMessageInterceptor[T](core.AuditLog(gollemAuditLog)),
	}
}

// gollemAuditLog is the core.AuditLog callback: it logs only a shape summary
// (message count) at debug level, never the message content itself — the
// content has already passed through RedactPII by the time this interceptor
// runs (interceptors execute in attachment order, see
// gollemGuardrailOptions), but logging full conversation content at debug
// regardless would still risk leaking business content the PII pattern
// above does not cover.
func gollemAuditLog(direction string, messages []core.ModelMessage) {
	slog.Debug("gollem agent message audit", "direction", direction, "message_count", len(messages))
}

// GollemReflect runs structured output + reflection loop using real Gollem
// (core.Agent[T] + core.RunWithReflection[T]) over the given Client — the
// Phase 2 replacement for Reflect[T] required by ADR-0008.
//
// *Schema* validation (does the LLM's JSON decode into T?) is resolved by
// Agent[T] internally: if it fails, it retries with the exact attached error
// (see the package comment). *Business* validation (does the already
// decoded T satisfy the domain rules?) is resolved by the validator passed
// to RunWithReflection, with the same shape core.RunWithReflection expects:
// non-empty feedback => request correction; empty feedback => accept.
//
// Also attaches the ADR-0010 cost tracker + guardrails/interceptors (see
// gollemGuardrailOptions above) and, once the run completes successfully,
// logs the tracker's recorded cost/usage at slog debug (accounting only —
// see the gollemModelPricing comment for why this is currently always 0 for
// traffic through GollemModel).
func GollemReflect[T any](
	ctx context.Context,
	client Client,
	system, prompt string,
	validate func(ctx context.Context, output T) (feedback string, err error),
	maxRetries int,
) (*core.ReflectResult[T], error) {
	model := NewGollemModel(client)
	tracker := core.NewCostTracker(gollemModelPricing)

	opts := append([]core.AgentOption[T]{
		core.WithSystemPrompt[T](system),
		core.WithMaxRetries[T](maxRetries),
	}, gollemGuardrailOptions[T](tracker)...)
	agent := core.NewAgent[T](model, opts...)

	if validate == nil {
		validate = func(context.Context, T) (string, error) { return "", nil }
	}

	result, err := core.RunWithReflection[T](ctx, agent, prompt, validate, core.WithMaxReflections(maxRetries))
	if err != nil {
		return nil, err
	}

	slog.DebugContext(ctx, "gollem run cost",
		"model", client.Name(),
		"total_cost_usd", tracker.TotalCost(),
		"cost_breakdown", tracker.CostBreakdown(),
	)

	return result, nil
}
