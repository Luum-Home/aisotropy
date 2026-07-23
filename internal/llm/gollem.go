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
	"time"

	"github.com/fugue-labs/gollem/core"
)

// GollemModel adapts an llm.Client (our narrow streaming interface) to
// Gollem's core.Model interface, so a core.Agent[T] can be mounted on top
// without re-implementing the provider's HTTP client.
//
// Native function-tool calling (ADR-0012 follow-up): this adapter now DOES
// bridge Gollem's native function-tool calling — but only when the wrapped
// Client additionally implements the optional llm.ToolCaller capability (its
// provider supports native tool-calling). In that case Request detects the
// Agent's offered params.FunctionTools and routes to the provider's native
// tool-calling API via ToolCaller.CallTools (see requestWithTools). A plain
// Client (StreamChat-only) is entirely unaffected and still resolves output
// exclusively through the "structured output via prompting" path
// (OutputModeTool/Native via text) below.
//
// Accepted 0.x caveat (option a, not hidden): ToolCaller.CallTools is
// single-prompt — it takes only (system, prompt), no ModelMessage history. So
// tool RESULTS from a prior turn reach the model FLATTENED inside `prompt`
// (buildGollemPrompt's core.ToolReturnPart arm), not as native role:"tool"
// messages. This is correct for a single tool-call-then-answer run and is the
// documented 0.x limitation; widening the llm.ToolCaller signature to carry
// history is explicitly out of scope.
type GollemModel struct {
	client Client

	// reasoningAcc, when non-nil, is a REQUEST-LOCAL reasoning carry-forward
	// accumulator (ADR-0061 Piece 2). RunToolAgent creates one per run on its own
	// goroutine and points this at it, so the prior reasoning is reinjected and
	// the fresh reasoning captured across the run's tool-call steps — a stateless,
	// provider-native carry. It is NEVER a shared/keyed map: two concurrent runs
	// have two distinct GollemModels each pointing at their own stack-local
	// ReasoningState, so tenants are structurally incapable of sharing reasoning.
	// nil (the default, e.g. the GollemReflect path) means no reasoning carry.
	reasoningAcc *ReasoningState
}

// NewGollemModel wraps client behind core.Model.
func NewGollemModel(client Client) *GollemModel {
	return &GollemModel{client: client}
}

// withReasoningAccumulator returns m carrying acc as its request-local reasoning
// carry-forward state. Used by RunToolAgent to thread a per-run ReasoningState
// through the native tool-calling steps.
func (m *GollemModel) withReasoningAccumulator(acc *ReasoningState) *GollemModel {
	m.reasoningAcc = acc
	return m
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

	// Native tool-calling path (ADR-0012 Option C): engaged ONLY when the
	// request actually offers function tools AND the wrapped Client can call
	// them natively (implements llm.ToolCaller). Otherwise fall through to the
	// unchanged structured-output/text path below. The nil-params check comes
	// first: params can be nil per the core.Model contract.
	if params != nil && len(params.FunctionTools) > 0 {
		if tc, ok := m.client.(ToolCaller); ok {
			return m.requestWithTools(ctx, tc, system, prompt, params)
		}
	}

	raw, usage, err := generateFull(ctx, m.client, system, prompt)
	if err != nil {
		return nil, fmt.Errorf("gollem model %q: %w", m.ModelName(), err)
	}

	return toModelResponse(raw, usage, params), nil
}

// requestWithTools translates a native function-tool request into a
// ToolCaller.CallTools invocation and maps its result back into the
// core.ModelResponse shape the Agent expects.
//
// Only Name/Description cross the llm seam (ToolSpec); the Agent's own
// registered function tools populated params.FunctionTools, so the ToolName the
// model returns round-trips exactly and lands in the Agent's functionCalls
// (agent.go's outputCalls/functionCalls/unknownCalls split). A name the Agent
// never registered is routed to unknownCalls (a retry), never executed.
//
// Usage is left zero-valued: llm.ToolCallResult carries no token counts (a
// deliberate contract limit — see the gollemModelPricing comment), so the cost
// tracker records 0 for native tool-calling turns. The unchanged
// generateFull/toModelResponse path still populates Usage.
func (m *GollemModel) requestWithTools(
	ctx context.Context,
	tc ToolCaller,
	system, prompt string,
	params *core.ModelRequestParameters,
) (*core.ModelResponse, error) {
	specs := make([]ToolSpec, len(params.FunctionTools))
	for i, td := range params.FunctionTools {
		specs[i] = ToolSpec{Name: td.Name, Description: td.Description}
		// Carry the tool's argument schema through to the provider so native
		// tool-calling advertises the real shape (ADR-0016 lever #1). The Agent's
		// core.ToolDefinition holds it as a core.Schema (map[string]any); marshal
		// it back to the JSON bytes ToolSpec.Parameters carries. A tool with no
		// declared schema leaves Parameters nil (permissive object downstream).
		if len(td.ParametersSchema) > 0 {
			if b, merr := json.Marshal(td.ParametersSchema); merr == nil {
				specs[i].Parameters = b
			}
		}
	}

	res, err := m.callToolsMaybeReasoning(ctx, tc, system, prompt, specs)
	if err != nil {
		return nil, fmt.Errorf("gollem model %q (tool calling): %w", m.ModelName(), err)
	}

	// Zero calls => terminal free-text answer.
	if len(res.Calls) == 0 {
		return core.TextResponse(res.Message), nil
	}

	// One core.ToolCallPart (a value, not a pointer — ModelResponse.ToolCalls()
	// type-asserts by value) per chosen call.
	parts := make([]core.ModelResponsePart, 0, len(res.Calls))
	for i, call := range res.Calls {
		argsJSON := []byte("{}")
		switch {
		case len(call.RawArgs) > 0:
			// Prefer the model's exact JSON so nested/typed args survive to the
			// tool handler (ADR-0016 lever #1); the flattened Args map would drop
			// structure.
			argsJSON = call.RawArgs
		case call.Args != nil: // json.Marshal(nil map) yields "null", not "{}"
			b, merr := json.Marshal(call.Args)
			if merr != nil {
				return nil, fmt.Errorf("gollem model %q: marshal args for tool %q: %w", m.ModelName(), call.Name, merr)
			}
			argsJSON = b
		}
		parts = append(parts, core.ToolCallPart{
			ToolName:   call.Name,
			ArgsJSON:   string(argsJSON),
			ToolCallID: fmt.Sprintf("call_%s_%d", call.Name, i), // synthesized, unique per call
		})
	}

	return &core.ModelResponse{
		Parts:        parts,
		ModelName:    m.ModelName(), // real provider name (NOT "test-model")
		FinishReason: core.FinishReasonToolCall,
		Timestamp:    time.Now(),
	}, nil
}

// callToolsMaybeReasoning routes the tool call through the provider's reasoning
// carry-forward path (CallToolsReasoning) when BOTH a request-local reasoning
// accumulator is present (RunToolAgent set one) AND the client implements
// ReasoningToolCaller — reinjecting the prior reasoning and capturing the fresh
// reasoning back into the accumulator. Otherwise it falls back to plain
// CallTools, byte-for-byte the pre-reasoning behavior. The accumulator is
// updated in place; because it is stack-local to one RunToolAgent run, this is
// tenant-safe by construction (no shared/keyed state).
func (m *GollemModel) callToolsMaybeReasoning(ctx context.Context, tc ToolCaller, system, prompt string, specs []ToolSpec) (ToolCallResult, error) {
	if m.reasoningAcc != nil {
		if rtc, ok := tc.(ReasoningToolCaller); ok {
			res, next, err := rtc.CallToolsReasoning(ctx, system, prompt, specs, m.reasoningAcc.Clone())
			if err != nil {
				return ToolCallResult{}, err
			}
			*m.reasoningAcc = next
			return res, nil
		}
	}
	return tc.CallTools(ctx, system, prompt, specs)
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
	usage  *Usage
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
			if tok.Usage != nil {
				s.usage = tok.Usage
			}
			return core.PartEndEvent{Index: 0}, nil
		}
		s.buf.WriteString(tok.Text)
		return core.PartDeltaEvent{Index: 0, Delta: core.TextPartDelta{ContentDelta: tok.Text}}, nil
	default:
		return nil, io.EOF
	}
}

func (s *gollemStream) Response() *core.ModelResponse {
	return toModelResponse(s.buf.String(), s.usage, s.params)
}

// Usage returns the real provider-reported token counts captured from the
// stream's terminal Token, when the underlying Client supplied one — zero
// value otherwise (never derived from a delta count).
func (s *gollemStream) Usage() core.Usage {
	if s.usage == nil {
		return core.Usage{}
	}
	u := core.Usage{InputTokens: s.usage.InputTokens, OutputTokens: s.usage.OutputTokens}
	// Reasoning tokens travel in gollem's Details["reasoning_tokens"] convention
	// (same mapping as toModelResponse below), so the streaming path does not
	// drop them either.
	if s.usage.ReasoningTokens > 0 {
		u.Details = map[string]int{"reasoning_tokens": s.usage.ReasoningTokens}
	}
	return u
}

func (s *gollemStream) Close() error { s.state = gollemStreamDone; return nil }

// generateFull drains the Client's streaming channel and returns the full
// response as a single string, plus the real Usage captured from the
// terminal Token when the Client supplied one (nil otherwise) — Gollem
// (Agent.Run, via Model.Request) needs the full response to be able to
// parse/validate against T before deciding whether to retry, and the real
// usage is what lets GollemModel populate core.ModelResponse.Usage (see
// toModelResponse) so CostTracker records non-zero cost.
func generateFull(ctx context.Context, client Client, system, prompt string) (string, *Usage, error) {
	tokens, err := client.StreamChat(ctx, system, prompt)
	if err != nil {
		return "", nil, err
	}

	var b strings.Builder
	for {
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case tok, ok := <-tokens:
			if tok.Err != nil {
				return "", nil, tok.Err
			}
			if !ok || tok.Done {
				return b.String(), tok.Usage, nil
			}
			b.WriteString(tok.Text)
		}
	}
}

// buildGollemPrompt flattens Gollem's ModelMessage history (system prompt,
// user prompt, feedback from previous retries, previous model responses)
// into a single (system, prompt) pair — what Client.StreamChat accepts.
//
// ADR-0068 (A1) — schema rider lives in the SYSTEM region, never the tail.
// The JSON-Schema "respond ONLY with JSON" instruction (outputInstruction) is
// appended to the SYSTEM prompt, NOT after the last user message. Previously it
// was written to the body as the FINAL line — so on the qa/chat route (which
// forces a struct T through the reflection loop) it was the last thing the model
// read, AFTER a possibly-adversarial user turn. That trailing formatting
// directive suppressed refusal/decline language: it is the code-grounded
// mechanism behind live board 06945bf's safeguard failures (sg-identity-model
// leaked the model/vendor name; sg-jailbreak-persona complied with a persona
// override). Relocating it to the system region keeps the user's turn as the
// FINAL content block, so the model's safety behaviour is not overwritten by a
// trailing schema instruction. The schema TEXT is unchanged, so gollem's own
// reflection retry (which resends a RetryPromptPart into the body — see below)
// still references the identical schema; retry semantics are preserved.
//
// Placement note: the SystemPromptPart already carries the fully assembled
// system prompt (persona, memory-recency policy, and — only when memory/RAG
// hydration produced content — the untrusted-context fence, built by
// internal/api.hydrateSystemPrompt). buildGollemPrompt receives that as one
// opaque string and can only APPEND, so the rider lands at the very end of the
// system region: right after the persona when there is no fence (the safeguard
// probes hydrate nothing, so this is their case), or just after the fence close
// otherwise — in both cases inside the system message and BEFORE the user turn,
// which is the property that fixes the suppression. Placing it strictly above
// the fence would require internal/api.hydrateSystemPrompt to inject it during
// assembly; that is recorded as the ADR-0068 follow-up (native structured
// output via the ToolCaller seam removes the prompt-borne rider entirely).
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

	// ADR-0068 (A1): rider into the SYSTEM region, not appended after the user
	// turn. See the function doc for the full rationale.
	if instr := outputInstruction(params); instr != "" {
		writeLine(&sys, instr)
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
//
// When usage is non-nil (the Client supplied real provider-reported token
// counts on its terminal Token), it populates ModelResponse.Usage so
// core.CostTracker (attached via gollemGuardrailOptions) records real,
// non-zero cost instead of always seeing a zero core.Usage.
func toModelResponse(raw string, usage *Usage, params *core.ModelRequestParameters) *core.ModelResponse {
	raw = strings.TrimSpace(raw)

	var resp *core.ModelResponse
	if tool := primaryOutputTool(params); tool != nil {
		resp = core.ToolCallResponse(tool.Name, extractJSONObject(raw))
	} else {
		resp = core.TextResponse(raw)
	}

	if usage != nil {
		resp.Usage = core.Usage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens}
		// Carry reasoning tokens (ADR-0061) through gollem's own convention —
		// core.Usage.Details["reasoning_tokens"] — so they are not dropped when a
		// gollem-side consumer (e.g. a CostTracker) reads ModelResponse.Usage.
		// gollem-exploitation audit (control-gates slice) flagged this drop.
		if usage.ReasoningTokens > 0 {
			resp.Usage.Details = map[string]int{"reasoning_tokens": usage.ReasoningTokens}
		}
	}

	return resp
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
// GollemModel.toModelResponse now DOES populate core.ModelResponse.Usage
// whenever the underlying llm.Client supplies real token counts on its
// terminal Token (see llm.go's Usage, generateFull, and each Client
// implementation: AnthropicClient reads message_start/message_delta usage,
// OpenAICompatClient requests stream_options.include_usage, StubClient emits
// a deterministic fake Usage). So tracker.Record sees real (non-zero, when
// pricing is configured) core.Usage for traffic through GollemModel — the
// remaining gap is only that this map ships empty (no $/token entries), a
// separate, deliberate decision (see the paragraph above), not a Usage
// plumbing gap.
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
// logs the tracker's recorded cost/usage at slog debug. The cost is real
// whenever the underlying Client reports usage AND gollemModelPricing has an
// entry for its model name (see that variable's comment) — it is 0 only when
// the model name is unpriced, an intentional accounting degrade, not a Usage
// plumbing gap.
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
