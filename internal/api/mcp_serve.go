package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/fugue-labs/gollem/ext/mcp"

	"github.com/Luum-Home/aisotropy/internal/agentloop"
	"github.com/Luum-Home/aisotropy/internal/auth"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// mcp_serve.go is the SERVE half of the Model Context Protocol interop (the
// mirror of internal/api/mcp_tools.go's consume half): it lets Aisotropy PUBLISH
// its own scope-gated tools as an MCP server so any MCP client (Claude Desktop,
// Cursor, LangGraph, another Aisotropy) can list and call them. It closes the
// consume-but-can't-publish asymmetry (arena extension-mechanisms report, Part C
// P1) by wiring the ALREADY-VENDORED gollem ext/mcp primitive.
//
// Constitutional posture (identical to the chat/agent routes, by construction):
//
//   - Scope gate on the serve side. The served tool set is exactly
//     tools.Registry.Permitted(scopes) for the caller's VERIFIED scopes: a tool
//     the caller lacks the scope for is never listed AND a tools/call for it is
//     refused with the same "unknown tool" a genuinely nonexistent tool gets
//     (governance by absence — internal/tools' package doc). The model/client can
//     neither see nor invoke a permission it was never granted.
//   - Tenant isolation via context.Context. Every served call runs under a ctx
//     carrying ONLY the caller's verified (tenant,user) — set through the exact
//     carriers the auth middleware uses (tenantCtxKey/userCtxKey + llm.WithTenant/
//     WithUser + the principal), so a served tool reads identity via
//     TenantFromContext exactly as a chat-route tool does and two tenants' calls
//     never share a scope.
//   - Pre-work quota gate. Each tools/call passes the per-tenant token-bucket
//     limiter BEFORE the tool runs (reject/degrade before spending), the same
//     constitutional gate the chat path applies before the LLM call. A tool that
//     itself calls the model rides the runtime's circuit-broken client, so the
//     breaker still governs its LLM calls.
//   - Fail closed. No authenticator => every request 401. A credential with no
//     usable identity => 401. Unverified scopes (NoAuth) => only always-available
//     (empty-RequiredScope) tools, never the full set.
//
// Transport: a STATELESS streamable-HTTP MCP endpoint. Each POST is a
// self-contained JSON-RPC request whose identity + scopes are re-derived from the
// bearer credential, so there is no session state to leak across callers and the
// scope gate is re-evaluated on every request. This is compatible with gollem's
// own mcp.HTTPClient (which degrades cleanly to direct JSON responses when the
// server advertises no session) and with any spec MCP client.

// MCPServeDeps are the runtime collaborators the MCP serve endpoint dispatches
// through. They are the SAME values the chat/agent routes use, so the served
// surface cannot drift from the native endpoints' guarantees: Registry is the
// scope-gated tool registry, Typed/Executor are the exact typed-then-map dispatch
// pair the agent route runs, Limiter is the per-tenant quota gate, and Authn is
// the runtime's Authenticator (nil => the endpoint denies every request).
type MCPServeDeps struct {
	// Authn verifies each request's credential and yields the (tenant,user,scopes)
	// principal. nil => the handler fails closed (401 on every request), matching
	// the /v1 default-deny posture.
	Authn auth.Authenticator
	// Registry is the scope-gated tool registry; Permitted(scopes) is the served
	// tool set for a request.
	Registry *tools.Registry
	// Typed dispatches typed tools by name from raw JSON args (preferred). nil-safe.
	Typed *TypedToolExecutor
	// Executor runs any tool Typed does not own, via the map[string]string seam
	// (the structured-args fallback). nil-safe.
	Executor agentloop.ToolExecutor
	// Limiter is the per-tenant token-bucket quota gate applied before each served
	// tool call. nil => no quota gate (tests).
	Limiter *memory.Limiter
	// ServerName / ServerVersion identify the server in the MCP initialize
	// handshake. Empty values fall back to sensible defaults.
	ServerName    string
	ServerVersion string
}

// NewMCPServeHandler builds the http.Handler for the scope-gated MCP serve
// endpoint. Mount it on a route OUTSIDE the /v1 Huma middleware chain: the
// handler runs its OWN per-request authentication (deps.Authn) so it is
// self-contained.
func NewMCPServeHandler(deps MCPServeDeps) http.Handler {
	if deps.ServerName == "" {
		deps.ServerName = "aisotropy"
	}
	if deps.ServerVersion == "" {
		deps.ServerVersion = "0.1.0"
	}
	return &mcpServeHandler{deps: deps}
}

type mcpServeHandler struct {
	deps MCPServeDeps
}

// mcpRPCRequest is one inbound JSON-RPC message. ID is kept as RawMessage so a
// response echoes the client's id verbatim (string or number); an absent/null id
// marks a notification (no response body).
type mcpRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpRPCError    `json:"error,omitempty"`
}

// JSON-RPC error codes used by the serve endpoint (standard + a server range
// value for quota rejection).
const (
	rpcInvalidParams  = -32602
	rpcMethodNotFound = -32601
	rpcServerError    = -32000
)

// mcpHTTPAuthRequest adapts a *http.Request to the auth.Request an Authenticator
// inspects, so internal/auth stays free of net/http. A real (JWT) authenticator
// reads the credential from Header and ignores PathTenant/PathUser; NoAuth (which
// trusts path identity) reads the X-Aisotropy-Tenant-Id/-User-Id dev headers, so
// the endpoint is usable under NoAuth for local development while a JWT
// authenticator remains authoritative in production.
type mcpHTTPAuthRequest struct{ r *http.Request }

func (a mcpHTTPAuthRequest) PathTenant() string        { return a.r.Header.Get("X-Aisotropy-Tenant-Id") }
func (a mcpHTTPAuthRequest) PathUser() string          { return a.r.Header.Get("X-Aisotropy-User-Id") }
func (a mcpHTTPAuthRequest) Header(name string) string { return a.r.Header.Get(name) }
func (a mcpHTTPAuthRequest) TLS() *tls.ConnectionState { return a.r.TLS }

func (h *mcpServeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.handlePost(w, r)
	case http.MethodDelete:
		// Stateless server: no per-session state to release.
		w.WriteHeader(http.StatusNoContent)
	default:
		// No server-initiated notification stream in the stateless mode; a GET
		// (the client's optional stream probe) is answered 405 and the gollem
		// client degrades to direct JSON responses.
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (h *mcpServeHandler) handlePost(w http.ResponseWriter, r *http.Request) {
	// Authenticate first (headers only) — fail closed BEFORE decoding or
	// dispatching anything.
	principal, scopes, ok := h.authenticate(w, r)
	if !ok {
		return
	}

	var req mcpRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON-RPC request"}`, http.StatusBadRequest)
		return
	}

	// A notification (no id) carries no reply; acknowledge and stop.
	if isNotification(req.ID) {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// Build the request-scoped, tenant-isolated ctx from the VERIFIED principal —
	// the same carriers the auth middleware sets, so downstream identity reads are
	// identical to the chat path.
	ctx := mcpScopedContext(r.Context(), principal)

	result, rpcErr := h.dispatch(ctx, principal, scopes, req.Method, req.Params)
	h.writeResult(w, req.ID, result, rpcErr)
}

// authenticate verifies the credential and resolves the AUTHORITATIVE scopes. It
// writes the HTTP error itself on failure (returning ok=false). Verified scopes
// gate tools; an unverified principal (NoAuth) yields nil scopes, so only
// always-available tools are ever served to it — never a body-asserted set (MCP
// carries none).
func (h *mcpServeHandler) authenticate(w http.ResponseWriter, r *http.Request) (auth.Principal, []string, bool) {
	if h.deps.Authn == nil {
		http.Error(w, `{"error":"mcp serve: authentication not configured"}`, http.StatusUnauthorized)
		return auth.Principal{}, nil, false
	}
	p, err := h.deps.Authn.Authenticate(r.Context(), mcpHTTPAuthRequest{r: r})
	if err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, auth.ErrForbidden) {
			status = http.StatusForbidden
		}
		http.Error(w, `{"error":"unauthenticated"}`, status)
		return auth.Principal{}, nil, false
	}
	// A usable, isolatable identity is mandatory — reject a credential that yields
	// an empty or malformed tenant/user (the sealed namespace can't be derived).
	if verr := memory.ValidateIdentityComponent("tenant", p.TenantID); verr != nil {
		http.Error(w, `{"error":"credential carries no valid tenant identity"}`, http.StatusUnauthorized)
		return auth.Principal{}, nil, false
	}
	if verr := memory.ValidateIdentityComponent("user", p.UserID); verr != nil {
		http.Error(w, `{"error":"credential carries no valid user identity"}`, http.StatusUnauthorized)
		return auth.Principal{}, nil, false
	}
	var scopes []string
	if p.ScopesVerified {
		scopes = p.Scopes
	}
	return p, scopes, true
}

// mcpScopedContext injects the verified principal's identity into ctx through the
// SAME carriers the auth middleware's withVerifiedPrincipal uses (the local
// tenant/user carriers, internal/llm's OTel carriers, and the principal carrier),
// so a served tool's TenantFromContext/PrincipalFromContext reads are identical
// to a chat-route tool's, and isolation holds by the same construction.
func mcpScopedContext(ctx context.Context, p auth.Principal) context.Context {
	ctx = context.WithValue(ctx, tenantCtxKey{}, p.TenantID)
	ctx = context.WithValue(ctx, userCtxKey{}, p.UserID)
	ctx = llm.WithTenant(ctx, p.TenantID)
	ctx = llm.WithUser(ctx, p.UserID)
	ctx = context.WithValue(ctx, principalCtxKey{}, p)
	return ctx
}

func (h *mcpServeHandler) dispatch(ctx context.Context, p auth.Principal, scopes []string, method string, params json.RawMessage) (any, *mcpRPCError) {
	switch method {
	case "initialize":
		return h.initializeResult(), nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return h.toolsList(scopes), nil
	case "tools/call":
		return h.toolsCall(ctx, p, scopes, params)
	default:
		return nil, &mcpRPCError{Code: rpcMethodNotFound, Message: "method not found: " + method}
	}
}

func (h *mcpServeHandler) initializeResult() *mcp.InitializeResult {
	return &mcp.InitializeResult{
		ProtocolVersion: mcp.ProtocolVersion,
		Capabilities:    mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
		ServerInfo:      &mcp.ServerInfo{Name: h.deps.ServerName, Version: h.deps.ServerVersion},
		Instructions:    "Aisotropy scope-gated tools. Only tools your verified scopes permit are listed or callable.",
	}
}

// toolsList advertises ONLY the scope-permitted tools. A tool with no declared
// schema is advertised with a permissive empty object schema (the same posture
// the model-facing native tool-calling path uses).
func (h *mcpServeHandler) toolsList(scopes []string) map[string]any {
	permitted := h.deps.Registry.Permitted(scopes)
	out := make([]mcp.Tool, 0, len(permitted))
	for _, t := range permitted {
		schema := t.Parameters
		if len(bytes.TrimSpace(schema)) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, mcp.Tool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	return map[string]any{"tools": out}
}

func (h *mcpServeHandler) toolsCall(ctx context.Context, p auth.Principal, scopes []string, params json.RawMessage) (any, *mcpRPCError) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(bytes.TrimSpace(params)) > 0 {
		if err := json.Unmarshal(params, &call); err != nil {
			return nil, &mcpRPCError{Code: rpcInvalidParams, Message: "invalid tools/call params"}
		}
	}
	// SERVE-SIDE SCOPE GATE: a tool outside Permitted(scopes) is refused with the
	// same message a genuinely unknown tool gets, so the caller cannot even learn
	// that a tool it lacks the scope for exists (governance by absence). The tool
	// is NEVER executed.
	if !h.permits(scopes, call.Name) {
		return nil, &mcpRPCError{Code: rpcMethodNotFound, Message: "unknown tool: " + call.Name}
	}
	// PRE-WORK QUOTA GATE: reject before the tool runs so a rate-limited tenant
	// cannot spend on served tool calls (the constitutional "reject/degrade before
	// spending" gate, applied on the serve side too).
	if h.deps.Limiter != nil {
		allowed, err := h.deps.Limiter.Allow(ctx, p.TenantID)
		if err != nil {
			return nil, &mcpRPCError{Code: rpcServerError, Message: "quota check failed"}
		}
		if !allowed {
			return nil, &mcpRPCError{Code: rpcServerError, Message: "rate limit exceeded for tenant"}
		}
	}
	text, err := h.runTool(ctx, call.Name, call.Arguments)
	if err != nil {
		// A typed-tool argument error is surfaced as a tool RESULT flagged isError
		// (so an agentic client can reflect), NOT a protocol error — mirroring the
		// consume-side mcpHandler and the fail-closed typed-tool contract.
		var ae *ToolArgsError
		if errors.As(err, &ae) {
			return &mcp.ToolResult{IsError: true, Content: []mcp.Content{{Type: "text", Text: "invalid arguments: " + ae.Err.Error()}}}, nil
		}
		return &mcp.ToolResult{IsError: true, Content: []mcp.Content{{Type: "text", Text: err.Error()}}}, nil
	}
	return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: text}}}, nil
}

// permits reports whether name is among the caller's scope-permitted tools.
func (h *mcpServeHandler) permits(scopes []string, name string) bool {
	for _, t := range h.deps.Registry.Permitted(scopes) {
		if t.Name == name {
			return true
		}
	}
	return false
}

// runTool dispatches through the SAME typed-then-map executor path the agent
// route uses: a typed tool runs against the model's exact JSON (preserving
// nested/typed fields on this native path), any other tool falls to the map
// executor with flattened string args.
func (h *mcpServeHandler) runTool(ctx context.Context, name string, rawArgs json.RawMessage) (string, error) {
	if h.deps.Typed != nil && h.deps.Typed.Has(name) {
		return h.deps.Typed.Run(ctx, name, rawArgs)
	}
	if h.deps.Executor != nil {
		return h.deps.Executor.Execute(ctx, name, decodeStringArgs(string(rawArgs)))
	}
	return "", fmt.Errorf("tool %q has no handler", name)
}

func (h *mcpServeHandler) writeResult(w http.ResponseWriter, id json.RawMessage, result any, rpcErr *mcpRPCError) {
	resp := mcpRPCResponse{JSONRPC: "2.0", ID: id, Result: result, Error: rpcErr}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// isNotification reports whether a JSON-RPC id is absent or explicitly null (a
// notification, which takes no response).
func isNotification(id json.RawMessage) bool {
	trimmed := bytes.TrimSpace(id)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}
