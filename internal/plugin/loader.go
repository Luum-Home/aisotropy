package plugin

import (
	"context"
	"errors"
	"fmt"

	"github.com/Luum-Home/aisotropy/internal/api"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// ErrKindUnsupported is returned by LoadTools for a manifest whose Kind is
// recognized by Validate but not yet runnable — today only KindWASM. It lets a
// caller distinguish "reserved-for-later" from a genuine configuration error and
// degrade gracefully (skip the plugin, log, keep booting).
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
//   - KindWASM: returns ErrKindUnsupported — reserved for a later workflow.
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
		return nil, fmt.Errorf("%w: %q (reserved, not implemented)", ErrKindUnsupported, m.Kind)
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
