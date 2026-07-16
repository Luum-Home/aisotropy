package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/fugue-labs/gollem/ext/mcp"

	"github.com/Luum-Home/aisotropy/internal/api"
)

// fakeMCPClient satisfies api.MCPClient without a subprocess: it returns a canned
// tool list and echoes call args, so LoadTools' MCP path can be exercised in a
// unit test (the same seam api.BuildMCPTools uses in its own tests).
type fakeMCPClient struct {
	tools    []mcp.Tool
	lastName string
	lastArgs map[string]any
}

func (f *fakeMCPClient) ListTools(ctx context.Context) ([]mcp.Tool, error) {
	return f.tools, nil
}

func (f *fakeMCPClient) CallTool(ctx context.Context, name string, args map[string]any) (*mcp.ToolResult, error) {
	f.lastName = name
	f.lastArgs = args
	return &mcp.ToolResult{Content: []mcp.Content{{Text: "ok:" + name}}}, nil
}

func TestLoadTools_MCP_WiresToolsAndScope(t *testing.T) {
	fake := &fakeMCPClient{tools: []mcp.Tool{
		{Name: "read_file", Description: "Read a file", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "write_file", Description: "Write a file", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}}
	var closed bool
	connect := func(ctx context.Context, command string, args ...string) (api.MCPClient, func() error, error) {
		if command != "npx" {
			t.Fatalf("connect command = %q, want npx", command)
		}
		return fake, func() error { closed = true; return nil }, nil
	}

	m := Manifest{
		Name: "fs", Version: "1", License: "MIT", Kind: KindMCP,
		Run:   RunSpec{Command: "npx"},
		Tools: []ToolSpec{{Name: "read_file", RequiredScope: "read:files"}},
	}
	loaded, err := LoadTools(context.Background(), m, connect)
	if err != nil {
		t.Fatalf("LoadTools: %v", err)
	}
	if len(loaded.Tools) != 2 {
		t.Fatalf("tools = %d, want 2", len(loaded.Tools))
	}
	for _, tl := range loaded.Tools {
		if tl.RequiredScope != "read:files" {
			t.Fatalf("tool %q scope = %q, want read:files (manifest server-wide scope)", tl.Name, tl.RequiredScope)
		}
	}
	if len(loaded.Handlers) != 2 {
		t.Fatalf("handlers = %d, want 2", len(loaded.Handlers))
	}
	// The handler must dispatch to the MCP client.
	out, err := loaded.Handlers["read_file"](context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if out != "ok:read_file" || fake.lastName != "read_file" {
		t.Fatalf("dispatch = %q (last=%q), want ok:read_file", out, fake.lastName)
	}
	if err := loaded.Close(); err != nil || !closed {
		t.Fatalf("Close did not run the injected closer (err=%v closed=%v)", err, closed)
	}
}

func TestLoadTools_MCP_NilConnector(t *testing.T) {
	m := Manifest{Name: "fs", Version: "1", License: "MIT", Kind: KindMCP, Run: RunSpec{Command: "npx"}}
	if _, err := LoadTools(context.Background(), m, nil); err == nil {
		t.Fatal("expected error for nil connector on mcp plugin")
	}
}

func TestLoadTools_MCP_ConnectError(t *testing.T) {
	connect := func(ctx context.Context, command string, args ...string) (api.MCPClient, func() error, error) {
		return nil, nil, errors.New("boom")
	}
	m := Manifest{Name: "fs", Version: "1", License: "MIT", Kind: KindMCP, Run: RunSpec{Command: "npx"}}
	if _, err := LoadTools(context.Background(), m, connect); err == nil {
		t.Fatal("expected connect error to propagate")
	}
}

func TestLoadTools_GoImport_DescriptorsNoHandlers(t *testing.T) {
	m := Manifest{
		Name: "calc", Version: "1", License: "MIT", Kind: KindGoImport,
		Tools: []ToolSpec{
			{Name: "add", Description: "Add two ints", RequiredScope: "use:calc"},
			{Name: "sub", Description: "Subtract"},
		},
	}
	loaded, err := LoadTools(context.Background(), m, nil)
	if err != nil {
		t.Fatalf("LoadTools go-import: %v", err)
	}
	if len(loaded.Tools) != 2 {
		t.Fatalf("tools = %d, want 2", len(loaded.Tools))
	}
	if loaded.Tools[0].Name != "add" || loaded.Tools[0].RequiredScope != "use:calc" {
		t.Fatalf("tool[0] = %+v, want add/use:calc", loaded.Tools[0])
	}
	if len(loaded.Handlers) != 0 {
		t.Fatalf("handlers = %d, want 0 (go-import handlers arrive via Go import)", len(loaded.Handlers))
	}
	if loaded.Close == nil {
		t.Fatal("Close must never be nil")
	}
	if err := loaded.Close(); err != nil {
		t.Fatalf("noop Close: %v", err)
	}
}

func TestLoadTools_WASM_NotYetSupported(t *testing.T) {
	m := Manifest{Name: "w", Version: "1", License: "MIT", Kind: KindWASM}
	_, err := LoadTools(context.Background(), m, nil)
	if !errors.Is(err, ErrKindUnsupported) {
		t.Fatalf("error = %v, want ErrKindUnsupported", err)
	}
}
