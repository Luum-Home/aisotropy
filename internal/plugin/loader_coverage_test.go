package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fugue-labs/gollem/ext/mcp"

	"github.com/Luum-Home/aisotropy/internal/api"
)

// TestLoadTools_UnknownKind exercises LoadTools' defensive default branch:
// Validate is expected to reject an unknown Kind before LoadTools ever sees
// it, but LoadTools itself must still degrade to an error, not a panic, if
// that invariant is ever bypassed (Kind is a bare string type).
func TestLoadTools_UnknownKind(t *testing.T) {
	m := Manifest{Name: "mystery", Version: "1", License: "MIT", Kind: Kind("bogus")}
	_, err := LoadTools(context.Background(), m, nil)
	if err == nil {
		t.Fatal("expected an error for an unknown Kind")
	}
}

// TestMcpScope_NoDeclaredTools exercises mcpScope's "no tools declared"
// branch directly: the server's tools become always-available (empty scope).
func TestMcpScope_NoDeclaredTools(t *testing.T) {
	m := Manifest{Name: "fs", Kind: KindMCP}
	if got := mcpScope(m); got != "" {
		t.Fatalf("mcpScope with no declared tools = %q, want empty string", got)
	}
}

// TestLoadTools_MCP_NilCloserBecomesNoop exercises loadMCP's closer==nil
// branch: a connector that returns a nil closer must not panic on Close, and
// Close must still be safely callable (noopClose).
func TestLoadTools_MCP_NilCloserBecomesNoop(t *testing.T) {
	fake := &fakeMCPClient{tools: []mcp.Tool{{Name: "ping", Description: "ping"}}}
	connect := func(ctx context.Context, command string, args ...string) (api.MCPClient, func() error, error) {
		return fake, nil, nil // no closer supplied
	}
	m := Manifest{Name: "fs", Version: "1", License: "MIT", Kind: KindMCP, Run: RunSpec{Command: "npx"}}

	loaded, err := LoadTools(context.Background(), m, connect)
	if err != nil {
		t.Fatalf("LoadTools: %v", err)
	}
	if err := loaded.Close(); err != nil {
		t.Fatalf("expected the nil closer to become a no-op, got error: %v", err)
	}
}

// failingListToolsClient satisfies api.MCPClient but fails discovery, so
// loadMCP's "close subprocess on failed discovery, never leak it" branch can
// be exercised.
type failingListToolsClient struct{}

func (failingListToolsClient) ListTools(ctx context.Context) ([]mcp.Tool, error) {
	return nil, errors.New("server unreachable")
}

func (failingListToolsClient) CallTool(ctx context.Context, name string, args map[string]any) (*mcp.ToolResult, error) {
	return nil, errors.New("unreachable")
}

func TestLoadTools_MCP_DiscoveryFailureClosesSubprocess(t *testing.T) {
	var closed bool
	connect := func(ctx context.Context, command string, args ...string) (api.MCPClient, func() error, error) {
		return failingListToolsClient{}, func() error { closed = true; return nil }, nil
	}
	m := Manifest{Name: "fs", Version: "1", License: "MIT", Kind: KindMCP, Run: RunSpec{Command: "npx"}}

	_, err := LoadTools(context.Background(), m, connect)
	if err == nil {
		t.Fatal("expected discovery error to propagate")
	}
	if !closed {
		t.Fatal("expected the subprocess closer to run on a failed discovery, to avoid leaking it")
	}
}

// TestLoadTools_WASM_InvalidModuleBytes exercises wasmtool.New's own error
// path (invalid module compile) as surfaced through loadWASM: a file that
// exists but is not valid WASM must produce a load error, not a panic.
func TestLoadTools_WASM_InvalidModuleBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.wasm")
	if err := os.WriteFile(path, []byte("not a real wasm module"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	m := Manifest{
		Name: "broken", Version: "1", License: "MIT", Kind: KindWASM,
		Run: RunSpec{Module: path},
	}
	_, err := LoadTools(context.Background(), m, nil)
	if err == nil {
		t.Fatal("expected an error for an invalid wasm module")
	}
}
