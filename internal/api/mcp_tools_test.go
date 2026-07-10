package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fugue-labs/gollem/ext/mcp"
)

// fakeMCPClient is an in-memory MCPClient: no subprocess. It records the last
// CallTool invocation so tests can assert dispatch + arg decoding.
type fakeMCPClient struct {
	tools    []mcp.Tool
	result   *mcp.ToolResult
	callErr  error
	lastName string
	lastArgs map[string]any
	calls    int
}

func (f *fakeMCPClient) ListTools(context.Context) ([]mcp.Tool, error) { return f.tools, nil }

func (f *fakeMCPClient) CallTool(_ context.Context, name string, args map[string]any) (*mcp.ToolResult, error) {
	f.calls++
	f.lastName = name
	f.lastArgs = args
	if f.callErr != nil {
		return nil, f.callErr
	}
	return f.result, nil
}

var readFileSchema = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)

// TestBuildMCPTools_ScopesAndCarriesSchema: each discovered MCP tool becomes a
// scope-gated Aisotropy tool carrying the server's InputSchema (lever #2).
func TestBuildMCPTools_ScopesAndCarriesSchema(t *testing.T) {
	fc := &fakeMCPClient{tools: []mcp.Tool{{Name: "read_file", Description: "read a file", InputSchema: readFileSchema}}}

	ts, handlers, err := BuildMCPTools(context.Background(), fc, "read:files")
	if err != nil {
		t.Fatalf("BuildMCPTools: %v", err)
	}
	if len(ts) != 1 || ts[0].Name != "read_file" {
		t.Fatalf("tools = %+v, want a single read_file", ts)
	}
	if ts[0].RequiredScope != "read:files" {
		t.Fatalf("RequiredScope = %q, want read:files (scope-gated)", ts[0].RequiredScope)
	}
	if !bytes.Equal(ts[0].Parameters, readFileSchema) {
		t.Fatalf("Parameters = %s, want the server InputSchema", ts[0].Parameters)
	}
	if _, ok := handlers["read_file"]; !ok {
		t.Fatalf("no handler built for read_file")
	}
}

// TestMCPHandler_DispatchesDecodedArgs: a valid call decodes the model's JSON
// into the map[string]any MCP carries, invokes the server, and renders the text.
func TestMCPHandler_DispatchesDecodedArgs(t *testing.T) {
	fc := &fakeMCPClient{
		tools:  []mcp.Tool{{Name: "read_file", InputSchema: readFileSchema}},
		result: &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: "file contents"}}},
	}
	_, handlers, _ := BuildMCPTools(context.Background(), fc, "read:files")

	out, err := handlers["read_file"](context.Background(), json.RawMessage(`{"path":"/tmp/x"}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if out != "file contents" {
		t.Fatalf("result = %q, want %q", out, "file contents")
	}
	if fc.lastName != "read_file" || fc.lastArgs["path"] != "/tmp/x" {
		t.Fatalf("dispatched name=%q args=%+v, want read_file path=/tmp/x", fc.lastName, fc.lastArgs)
	}
}

// TestMCPHandler_InvalidArgsReflect: args that violate the server's InputSchema
// (missing required field) must NOT reach the server — they reflect (SDD R4).
func TestMCPHandler_InvalidArgsReflect(t *testing.T) {
	fc := &fakeMCPClient{
		tools:  []mcp.Tool{{Name: "read_file", InputSchema: readFileSchema}},
		result: &mcp.ToolResult{Content: []mcp.Content{{Text: "should not happen"}}},
	}
	_, handlers, _ := BuildMCPTools(context.Background(), fc, "read:files")

	_, err := handlers["read_file"](context.Background(), json.RawMessage(`{"wrong":"y"}`))
	var ae *ToolArgsError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want a *ToolArgsError (reflection signal)", err)
	}
	if fc.calls != 0 {
		t.Fatalf("MCP server was called %d times on invalid args, want 0", fc.calls)
	}
}

// TestMCPHandler_IsErrorResultSurfacesError: an MCP result flagged IsError
// becomes a Go error (so a failing MCP tool is a real failure, not a silent ok).
func TestMCPHandler_IsErrorResultSurfacesError(t *testing.T) {
	fc := &fakeMCPClient{
		tools:  []mcp.Tool{{Name: "read_file", InputSchema: readFileSchema}},
		result: &mcp.ToolResult{IsError: true, Content: []mcp.Content{{Text: "permission denied"}}},
	}
	_, handlers, _ := BuildMCPTools(context.Background(), fc, "read:files")

	_, err := handlers["read_file"](context.Background(), json.RawMessage(`{"path":"/root/secret"}`))
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v, want an error carrying the MCP error text", err)
	}
}

// TestFormatMCPResult_StructuredFallback: a result with no text content falls
// back to the compact JSON of its structured content.
func TestFormatMCPResult_StructuredFallback(t *testing.T) {
	res := &mcp.ToolResult{StructuredContent: map[string]any{"temp": 22}}
	out, err := formatMCPResult("get_weather", res)
	if err != nil {
		t.Fatalf("formatMCPResult: %v", err)
	}
	if !strings.Contains(out, "temp") || !strings.Contains(out, "22") {
		t.Fatalf("structured fallback = %q, want the JSON of the structured content", out)
	}
}
