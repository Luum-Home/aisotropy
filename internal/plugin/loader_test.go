package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

// buildWASMGuest compiles the wasmtool echo fixture to a wasip1 reactor module
// and returns its on-disk path, so the loader's real KindWASM path can be
// exercised end to end. The fixture is shared with the wasmtool package (it lives
// under internal/wasmtool/testdata); building it here needs only the standard Go
// toolchain (no external tools), and is skipped under -short.
func buildWASMGuest(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping wasm build under -short (shells out to `go build` for GOOS=wasip1)")
	}
	out := filepath.Join(t.TempDir(), "echo.wasm")
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", out, ".")
	cmd.Dir = filepath.Join("..", "wasmtool", "testdata", "echomod")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasm guest: %v\n%s", err, b)
	}
	return out
}

// TestLoadTools_WASM_MissingModule proves KindWASM now takes the real loading
// path: a manifest whose run.module does not exist fails with a read error, NOT
// the old ErrKindUnsupported stub.
func TestLoadTools_WASM_MissingModule(t *testing.T) {
	m := Manifest{
		Name: "w", Version: "1", License: "MIT", Kind: KindWASM,
		Run: RunSpec{Module: filepath.Join(t.TempDir(), "does-not-exist.wasm")},
	}
	_, err := LoadTools(context.Background(), m, nil)
	if err == nil {
		t.Fatal("expected an error for a missing wasm module")
	}
	if errors.Is(err, ErrKindUnsupported) {
		t.Fatalf("error = %v, want a real load error, not ErrKindUnsupported", err)
	}
}

// TestLoadTools_WASM_LoadsSandboxedTool proves a KindWASM manifest produces a
// working, scope-gated, sandboxed tool: the tool name/scope come from the
// manifest, the handler runs the module, and the guest cannot reach the host.
func TestLoadTools_WASM_LoadsSandboxedTool(t *testing.T) {
	modulePath := buildWASMGuest(t)
	m := Manifest{
		Name: "echo-plugin", Version: "1", License: "Apache-2.0", Kind: KindWASM,
		Run:   RunSpec{Module: modulePath},
		Tools: []ToolSpec{{Name: "echo", Description: "echo", RequiredScope: "use:echo"}},
	}

	loaded, err := LoadTools(context.Background(), m, nil)
	if err != nil {
		t.Fatalf("LoadTools wasm: %v", err)
	}
	defer func() {
		if err := loaded.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	if len(loaded.Tools) != 1 || loaded.Tools[0].Name != "echo" || loaded.Tools[0].RequiredScope != "use:echo" {
		t.Fatalf("tools = %+v, want one echo/use:echo", loaded.Tools)
	}
	handler, ok := loaded.Handlers["echo"]
	if !ok {
		t.Fatal("no handler registered for echo")
	}

	res, err := handler(context.Background(), json.RawMessage(`{"name":"plugin","n":3}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Greeting     string `json:"greeting"`
		Doubled      int    `json:"doubled"`
		FSReadDenied bool   `json:"fs_read_denied"`
	}
	if err := json.Unmarshal([]byte(res), &out); err != nil {
		t.Fatalf("unmarshal %q: %v", res, err)
	}
	if out.Greeting != "hello plugin" || out.Doubled != 6 {
		t.Errorf("result = %+v, want greeting=hello plugin doubled=6", out)
	}
	if !out.FSReadDenied {
		t.Error("wasm plugin READ a host file — sandbox isolation FAILED")
	}
}
