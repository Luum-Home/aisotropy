package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fugue-labs/gollem/ext/mcp"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// MCPClient is the minimal Model Context Protocol client surface the runtime
// needs to expose an MCP server's tools as Aisotropy tools (ADR-0016 lever #2:
// polyglot tool EXECUTION — a tool's behavior can live in an MCP server written
// in any language, while the Go runtime keeps owning scoping and identity).
// gollem's *mcp.Client satisfies it; the interface exists so the adapter is
// unit-testable without spawning a subprocess.
type MCPClient interface {
	ListTools(ctx context.Context) ([]mcp.Tool, error)
	CallTool(ctx context.Context, name string, args map[string]any) (*mcp.ToolResult, error)
}

// BuildMCPTools discovers the tools an MCP server exposes and adapts each into
// an Aisotropy tool + a typed dispatch handler. Every discovered tool is
// scope-gated with requiredScope (so the double scope-gate applies to MCP tools
// exactly as to native ones) and carries the server's JSON Schema as its
// Parameters, so the model is shown the real argument shape and the args are
// validated (reflection on violation) before the call crosses to the server.
//
// The returned handlers dispatch to the MCP server via CallTool; a consumer
// never imports gollem's mcp package — the runtime owns the client lifecycle.
func BuildMCPTools(ctx context.Context, client MCPClient, requiredScope string) ([]tools.Tool, map[string]TypedToolHandler, error) {
	list, err := client.ListTools(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("mcp: list tools: %w", err)
	}
	ts := make([]tools.Tool, 0, len(list))
	handlers := make(map[string]TypedToolHandler, len(list))
	for _, mt := range list {
		ts = append(ts, tools.Tool{
			Name:          mt.Name,
			Description:   mt.Description,
			RequiredScope: requiredScope,
			Parameters:    mt.InputSchema,
		})
		handlers[mt.Name] = mcpHandler(mt.Name, mt.InputSchema, client)
	}
	return ts, handlers, nil
}

// mcpHandler builds the typed dispatch handler for one MCP tool: it validates
// the model's raw args against the tool's InputSchema (a violation reflects, as
// for any typed tool — SDD R4), decodes them into the map[string]any the MCP
// protocol carries, invokes the server, and renders the result to text. The ctx
// is the request's already-scoped context (ADR-0016 invariant): the handler
// never constructs a Scope; a tenant-aware MCP server reads identity the runtime
// passes, not one the tool mints.
func mcpHandler(name string, schema json.RawMessage, client MCPClient) TypedToolHandler {
	validator, _ := llm.CompileSchema(schema) // nil on empty/uncompilable -> no constraint check
	return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		payload := rawArgs
		if len(bytes.TrimSpace(payload)) == 0 {
			payload = json.RawMessage("{}")
		}
		if err := validator.Validate(payload); err != nil {
			return "", &ToolArgsError{Tool: name, Err: err}
		}
		var args map[string]any
		if err := json.Unmarshal(payload, &args); err != nil {
			return "", &ToolArgsError{Tool: name, Err: err}
		}
		res, err := client.CallTool(ctx, name, args)
		if err != nil {
			return "", fmt.Errorf("mcp tool %q: %w", name, err)
		}
		return formatMCPResult(name, res)
	}
}

// formatMCPResult renders an MCP ToolResult into the plain-text result the agent
// loop feeds back to the model. It joins the textual content parts; a result
// flagged IsError becomes a Go error (so a failing MCP tool records friction and
// surfaces as a tool failure, not a silent success); a result with only
// structured content falls back to its compact JSON.
func formatMCPResult(name string, res *mcp.ToolResult) (string, error) {
	if res == nil {
		return "", nil
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if c.Text == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(c.Text)
	}
	text := sb.String()
	if res.IsError {
		if text == "" {
			text = "tool reported an error with no message"
		}
		return "", fmt.Errorf("mcp tool %q error: %s", name, text)
	}
	if text == "" && res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			text = string(b)
		}
	}
	return text, nil
}
