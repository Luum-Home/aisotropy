package aisotropy

// mcpServerSpec records a stdio MCP server a consumer registered via
// WithMCPServer. The runtime connects it at Run time, discovers its tools, and
// exposes each as a scope-gated Aisotropy tool (ADR-0016 lever #2).
type mcpServerSpec struct {
	requiredScope string
	command       string
	args          []string
}

// WithMCPServer registers a Model Context Protocol (MCP) server whose tools the
// runtime exposes as Aisotropy tools — the polyglot-execution path (ADR-0016
// lever #2). The server is any executable speaking MCP over stdio (written in
// ANY language: a TypeScript/Python/Rust tool server, an off-the-shelf
// `@modelcontextprotocol/server-*`, etc.); the Aisotropy core stays Go and keeps
// owning tenant scoping and identity.
//
// At Run time the runtime spawns `command args...`, lists the server's tools,
// and for each one registers a scope-gated tool carrying the server's JSON
// Schema (so the model sees the real shape and arguments are validated —
// violations reflect, as for any typed tool) that dispatches to the server. All
// of a server's tools share requiredScope (empty = always available); the double
// scope-gate applies exactly as to native tools. The subprocess lifecycle is the
// runtime's: it is closed on graceful shutdown.
//
// Connection is best-effort: a server that fails to start or list its tools is
// logged and skipped, never preventing the runtime from booting. Call once per
// server; it accumulates. The consumer never imports any MCP library — command,
// args, and scope are the entire surface.
func WithMCPServer(requiredScope, command string, args ...string) Option {
	return func(a *App) {
		a.mcpServers = append(a.mcpServers, mcpServerSpec{
			requiredScope: requiredScope,
			command:       command,
			args:          append([]string(nil), args...),
		})
	}
}
