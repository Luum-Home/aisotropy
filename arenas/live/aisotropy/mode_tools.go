package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

// calcExecutor is the REAL tool body the agent route executes: it truly evaluates
// the arithmetic expression the model supplies — never a canned value.
type calcExecutor struct{}

func (calcExecutor) Execute(_ context.Context, name string, args map[string]string) (string, error) {
	if name != "calculate" {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	v, err := evalArithmetic(args["expression"])
	if err != nil {
		return "", fmt.Errorf("calculate(%q): %w", args["expression"], err)
	}
	return formatNumber(v), nil
}

const calcParamsSchema = `{
  "type": "object",
  "properties": {
    "expression": {
      "type": "string",
      "description": "A plain arithmetic expression to evaluate, e.g. '(12 + 30) * 7 / 2'."
    }
  },
  "required": ["expression"]
}`

const toolsPersona = `You are a careful assistant. For any arithmetic in the
user's request you MUST call the "calculate" tool to compute the exact value —
never do the arithmetic yourself. After the tool returns, answer in one sentence
that states the computed result.`

// runTools drives aisotropy's REAL tool-EXECUTION route. It boots App.Run and
// POSTs /v1/.../chat/agent, which runs aisotropy's agentic loop (chat_agent.go):
// the model chooses the scope-permitted `calculate` tool, the runtime EXECUTES it
// through the registered ToolExecutor, feeds the result back, and returns the
// ordered tool decisions + results. This is the genuine seam App.Turn cannot
// express (its text-first chat surface never surfaces tool_calls) — so this mode,
// unlike the single-shot qa mode, reports real EXECUTED tool calls.
func runTools(req *request, out *result) error {
	prompt, err := lastUserMessage(req.Messages)
	if err != nil {
		return err
	}

	agnes, err := newAgnesClient()
	if err != nil {
		return err
	}

	calcTool := aisotropy.Tool{
		Name:          "calculate",
		Description:   "Evaluates a plain arithmetic expression (+ - * / and parentheses) and returns the exact numeric result.",
		RequiredScope: "calc",
		Parameters:    json.RawMessage(calcParamsSchema),
	}
	opts := []aisotropy.Option{
		aisotropy.WithTool(calcTool),
		aisotropy.WithToolExecutor(calcExecutor{}),
	}
	if agnes == nil {
		// ARENA_STUB=1: inject the adapter-local tool-capable stub (tool_stub.go)
		// instead of falling through to aisotropy's internal keyless StubClient,
		// which does not implement llm.ToolCaller and cannot drive the
		// /chat/agent route offline (see tool_stub.go's doc comment).
		opts = append(opts, aisotropy.WithLLM(toolStub{}))
	}
	srv, err := bootServer(agnes, toolsPersona, opts...)
	if err != nil {
		return err
	}
	defer srv.stop()

	agentBody := map[string]any{
		"prompt":     prompt,
		"product_id": productID,
		"session_id": sessionID(req),
		"scopes":     []string{"calc"},
	}
	body, _ := json.Marshal(agentBody)

	url := fmt.Sprintf("%s/v1/tenant/%s/user/%s/chat/agent", srv.baseURL, arenaTenant, arenaUser)
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := (&http.Client{Timeout: 115 * time.Second}).Do(httpReq)
	if err != nil {
		return fmt.Errorf("POST /chat/agent: %w", err)
	}
	defer resp.Body.Close()
	out.LatencyMs = time.Since(start).Milliseconds()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/chat/agent returned status %d", resp.StatusCode)
	}

	var agentResp struct {
		TenantID string `json:"tenant_id"`
		Steps    []struct {
			Tool   string            `json:"tool"`
			Args   map[string]string `json:"args"`
			Result string            `json:"result"`
		} `json:"steps"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&agentResp); err != nil {
		return fmt.Errorf("decode /chat/agent response: %w", err)
	}

	var lastResult string
	seen := map[string]bool{}
	steps := 0
	for _, s := range agentResp.Steps {
		if s.Tool == "" {
			continue // "answer directly" step, no tool executed
		}
		steps++
		args := map[string]any{}
		for k, v := range s.Args {
			args[k] = v
		}
		if s.Result != "" {
			lastResult = s.Result
		}
		// The homegrown structured-output agent loop may re-issue an identical
		// call across steps (it feeds the tool result back as text and asks the
		// model to continue); collapse identical (name+args+result) executions so
		// the reported trace is not noisy while staying faithful to what ran.
		key := s.Tool + "|" + fmt.Sprint(args) + "|" + s.Result
		if seen[key] {
			continue
		}
		seen[key] = true
		out.ToolCalls = append(out.ToolCalls, toolCall{Name: s.Tool, Arguments: args, Result: s.Result})
	}
	setUsage(out, agnes)
	logf("tools: agent loop ran %d tool step(s), %d unique call(s)", steps, len(out.ToolCalls))

	if len(out.ToolCalls) == 0 {
		return fmt.Errorf("agent route completed without executing a tool — expected the calculate tool to run for an arithmetic request")
	}
	// Fold the executed tool result into the answer (the agent route returns tool
	// decisions, not a final natural-language reply).
	out.Reply = fmt.Sprintf("The calculate tool was executed and returned %s.", lastResult)
	logf("tools: executed %d tool call(s), last result=%s", len(out.ToolCalls), lastResult)
	return nil
}
