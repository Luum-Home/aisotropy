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
type Decision struct {
	Tool string            `json:"tool"`
	Args map[string]string `json:"args"`
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
		return Decision{Tool: ""}, nil
	}

	if _, ok := permittedSet[dec.Tool]; !ok {
		// SECURITY INVARIANT: the model named a tool it was never offered
		// (either it hallucinated, or something tried to coerce it into
		// naming a tool outside the caller's scopes). Never let that
		// surface as a usable Decision — treat it exactly like "no tool".
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
	for range l.MaxSteps {
		dec, err := l.SelectTool(ctx, scopes, current)
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
		`{"tool": "<tool name or empty string>", "args": {"<key>": "<value>"}}` +
		"\n\nIf none of the listed tools help, or no tools are available, set " +
		`"tool" to "" and "args" to {}. Never name a tool that was not ` +
		"explicitly listed above.")
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
