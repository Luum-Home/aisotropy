// tool_stub.go — an ADAPTER-LOCAL, offline, tool-capable stub llm.Client for
// ARENA_STUB=1 runs of the `tools` and `structured` modes ONLY.
//
// WHY THIS EXISTS: aisotropy's own internal/llm.StubClient — the fallback
// llm.FromEnv() resolves to when every provider key is unset, which is the
// ARENA_STUB=1 offline path every other mode in this adapter already relies on
// (see newAgnesClient/app.go's nil-agnes fallback) — does NOT implement the
// optional llm.ToolCaller capability (internal/llm/tools.go). The
// POST /chat/agent route (internal/api/chat_agent.go's RegisterAgent) therefore
// falls back, for a plain StreamChat-only client, to the homegrown
// structured-output agentloop (internal/agentloop), which asks the model to
// reply with a bare `{"tool":...,"args":{...}}` JSON object as raw chat text.
// StubClient's only structured-output recognition is keyed to GollemReflect's
// distinct schema marker ("Respond ONLY with a valid JSON object that exactly
// satisfies" — internal/llm/stub.go's schemaInstructionMarker), which
// agentloop's system prompt never emits, so StubClient falls through to its
// default free-text echo — non-JSON as far as agentloop's parseDecision is
// concerned — and the route 500s (agentloop.ErrMalformedDecision), taking
// `tools`/`structured` stub-mode (native+docker) down with it.
//
// THE FIX is adapter-local, not a project-wide internal/llm.StubClient change:
// that type is shared by many other tests/paths having nothing to do with this
// arena (see .claude/rules/prune-evidence-gate.md's code-truth discipline —
// only THIS adapter's two agent-route modes are broken, so only THIS adapter's
// stub wiring changes). toolStub implements aisotropy.LLMClient (StreamChat +
// Name) AND the optional aisotropy.ToolCaller capability (CallTools, re-exported
// from internal/llm.ToolCaller via aisotropy/graph.go). Once
// runNativeToolAgent's `client.(llm.ToolCaller)` type assertion
// (internal/api/chat_agent.go) succeeds, the agent route SKIPS agentloop
// entirely and drives internal/llm.RunToolAgent's NATIVE tool-calling path
// (internal/llm/toolagent.go) — exactly the seam every sibling framework's own
// stub already exercises for its tools/structured modes (see
// arenas/live/eino/stub.go's stubChatModel.toolCallMessage, arenas/live/
// langgraph/modes/common.py's FakeListChatModel subclass, arenas/live/mastra's
// lib/stub-model.mjs) — this brings aisotropy's stub to parity, not a bespoke
// shortcut.
//
// Tool DECISIONS are canned (deterministic, no real reasoning) exactly like
// every sibling adapter's stub — the tool itself (calcExecutor /
// structuredExecutor in mode_tools.go / mode_structured.go) always genuinely
// executes; only the choice to call it, and its arguments, are synthesized
// here. Unlike the sibling stubs — which use ONE fixed canned expression
// regardless of the actual question (see eino/stub.go's stubToolCallArgs,
// langgraph's common.py's literal "(482 * 17) + 391") — this stub DERIVES a
// genuinely-evaluable arithmetic expression from the user's actual question
// (deriveExpression, below) when it can, falling back to a fixed expression
// only when extraction fails. That lets the runner's numeric correctness check
// (numberPresent, arenas/live/runner/eval.go) genuinely pass under the stub
// too, not merely "a tool executed" — a strictly higher, still fully offline,
// bar than the sibling adapters hold themselves to.
package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

// compile-time proof toolStub satisfies both the plain streaming contract and
// the optional native tool-calling capability the agent route looks for.
var (
	_ aisotropy.LLMClient  = toolStub{}
	_ aisotropy.ToolCaller = toolStub{}
)

// toolStub is the ARENA_STUB=1 tool-capable client for the `tools` and
// `structured` modes only (see the newAgnesClient call sites in
// mode_tools.go / mode_structured.go). It never makes a network call — every
// decision is a pure function of its input.
type toolStub struct{}

func (toolStub) Name() string { return "aisotropy-stub-toolcaller" }

// StreamChat is the plain-text fallback path: Gollem's GollemModel.Request
// (internal/llm/gollem.go) only calls it when a turn offers NO function tools,
// which never happens on the `tools`/`structured` agent-route turns this stub
// is wired into (CallTools intercepts those first, whenever
// params.FunctionTools is non-empty). Implemented anyway for full
// aisotropy.LLMClient compliance — a deterministic, clearly-labeled single-shot
// echo, never a panic or an "unimplemented" stub.
func (toolStub) StreamChat(ctx context.Context, system, prompt string) (<-chan aisotropy.Token, error) {
	reply := fmt.Sprintf("[aisotropy-tool-stub echo] %s", strings.TrimSpace(prompt))
	usage := aisotropy.Usage{
		InputTokens:  len(strings.Fields(system)) + len(strings.Fields(prompt)),
		OutputTokens: len(strings.Fields(reply)),
	}
	ch := make(chan aisotropy.Token, 2)
	go func() {
		defer close(ch)
		select {
		case ch <- aisotropy.Token{Text: reply}:
		case <-ctx.Done():
			return
		}
		select {
		case ch <- aisotropy.Token{Done: true, Usage: &usage}:
		case <-ctx.Done():
		}
	}()
	return ch, nil
}

// toolResultMarker mirrors buildGollemPrompt's ToolReturnPart flattening
// (internal/llm/gollem.go: `"[previous result from %s] %v"`) — its presence in
// prompt is how CallTools tells "first decision turn" (no marker yet) apart
// from "the tool already ran, now answer" (marker present), across the two (or
// more) CallTools invocations one agent run makes.
const toolResultMarker = "[previous result from "

// CallTools is the native tool-calling seam runNativeToolAgent drives
// (internal/api/chat_agent.go -> internal/llm.RunToolAgent ->
// GollemModel.requestWithTools -> here). It is invoked once per agent turn:
//   - the FIRST turn (prompt carries no prior tool result yet) canned-decides
//     to call the one offered tool this arena ever registers for a stub run
//     (`calculate` in `tools` mode, `emit_structured_answer` in `structured`
//     mode) with a genuinely-evaluable argument set;
//   - every SUBSEQUENT turn (prompt now contains toolResultMarker) returns
//     zero Calls so the agent converges on a terminal answer — mirroring
//     exactly what a real model does once it already has the tool's result.
func (toolStub) CallTools(ctx context.Context, system, prompt string, tools []aisotropy.ToolSpec) (aisotropy.ToolCallResult, error) {
	if len(tools) == 0 {
		return aisotropy.ToolCallResult{Message: "no tools offered"}, nil
	}
	if strings.Contains(prompt, toolResultMarker) {
		// A tool already executed this run; answer directly (terminal state) —
		// mode_tools.go / mode_structured.go both read the tool's RESULT off
		// AgentStep, not this free-text Message, so its exact content is not
		// load-bearing beyond being non-empty.
		return aisotropy.ToolCallResult{Message: "done"}, nil
	}

	target := tools[0]
	for _, t := range tools {
		if t.Name == "calculate" || t.Name == "emit_structured_answer" {
			target = t
			break
		}
	}

	return aisotropy.ToolCallResult{
		Calls: []aisotropy.ToolCall{{Name: target.Name, Args: cannedToolArgs(target.Name, prompt)}},
	}, nil
}

// cannedToolArgs builds deterministic, genuinely-evaluable arguments for the
// two tools this adapter's stub-mode agent route ever offers. An unrecognized
// tool name (should not occur — the arena only registers these two for
// stub-mode agent-route runs) gets an empty argument object rather than a
// panic, so a future third tool degrades gracefully instead of crashing.
func cannedToolArgs(name, prompt string) map[string]string {
	switch name {
	case "calculate":
		return map[string]string{"expression": deriveExpression(prompt)}
	case "emit_structured_answer":
		return map[string]string{
			"answer":     cannedStructuredAnswer(prompt),
			"confidence": "medium",
		}
	default:
		return map[string]string{}
	}
}

// wordOperator replaces one English arithmetic phrase with its symbol,
// case-insensitively. Longer/more-specific phrases are listed first so e.g.
// "multiplied by" is consumed whole before a shorter, unrelated match could
// interfere.
type wordOperator struct {
	re     *regexp.Regexp
	symbol string
}

var wordOperators = []wordOperator{
	{regexp.MustCompile(`(?i)multiplied\s+by`), "*"},
	{regexp.MustCompile(`(?i)divided\s+by`), "/"},
	{regexp.MustCompile(`(?i)\btimes\b`), "*"},
	{regexp.MustCompile(`(?i)\bplus\b`), "+"},
	{regexp.MustCompile(`(?i)\bminus\b`), "-"},
}

// exprTokenRe pulls out the arithmetic-expression-shaped tokens (numbers,
// parens, + - * /) from arbitrary surrounding prose, after wordOperators has
// already turned English arithmetic phrases into their symbols.
var exprTokenRe = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?|[()+\-*/]`)

// fallbackExpression is used whenever the prompt does not contain a
// recognizable, evaluable arithmetic expression (e.g. a non-arithmetic probe
// hitting `tools` mode) — a fixed, always-valid expression so the tool still
// genuinely executes and returns a real number rather than erroring.
const fallbackExpression = "6 * 7"

// deriveExpression extracts a genuinely-evaluable arithmetic expression from a
// natural-language arithmetic question (e.g. "What is 47 multiplied by 89? Use
// the calculator tool." -> "47*89"). It recognizes the plain symbols
// (+ - * / parens) and the small set of English operator words this arena's
// built-in task set uses (arenas/live/runner/tasks.go's arithmeticTasks).
// Falls back to fallbackExpression when nothing evaluable is found, so the
// `calculate` tool this feeds always receives something it can compute — the
// tool is never handed empty/malformed input by this stub.
func deriveExpression(prompt string) string {
	text := prompt
	for _, w := range wordOperators {
		text = w.re.ReplaceAllString(text, " "+w.symbol+" ")
	}
	toks := exprTokenRe.FindAllString(text, -1)
	if len(toks) == 0 {
		return fallbackExpression
	}
	expr := strings.Join(toks, "")
	if _, err := evalArithmetic(expr); err != nil {
		return fallbackExpression
	}
	return expr
}

// structuredInstructionMarker is the exact trailing instruction runStructured
// (mode_structured.go) appends to the user's question before it reaches the
// agent route. cannedStructuredAnswer strips it so the canned answer echoes
// only the actual question, not the instruction text.
const structuredInstructionMarker = "\n\nAnswer by calling emit_structured_answer"

// cannedStructuredAnswer builds a deterministic, always non-empty answer
// string derived from the actual question — clearly labeled synthetic (never
// confusable with a real agnes-2.0-flash answer), matching the sibling
// adapters' stub-labeling convention (e.g. eino's stubEchoPrefix).
func cannedStructuredAnswer(prompt string) string {
	q := prompt
	if i := strings.Index(q, structuredInstructionMarker); i >= 0 {
		q = q[:i]
	}
	q = strings.TrimSpace(q)
	if q == "" {
		q = "(no question)"
	}
	return fmt.Sprintf("[ARENA_STUB] canned structured answer for: %q", q)
}
