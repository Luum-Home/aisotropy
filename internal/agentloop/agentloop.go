// Package agentloop implements scope-safe "tool calling" for text-streaming
// LLM clients that have no native function-calling API (internal/llm.Client
// is a single StreamChat method — see internal/llm/llm.go). Real tool use is
// achieved through structured output instead: the model is asked to reply
// with a small JSON Decision naming, at most, one of the tools it was
// explicitly offered.
//
// The security invariant this package exists to enforce is simple to state
// and easy to get wrong: a tool the caller's scopes did not permit must
// never run, even if the model's raw text output names it (hallucination,
// prompt injection, or an adversarial completion). agentloop enforces this
// twice — once by never mentioning non-permitted tools in the system prompt
// (internal/tools.Registry.Permitted/PromptFragment already do this), and
// again by re-checking the model's chosen tool name against the permitted
// set before returning a Decision from SelectTool. There is no path from "not
// in scopes" to "executed".
package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// ToolExecutor executes a tool by name with string-keyed arguments and
// returns its textual result. The host application supplies the concrete
// implementation (dispatching to whatever the tool actually does);
// agentloop never executes tool logic itself, only decides — safely —
// which tool, if any, the model may invoke.
type ToolExecutor interface {
	Execute(ctx context.Context, name string, args map[string]string) (string, error)
}

// Decision is the structured-output contract the model must reply with: at
// most one tool name (exactly as listed in the system prompt) plus its
// arguments, or an empty Tool meaning "answer directly, no tool needed".
//
// Answer carries the model's natural-language reply to the user and is only
// meaningful on the terminating no-tool Decision (Tool == ""): it is how a
// StreamChat-only client — which has no native final-text channel like a
// ToolCaller does — conveys the answer it synthesized after (or instead of)
// any tool calls. The field is ADDITIVE and backward-compatible: a model that
// still emits only {"tool":"","args":{}} decodes with Answer == "", exactly as
// before this field existed. On a tool-call Decision the model is instructed to
// leave Answer empty.
type Decision struct {
	Tool   string            `json:"tool"`
	Args   map[string]string `json:"args"`
	Answer string            `json:"answer"`
}

// ErrMalformedDecision is returned (wrapped, via errors.Is) by SelectTool
// when the model's raw output does not contain a JSON object parseable as a
// Decision. Callers should treat this the same as "no tool selected" for
// the purpose of continuing the conversation — the exact recovery strategy
// (retry, fall back to a tool-free answer, surface to the user) is a host
// decision, not agentloop's.
var ErrMalformedDecision = errors.New("agentloop: model output did not contain a valid Decision JSON object")

// Loop drives structured-output tool-selection turns against an llm.Client,
// restricting the model to the tools the caller's scopes permit.
type Loop struct {
	// Client is the LLM used to ask for a Decision.
	Client llm.Client
	// Registry is the full tool catalog; only the subset Permitted by the
	// caller's scopes is ever placed in front of the model.
	Registry *tools.Registry
	// Executor runs the tool a Decision selects.
	Executor ToolExecutor
	// MaxSteps bounds Run's tool-call loop (SelectTool -> execute -> feed
	// result back -> SelectTool again). SelectTool itself is a single turn
	// and ignores MaxSteps.
	MaxSteps int
}

// SelectTool asks the model, via structured output, to pick one
// scope-permitted tool (or none) for prompt. It never returns a Decision
// whose Tool is outside the set Registry.Permitted(scopes) computes — a
// Tool name that is not in that set (including malicious or hallucinated
// names) is silently downgraded to the no-tool Decision{} rather than
// propagated, so callers cannot accidentally execute it by trusting the
// returned Decision.
//
// SelectTool returns an error (wrapping ErrMalformedDecision) if the
// model's raw output cannot be parsed as Decision JSON at all, and any
// error from the underlying stream (including context cancellation).
func (l *Loop) SelectTool(ctx context.Context, scopes []string, prompt string) (Decision, error) {
	permitted := l.Registry.Permitted(scopes)
	permittedSet := make(map[string]struct{}, len(permitted))
	for _, t := range permitted {
		permittedSet[t.Name] = struct{}{}
	}

	system := buildSystemPrompt(l.Registry, scopes)

	raw, err := collectText(ctx, l.Client, system, prompt)
	if err != nil {
		return Decision{}, err
	}

	dec, err := parseDecision(raw)
	if err != nil {
		return Decision{}, fmt.Errorf("%w: %w", ErrMalformedDecision, err)
	}

	if dec.Tool == "" {
		// Preserve the model's natural-language answer (Answer) while forcing the
		// no-tool shape — this is the terminating direct-answer Decision.
		return Decision{Tool: "", Answer: dec.Answer}, nil
	}

	if _, ok := permittedSet[dec.Tool]; !ok {
		// SECURITY INVARIANT: the model named a tool it was never offered
		// (either it hallucinated, or something tried to coerce it into
		// naming a tool outside the caller's scopes). Never let that
		// surface as a usable Decision — treat it exactly like "no tool".
		// Any Answer the model attached to a rejected tool call is dropped:
		// it never chose the no-tool path, so there is no trusted direct answer.
		return Decision{Tool: ""}, nil
	}

	return dec, nil
}

// StepResult records one iteration of Run: the Decision the model made and,
// when a tool was actually selected and executed, its textual result.
type StepResult struct {
	Decision Decision
	Result   string
}

// Run drives up to l.MaxSteps structured-output turns. Each step calls
// SelectTool; if the model answered directly (Decision.Tool == "", whether
// by genuine choice or because a non-permitted tool was rejected), Run
// stops and returns the steps taken so far. Otherwise Run executes the
// chosen tool via l.Executor, appends the tool's result to the running
// prompt so the next SelectTool call can see it, and continues.
//
// Run stops early and returns an error if SelectTool or Executor.Execute
// fails; the steps completed before the failure are still returned.
func (l *Loop) Run(ctx context.Context, scopes []string, prompt string) ([]StepResult, error) {
	var steps []StepResult
	current := prompt
	// reasoning is the REQUEST-LOCAL reasoning carry-forward (ADR-0061): a value
	// on THIS goroutine's stack, never a shared/keyed map, so two tenants' Run
	// calls cannot share a reasoning byte. It is captured from each step's stream
	// and reinjected as leading context into the next step's prompt — stateless
	// text reinjection, acceptable for these StreamChat-only (unsigned) clients.
	// It is empty and inert on non-reasoning endpoints (stub/agnes emit none), so
	// the loop behaves byte-for-byte as before when reasoning is off.
	var reasoning llm.ReasoningState
	for range l.MaxSteps {
		dec, err := l.selectToolCapturingReasoning(ctx, scopes, current, &reasoning)
		if err != nil {
			return steps, err
		}
		if dec.Tool == "" {
			steps = append(steps, StepResult{Decision: dec})
			return steps, nil
		}

		result, err := l.Executor.Execute(ctx, dec.Tool, dec.Args)
		if err != nil {
			steps = append(steps, StepResult{Decision: dec})
			return steps, fmt.Errorf("agentloop: executing tool %q: %w", dec.Tool, err)
		}

		steps = append(steps, StepResult{Decision: dec, Result: result})
		current = fmt.Sprintf("%s\n\nTool %q returned:\n%s\n\nContinue.", current, dec.Tool, result)
	}
	return steps, nil
}

// FinalAnswer returns the model's natural-language answer from a completed Run:
// the Answer carried by the terminating no-tool Decision (Tool == ""), which Run
// always records as the LAST step when the model chose to answer directly. It is
// "" when the run stopped without a direct answer — e.g. it exhausted MaxSteps
// while still calling tools (a graceful non-converged stop, mirroring the native
// RunToolAgentWithOutput contract), or the model emitted no answer text. This is
// the fallback-path analog of the native agent's final free-text output.
func FinalAnswer(steps []StepResult) string {
	if n := len(steps); n > 0 && steps[n-1].Decision.Tool == "" {
		return steps[n-1].Decision.Answer
	}
	return ""
}

// selectToolCapturingReasoning is SelectTool plus reasoning carry-forward: it
// reinjects the accumulated reasoning as leading prompt context, runs the same
// scope-safe single-turn selection, and folds this turn's reasoning back into
// *reasoning for the next step. It shares SelectTool's security guarantees
// exactly (a non-permitted tool is downgraded to the no-tool Decision). *reasoning
// stays empty when the client streams no reasoning (the default), making this a
// no-op wrapper over SelectTool.
func (l *Loop) selectToolCapturingReasoning(ctx context.Context, scopes []string, prompt string, reasoning *llm.ReasoningState) (Decision, error) {
	permitted := l.Registry.Permitted(scopes)
	permittedSet := make(map[string]struct{}, len(permitted))
	for _, t := range permitted {
		permittedSet[t.Name] = struct{}{}
	}

	system := buildSystemPrompt(l.Registry, scopes)
	effectivePrompt := reinjectReasoning(prompt, *reasoning)

	raw, captured, err := collectTextReasoning(ctx, l.Client, system, effectivePrompt)
	if err != nil {
		return Decision{}, err
	}
	*reasoning = captured

	dec, err := parseDecision(raw)
	if err != nil {
		return Decision{}, fmt.Errorf("%w: %w", ErrMalformedDecision, err)
	}
	if dec.Tool == "" {
		// Preserve the model's natural-language answer (Answer) on the terminating
		// direct-answer Decision; everything else is forced to the no-tool shape.
		return Decision{Tool: "", Answer: dec.Answer}, nil
	}
	if _, ok := permittedSet[dec.Tool]; !ok {
		// SECURITY INVARIANT (identical to SelectTool): a tool the caller's scopes
		// did not permit is never surfaced as a usable Decision. Any Answer attached
		// to a rejected tool call is dropped — the model never chose to answer.
		return Decision{Tool: ""}, nil
	}
	return dec, nil
}

// reinjectReasoning prepends accumulated reasoning text to prompt as clearly
// labelled leading context. Empty reasoning returns prompt unchanged (the
// no-reasoning / reasoning-off path).
func reinjectReasoning(prompt string, reasoning llm.ReasoningState) string {
	text := reasoning.ReasoningText()
	if text == "" {
		return prompt
	}
	return "[prior reasoning]\n" + text + "\n\n" + prompt
}

// buildSystemPrompt renders the scope-permitted tool list (via
// tools.Registry.PromptFragment, which already omits anything the scopes do
// not grant) plus the JSON-only response contract the model must follow.
func buildSystemPrompt(reg *tools.Registry, scopes []string) string {
	fragment := reg.PromptFragment(scopes)

	var sb strings.Builder
	sb.WriteString("You are a tool-selection assistant.")
	if fragment == "" {
		sb.WriteString(" You have no tools available for this request.")
	} else {
		sb.WriteString(" ")
		sb.WriteString(fragment)
	}
	sb.WriteString("\n\nRespond with EXACTLY ONE JSON object and nothing else " +
		"(no prose, no markdown code fences), matching this shape:\n" +
		`{"tool": "<tool name or empty string>", "args": {"<key>": "<value>"}, "answer": "<natural-language answer or empty string>"}` +
		"\n\nIf none of the listed tools help, or no tools are available, set " +
		`"tool" to "" and "args" to {}, and put your natural-language answer to ` +
		`the user in "answer". When you DO call a tool, set "answer" to "" (you ` +
		"will get the tool result and can answer on a later turn). Never name a " +
		"tool that was not explicitly listed above.")
	return sb.String()
}

// collectText drains client.StreamChat into a single string, stopping (and
// returning what was collected so far, plus the error) on the first
// mid-stream Err, on ctx cancellation, or when the stream reports Done.
func collectText(ctx context.Context, client llm.Client, system, prompt string) (string, error) {
	ch, err := client.StreamChat(ctx, system, prompt)
	if err != nil {
		return "", fmt.Errorf("agentloop: starting stream: %w", err)
	}

	var sb strings.Builder
	for {
		select {
		case <-ctx.Done():
			return sb.String(), ctx.Err()
		case tok, ok := <-ch:
			if !ok {
				return sb.String(), nil
			}
			sb.WriteString(tok.Text)
			if tok.Err != nil {
				return sb.String(), fmt.Errorf("agentloop: stream error: %w", tok.Err)
			}
			if tok.Done {
				return sb.String(), nil
			}
		}
	}
}

// collectTextReasoning drains client.StreamChat like collectText, but keeps the
// ANSWER text and the REASONING deltas in SEPARATE channels (ADR-0061): tok.Text
// accumulates into the returned raw answer, tok.Reasoning into a request-local
// ReasoningState — so chain-of-thought never contaminates the Decision JSON the
// model is parsed against. The returned ReasoningState is empty when the stream
// carried no reasoning (the default / non-reasoning endpoints).
func collectTextReasoning(ctx context.Context, client llm.Client, system, prompt string) (string, llm.ReasoningState, error) {
	ch, err := client.StreamChat(ctx, system, prompt)
	if err != nil {
		return "", llm.ReasoningState{}, fmt.Errorf("agentloop: starting stream: %w", err)
	}

	var text, reasoning strings.Builder
	for {
		select {
		case <-ctx.Done():
			return text.String(), llm.NewTextReasoningState(reasoning.String()), ctx.Err()
		case tok, ok := <-ch:
			if !ok {
				return text.String(), llm.NewTextReasoningState(reasoning.String()), nil
			}
			text.WriteString(tok.Text)
			reasoning.WriteString(tok.Reasoning)
			if tok.Err != nil {
				return text.String(), llm.NewTextReasoningState(reasoning.String()), fmt.Errorf("agentloop: stream error: %w", tok.Err)
			}
			if tok.Done {
				return text.String(), llm.NewTextReasoningState(reasoning.String()), nil
			}
		}
	}
}

// parseDecision extracts and decodes a Decision from the model's raw text,
// tolerating surrounding prose and markdown code fences. It scans every
// brace-balanced JSON object in the text and returns the first that decodes
// into a Decision with a concrete tool; failing that, the first that decodes
// at all (a valid "no tool" reply). A naive first-'{'/last-'}' slice would
// instead mis-capture stray braces in prose (e.g. "use {the tool}: {json}")
// and drop a legitimate tool call — a denial-of-tool.
func parseDecision(raw string) (Decision, error) {
	text := stripCodeFences(strings.TrimSpace(raw))
	var fallback *Decision
	for _, obj := range balancedJSONObjects(text) {
		var dec Decision
		if err := json.Unmarshal([]byte(obj), &dec); err != nil {
			continue // not JSON (prose braces) — try the next candidate
		}
		if strings.TrimSpace(dec.Tool) != "" {
			return dec, nil // a concrete tool choice wins immediately
		}
		if fallback == nil {
			d := dec
			fallback = &d // remember the first valid "no tool" object
		}
	}
	if fallback != nil {
		return *fallback, nil
	}
	return Decision{}, fmt.Errorf("no valid Decision JSON object in model output: %q", truncate(text, 200))
}

// balancedJSONObjects returns each top-level brace-balanced {...} substring in
// s, honoring string literals and escapes so braces inside strings do not
// count. This is what lets parseDecision tolerate prose or stray braces around
// the real object instead of mis-slicing on the first '{' and last '}'.
func balancedJSONObjects(s string) []string {
	var objs []string
	depth, start := 0, -1
	inStr, esc := false, false
	for i := range len(s) {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					objs = append(objs, s[start:i+1])
					start = -1
				}
			}
		}
	}
	return objs
}

// stripCodeFences removes a single leading/trailing ``` or ```json fence, if
// present, so parseDecision can find the JSON body inside it.
func stripCodeFences(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// truncate shortens s to at most n bytes for error messages, marking
// truncation with a trailing ellipsis.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
