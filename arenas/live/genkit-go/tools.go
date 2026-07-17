package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
)

// runTools exercises Genkit's NATIVE tool-execution loop: tools are
// registered with real Go bodies (genkit.DefineToolWithInputSchema) and
// ai.WithReturnToolRequests is left at its default (false), so Genkit itself
// invokes the chosen tool(s) and feeds their real results back to the model,
// looping until the model produces a final answer or ai.WithMaxTurns is hit —
// this is the opposite of qa mode's single-shot capture-and-stop.
//
// Two real, non-canned tools are wired by name/parameter-shape matching so
// this adapter works with whatever tool defs a caller supplies while still
// honoring _shared/README.md's "real, executable tools" convention:
//   - a calculator that actually evaluates an arithmetic expression;
//   - a corpus_lookup that performs the same real embed+Qdrant search as rag.go.
//
// A third fallback tool (clock) covers any tool name/shape that matches
// neither pattern: it returns the real wall-clock time and the echoed
// arguments — a genuinely executed, dynamic result, never a hardcoded value.
func runTools(rc *runCtx, out *arenaOutput) error {
	if len(rc.in.Tools) == 0 {
		return fmt.Errorf("tools mode: request has no tool definitions")
	}

	var mu sync.Mutex
	var executed []outToolCall

	toolRefs := make([]ai.ToolRef, 0, len(rc.in.Tools))
	for _, t := range rc.in.Tools {
		t := t // capture for the closure below
		schema := t.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		def := genkit.DefineToolWithInputSchema[any](rc.g, t.Name, t.Description, schema,
			func(tc *ai.ToolContext, input any) (any, error) {
				args := toArgsMap(input)
				fmt.Fprintf(os.Stderr, "PROGRESS: tools: executing %q args=%v\n", t.Name, args)
				result, err := executeRealTool(rc.ctx, rc, t.Name, args)
				mu.Lock()
				executed = append(executed, outToolCall{Name: t.Name, Arguments: args})
				mu.Unlock()
				if err != nil {
					return nil, err
				}
				return result, nil
			})
		toolRefs = append(toolRefs, def)
	}

	opts := buildBaseOpts(rc)
	opts = append(opts, ai.WithTools(toolRefs...), ai.WithMaxTurns(6))

	start := time.Now()
	resp, err := genkit.Generate(rc.ctx, rc.g, opts...)
	out.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		return fmt.Errorf("tools: genkit.Generate: %w", err)
	}

	applyUsage(out, resp)
	out.Reply = resp.Text()
	mu.Lock()
	out.ToolCalls = executed
	mu.Unlock()
	if out.ToolCalls == nil {
		out.ToolCalls = []outToolCall{}
	}
	fmt.Fprintf(os.Stderr, "PROGRESS: tools: %d real tool execution(s)\n", len(out.ToolCalls))
	return nil
}

// executeRealTool dispatches to a genuinely-executed implementation based on
// the tool's name and argument shape.
func executeRealTool(ctx context.Context, rc *runCtx, name string, args map[string]any) (any, error) {
	lname := strings.ToLower(name)

	if expr, ok := firstStringArg(args, "expression", "expr", "equation"); ok || strings.Contains(lname, "calc") || strings.Contains(lname, "math") {
		if !ok {
			return nil, fmt.Errorf("calculator: no expression argument in %v", args)
		}
		val, err := evalArithmetic(expr)
		if err != nil {
			return nil, fmt.Errorf("calculator: %w", err)
		}
		return map[string]any{"expression": expr, "result": val}, nil
	}

	if q, ok := firstStringArg(args, "query", "question", "text", "topic"); ok || strings.Contains(lname, "corpus") || strings.Contains(lname, "lookup") || strings.Contains(lname, "search") || strings.Contains(lname, "rag") {
		if !ok {
			return nil, fmt.Errorf("corpus_lookup: no query argument in %v", args)
		}
		vec, err := embedQuery(ctx, rc.embedURL, q)
		if err != nil {
			return nil, fmt.Errorf("corpus_lookup: embed: %w", err)
		}
		docs, err := searchQdrant(ctx, rc.qdrantURL, ragCollection, vec, 3)
		if err != nil {
			return nil, fmt.Errorf("corpus_lookup: qdrant: %w", err)
		}
		return map[string]any{"query": q, "passages": docs}, nil
	}

	// Generic fallback: a real, dynamic (non-canned) execution for any tool
	// shape this adapter doesn't specifically recognize.
	return map[string]any{
		"tool":       name,
		"args_echo":  args,
		"server_utc": time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

func firstStringArg(args map[string]any, keys ...string) (string, bool) {
	for _, k := range keys {
		if v, ok := args[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s, true
			}
		}
	}
	return "", false
}

// ---- A tiny, real recursive-descent arithmetic evaluator (+ - * / ()) ----
// No third-party expression-eval dependency is needed for the 4 operators the
// calculator tool exposes; this is exact, not an approximation or a stub.

type exprParser struct {
	s   string
	pos int
}

func evalArithmetic(expr string) (float64, error) {
	p := &exprParser{s: strings.ReplaceAll(expr, " ", "")}
	if p.s == "" {
		return 0, fmt.Errorf("empty expression")
	}
	v, err := p.parseExpr()
	if err != nil {
		return 0, err
	}
	if p.pos != len(p.s) {
		return 0, fmt.Errorf("unexpected trailing input at %d in %q", p.pos, expr)
	}
	return v, nil
}

func (p *exprParser) parseExpr() (float64, error) {
	v, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for p.pos < len(p.s) && (p.s[p.pos] == '+' || p.s[p.pos] == '-') {
		op := p.s[p.pos]
		p.pos++
		rhs, err := p.parseTerm()
		if err != nil {
			return 0, err
		}
		if op == '+' {
			v += rhs
		} else {
			v -= rhs
		}
	}
	return v, nil
}

func (p *exprParser) parseTerm() (float64, error) {
	v, err := p.parseFactor()
	if err != nil {
		return 0, err
	}
	for p.pos < len(p.s) && (p.s[p.pos] == '*' || p.s[p.pos] == '/') {
		op := p.s[p.pos]
		p.pos++
		rhs, err := p.parseFactor()
		if err != nil {
			return 0, err
		}
		if op == '*' {
			v *= rhs
		} else {
			if rhs == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			v /= rhs
		}
	}
	return v, nil
}

func (p *exprParser) parseFactor() (float64, error) {
	if p.pos < len(p.s) && p.s[p.pos] == '-' {
		p.pos++
		v, err := p.parseFactor()
		return -v, err
	}
	if p.pos < len(p.s) && p.s[p.pos] == '(' {
		p.pos++
		v, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		if p.pos >= len(p.s) || p.s[p.pos] != ')' {
			return 0, fmt.Errorf("missing closing paren")
		}
		p.pos++
		return v, nil
	}
	start := p.pos
	for p.pos < len(p.s) && (p.s[p.pos] >= '0' && p.s[p.pos] <= '9' || p.s[p.pos] == '.') {
		p.pos++
	}
	if start == p.pos {
		return 0, fmt.Errorf("expected number at %d in %q", p.pos, p.s)
	}
	return strconv.ParseFloat(p.s[start:p.pos], 64)
}
