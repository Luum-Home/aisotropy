package aisotropy

import (
	"net/http"

	"github.com/Luum-Home/aisotropy/internal/api"
)

// mcpServeSpec records that a consumer opted into the MCP SERVE endpoint via
// WithMCPServe, and the route path it mounts on. The runtime builds the handler
// at Run time from the live tool registry, typed handlers, executor, limiter, and
// the configured Authenticator (see buildMCPServeHandler).
type mcpServeSpec struct {
	path string
}

// WithMCPServe publishes Aisotropy's OWN scope-gated tools as a Model Context
// Protocol (MCP) server, mounted at path (default "/mcp" when empty). It closes
// the consume-but-can't-publish asymmetry: WithMCPServer (note the trailing "r")
// CONSUMES another server's tools, while WithMCPServe SERVES this runtime's tools
// so any MCP client (Claude Desktop, Cursor, LangGraph, another Aisotropy) can
// list and call them.
//
// The endpoint is a stateless streamable-HTTP MCP server that runs its OWN
// per-request authentication using the SAME Authenticator the /v1 routes use
// (WithAuthenticator), and it honors every constitutional gate the chat/agent
// routes do, by construction:
//
//   - Scope gate: the served tool set is exactly the caller's scope-permitted
//     tools (tools.Registry.Permitted of the VERIFIED scopes). A tool the caller
//     lacks the scope for is neither listed nor callable — a call to it is refused
//     as an unknown tool (governance by absence).
//   - Tenant isolation: each served call runs under a ctx carrying only the
//     caller's verified (tenant,user), so a served tool reads identity via
//     context exactly as a chat tool does; two tenants' calls never cross.
//   - Quota gate: each tool call passes the per-tenant rate limiter before the
//     tool runs (reject before spending); a tool that calls the model still rides
//     the runtime's circuit-broken client.
//
// Fail-closed defaults: with no Authenticator configured the endpoint rejects
// every request (401). Under WithAuthenticator(NoAuth()) — which trusts caller
// identity — the client supplies identity via the X-Aisotropy-Tenant-Id /
// X-Aisotropy-User-Id headers and only always-available (unscoped) tools are
// served, since NoAuth does not vouch for scopes. Unset (the default) => the
// endpoint is not registered and the runtime stays MCP consume-only.
func WithMCPServe(path string) Option {
	return func(a *App) {
		if path == "" {
			path = "/mcp"
		}
		a.mcpServe = &mcpServeSpec{path: path}
	}
}

// buildMCPServeHandler assembles the MCP serve http.Handler from the live runtime
// collaborators — the exact scope-gated registry, typed-then-map dispatch pair,
// and per-tenant limiter the agent route uses — plus the configured
// Authenticator (nil => the handler fails closed, denying every request).
func (a *App) buildMCPServeHandler(rt *appRuntime) http.Handler {
	return api.NewMCPServeHandler(api.MCPServeDeps{
		Authn:         a.authenticator,
		Registry:      rt.toolRegistry,
		Typed:         api.NewTypedToolExecutor(rt.typedHandlers),
		Executor:      rt.executor,
		Limiter:       rt.limiter,
		ServerName:    "aisotropy",
		ServerVersion: "0.1.0",
	})
}
