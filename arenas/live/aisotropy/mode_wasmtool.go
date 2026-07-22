package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	_ "embed"
	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

// wasmModule is the compiled WASM tool guest (testdata/wasmguest), built by
// build.sh with `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared` and
// embedded here so the adapter binary is self-contained (no runtime toolchain,
// no external file — it works identically native and in the distroless image).
//
//go:embed calc.wasm
var wasmModule []byte

// arithmeticRe matches a run of arithmetic characters — used to lift the
// expression out of a natural-language prompt for the sandboxed calculator.
var arithmeticRe = regexp.MustCompile(`[0-9(][0-9\.\s\+\-\*/\(\)]*[0-9)]`)

// runWASMTool is aisotropy's DIFFERENTIATOR: it executes a tool body inside a
// wazero WebAssembly sandbox — memory-isolated, no host filesystem/network/clock,
// reachable only through the ABI the host chooses — the capability no other
// framework in this arena has. The sandboxed `calculate` tool genuinely evaluates
// the arithmetic; its result (and the isolation proof that it could NOT read a
// host file, hence could not reach the agnes key the host holds) is folded into a
// real agnes turn.
//
// This is the HONEST WASM dimension: a tool executed in a WASM sandbox
// (tool-sandbox capability), not the runtime itself running in WASM.
func runWASMTool(req *request, out *result) error {
	prompt, err := lastUserMessage(req.Messages)
	if err != nil {
		return err
	}
	expression := extractExpression(prompt)
	if expression == "" {
		return fmt.Errorf("wasm_tool: could not find an arithmetic expression in the prompt")
	}

	// Execute the tool inside the wazero sandbox.
	wctx, wcancel := context.WithTimeout(context.Background(), 10*time.Second)
	tool, err := newWASMTool(wctx, wasmModule)
	if err != nil {
		wcancel()
		return fmt.Errorf("wasm_tool: load sandbox: %w", err)
	}
	calc, err := tool.runWASMCalc(wctx, expression)
	_ = tool.Close(wctx)
	wcancel()
	if err != nil {
		return fmt.Errorf("wasm_tool: sandbox dispatch: %w", err)
	}
	if calc.Error != "" {
		return fmt.Errorf("wasm_tool: guest reported error: %s", calc.Error)
	}
	if calc.Result == nil {
		return fmt.Errorf("wasm_tool: guest returned no result")
	}
	resultStr := formatNumber(*calc.Result)
	logf("wasm_tool: wazero(%s)=%s engine=%s fs_read_denied=%v", expression, resultStr, calc.Engine, calc.FSReadDenied)

	out.ToolCalls = []toolCall{{
		Name:      "calculate_wasm",
		Arguments: map[string]any{"expression": expression},
		Result:    fmt.Sprintf("%s (engine=%s, sandbox_fs_read_denied=%v)", resultStr, calc.Engine, calc.FSReadDenied),
	}}

	// Fold the sandbox result into a real agnes turn.
	agnes, err := newAgnesClient()
	if err != nil {
		return err
	}
	app := newApp(agnes, wasmToolPersona)
	defer func() { _ = app.Close() }()

	foldPrompt := fmt.Sprintf(
		"The sandboxed WASM calculator tool evaluated %q and returned %s. Answer the user's question in one sentence using this result.\n\nUser question: %s",
		expression, resultStr, prompt)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	o, terr := app.Turn(ctx, aisotropy.ChatInput{
		TenantID:  arenaTenant,
		UserID:    arenaUser,
		Prompt:    foldPrompt,
		SessionID: sessionID(req),
		ProductID: productID,
	})
	cancel()
	out.LatencyMs = time.Since(start).Milliseconds()
	if terr != nil {
		setUsage(out, app)
		out.Reply = fmt.Sprintf("The sandboxed WASM tool computed %s = %s.", expression, resultStr)
		return fmt.Errorf("App.Turn (wasm fold) failed: %w", terr)
	}
	out.Reply = o.Reply
	setUsage(out, app)
	return nil
}

const wasmToolPersona = `You are a concise assistant. A sandboxed WebAssembly
calculator tool has already computed the exact arithmetic result for you; state
the answer to the user in one sentence using that result.`

// extractExpression lifts the longest arithmetic substring from a prompt.
func extractExpression(prompt string) string {
	matches := arithmeticRe.FindAllString(prompt, -1)
	best := ""
	for _, m := range matches {
		m = strings.TrimSpace(m)
		// Require at least one operator so a bare number is not treated as an
		// "expression".
		if strings.ContainsAny(m, "+-*/") && len(m) > len(best) {
			best = m
		}
	}
	return best
}
