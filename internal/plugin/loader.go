package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/Luum-Home/aisotropy/internal/api"
	"github.com/Luum-Home/aisotropy/internal/tools"
	"github.com/Luum-Home/aisotropy/internal/wasmtool"
)

// ErrKindUnsupported is reserved for a manifest whose Kind is recognized by
// Validate but not yet runnable. Every kind Validate accepts (mcp, go-import,
// wasm) is runnable today, so LoadTools does not currently return it; it is kept
// so a future reserved kind can degrade gracefully (skip the plugin, log, keep
// booting) with a distinguishable sentinel.
var ErrKindUnsupported = errors.New("plugin kind not yet supported")

// MCPConnector is how LoadTools reaches an MCP server without this package
// importing any MCP client library (or the public aisotropy package). It connects
// the server described by command+args and returns a live client satisfying
// api.MCPClient plus a closer for its subprocess lifecycle. Wave 2 supplies the
// real one (backed by gollem's mcp.NewStdioClient, exactly as aisotropy/app.go's
// discovery loop already does); tests supply a fake. A nil connector is a
// programming error for a KindMCP manifest and yields an error, not a panic.
type MCPConnector func(ctx context.Context, command string, args ...string) (client api.MCPClient, closer func() error, err error)

// Loaded is a launched plugin's contribution to the runtime, in the exact shapes
// the core already assembles: a scope-gated tool list and the typed handlers that
// dispatch them, plus a Close that releases any subprocess the plugin owns. The
// caller merges Tools into its tools.Registry and Handlers into its
// map[string]api.TypedToolHandler, and defers Close.
type Loaded struct {
	// Tools are the plugin's tools, each already carrying its RequiredScope so the
	// same scope gate native and MCP tools get applies unchanged.
	Tools []tools.Tool
	// Handlers dispatch each tool by name. For KindMCP there is one handler per
	// discovered tool; for KindGoImport it is empty (see LoadTools).
	Handlers map[string]api.TypedToolHandler
	// Close releases the plugin's resources (an MCP subprocess for KindMCP). Never
	// nil — a plugin that owns nothing gets a no-op — so the caller can always
	// `defer loaded.Close()`.
	Close func() error
}

// noopClose is the Close for a plugin that owns no external resource.
func noopClose() error { return nil }

// LoadTools turns a validated manifest into its runtime contribution, dispatching
// on Kind. It assumes m has passed Validate (ParseManifest guarantees this).
//
//   - KindMCP: connect the server via connect, discover its tools with
//     api.BuildMCPTools, and return them scope-gated by m.Run's declared scope —
//     i.e. every one of the server's tools shares the manifest's tool scope. This
//     reuses the same MCP path aisotropy/app.go already runs, so the resulting
//     shapes are identical. The returned Close closes the subprocess; if
//     discovery fails after connecting, the subprocess is closed before returning
//     the error so a failed load never leaks a process.
//
//   - KindGoImport: Go has no safe runtime plugin loading, so nothing is
//     executed here. The manifest's declared Tools are returned as governance
//     descriptors (scope-gated, no Parameters schema) so they can be surfaced and
//     scope-audited, but Handlers is EMPTY: the behavior arrives via a normal Go
//     import the consumer links and registers itself. Wiring those handlers is a
//     compile-time step outside this loader — a caller that surfaces these
//     descriptors without also registering the Go handlers will have tools the
//     model can see but not execute. This is the documented convention, stated
//     plainly rather than faked.
//
//   - KindWASM: reads the module at m.Run.Module and loads it into the wazero
//     sandbox (internal/wasmtool), exposing its ABI entry point as ONE
//     scope-gated tool. The tool's name/description/scope come from m.Tools[0]
//     when declared, else from the manifest itself (see loadWASM). The module
//     runs in-process but memory-isolated with NO host access by default; the
//     returned Close tears down the wazero runtime. This replaces the former
//     "not yet supported" stub.
//
// The requiredScope applied to KindMCP tools is derived from the manifest: MCP
// servers expose a homogeneous tool set under one scope, matching WithMCPServer's
// existing model. When a manifest lists per-tool scopes in m.Tools for an MCP
// plugin, they are advisory documentation only — the server is the source of
// truth for its actual tool list, so the manifest cannot rename or re-scope
// individual server tools. The single scope used is mcpScope(m).
func LoadTools(ctx context.Context, m Manifest, connect MCPConnector) (*Loaded, error) {
	switch m.Kind {
	case KindMCP:
		return loadMCP(ctx, m, connect)
	case KindGoImport:
		return loadGoImport(m), nil
	case KindWASM:
		return loadWASM(ctx, m)
	default:
		// Validate should have rejected this; guard defensively.
		return nil, fmt.Errorf("plugin: cannot load unknown kind %q", m.Kind)
	}
}

// mcpScope is the single RequiredScope applied to every tool an MCP plugin
// exposes. If the manifest declares exactly one tool scope it is honored; any
// declared scope on the first tool is used as the server-wide scope. When the
// manifest declares no tools, the server's tools are always-available (empty
// scope) — matching WithMCPServer's "empty = always available" contract.
func mcpScope(m Manifest) string {
	if len(m.Tools) > 0 {
		return m.Tools[0].RequiredScope
	}
	return ""
}

// loadMCP connects the MCP subprocess and adapts its tools via the shared
// api.BuildMCPTools path.
func loadMCP(ctx context.Context, m Manifest, connect MCPConnector) (*Loaded, error) {
	if connect == nil {
		return nil, fmt.Errorf("plugin %q: kind mcp requires an MCPConnector", m.Name)
	}
	client, closer, err := connect(ctx, m.Run.Command, m.Run.Args...)
	if err != nil {
		return nil, fmt.Errorf("plugin %q: connect mcp server: %w", m.Name, err)
	}
	if closer == nil {
		closer = noopClose
	}
	ts, handlers, err := api.BuildMCPTools(ctx, client, mcpScope(m))
	if err != nil {
		_ = closer() // never leak the subprocess on a failed discovery
		return nil, fmt.Errorf("plugin %q: discover mcp tools: %w", m.Name, err)
	}
	return &Loaded{Tools: ts, Handlers: handlers, Close: closer}, nil
}

// loadWASM reads the module named by m.Run.Module and adapts it into a single
// scope-gated, wasmtool-backed tool. The tool's identity is taken from the
// manifest's first declared tool when present (name/description/required_scope) —
// a wasm plugin exposes exactly one ABI entry point, so at most the first tool
// spec is meaningful — and otherwise falls back to the manifest's own name with
// an empty (always-available) scope. Validate has already guaranteed Run.Module
// is non-empty.
//
// The sandbox is wired with its DEFAULT capabilities: WASI present but powerless
// (no filesystem, env, args, network, or real clock) and NO aisotropy host
// functions. Granting the optional log host function needs a logger to hand the
// sandbox, which is a wiring concern for the public builder (Wave 2), not this
// dependency-free loader — so the loader deliberately loads wasm plugins as pure
// sandboxes. The returned Close tears down the wazero runtime the tool owns.
func loadWASM(ctx context.Context, m Manifest) (*Loaded, error) {
	wasmBytes, err := os.ReadFile(m.Run.Module)
	if err != nil {
		return nil, fmt.Errorf("plugin %q: read wasm module %q: %w", m.Name, m.Run.Module, err)
	}

	name, description, scope := m.Name, m.Description, ""
	if len(m.Tools) > 0 {
		t := m.Tools[0]
		name, description, scope = t.Name, t.Description, t.RequiredScope
	}

	tool, err := wasmtool.New(ctx, wasmtool.Config{
		Name:          name,
		Description:   description,
		RequiredScope: scope,
		Module:        wasmBytes,
		Capabilities:  wasmtool.Capabilities{AllowWASI: true},
	})
	if err != nil {
		return nil, fmt.Errorf("plugin %q: load wasm tool: %w", m.Name, err)
	}

	return &Loaded{
		Tools:    []tools.Tool{tool.Spec()},
		Handlers: map[string]api.TypedToolHandler{tool.Spec().Name: tool.Handler()},
		// Close with a fresh context so a cancelled request ctx cannot interfere
		// with runtime teardown.
		Close: func() error { return tool.Close(context.Background()) },
	}, nil
}

// loadGoImport returns the manifest's declared tools as scope-gated governance
// descriptors with no handlers (see LoadTools). It never fails: a go-import
// manifest with zero tools yields an empty, harmless contribution.
func loadGoImport(m Manifest) *Loaded {
	ts := make([]tools.Tool, 0, len(m.Tools))
	for _, t := range m.Tools {
		ts = append(ts, tools.Tool{
			Name:          t.Name,
			Description:   t.Description,
			RequiredScope: t.RequiredScope,
		})
	}
	return &Loaded{
		Tools:    ts,
		Handlers: map[string]api.TypedToolHandler{},
		Close:    noopClose,
	}
}
