package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fugue-labs/gollem/ext/mcp"

	"github.com/Luum-Home/aisotropy/internal/agentloop"
	"github.com/Luum-Home/aisotropy/internal/auth"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// mcp_serve_test.go covers the SERVE half of the MCP interop (mcp_serve.go):
// the scope-gated JSON-RPC endpoint that publishes Aisotropy's own tools to
// any MCP client. It exercises the fail-closed auth gate, the serve-side
// scope gate (governance by absence), the pre-work quota gate, and every
// JSON-RPC method (initialize/ping/tools-list/tools-call) plus the typed vs.
// map tool dispatch paths, mirroring the constitutional gates already proven
// for the chat/agent routes.

// ---- fixtures ---------------------------------------------------------

// mcpTestRegistry returns a scope-gated tool set: one always-available tool,
// one admin-gated tool (never offered without the "admin" scope), one typed
// tool with an explicit schema, one tool the map executor owns, and one
// permitted tool NEITHER dispatcher owns (exercises runTool's "no handler"
// branch).
func mcpTestRegistry() *tools.Registry {
	return tools.NewRegistry(
		tools.Tool{Name: "open_tool", Description: "always available"},
		tools.Tool{Name: "admin_tool", Description: "gated", RequiredScope: "admin"},
		tools.Tool{
			Name:        "typed_tool",
			Description: "typed",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"fail":{"type":"boolean"}}}`),
		},
		tools.Tool{Name: "map_tool", Description: "map fallback"},
		tools.Tool{Name: "orphan_tool", Description: "permitted, no handler"},
	)
}

// mcpTypedArgs is the typed_tool argument shape: fail triggers a *ToolArgsError
// (the args-reflect signal), plainFail triggers a plain runtime error, distinct
// branches in toolsCall's error mapping.
type mcpTypedArgs struct {
	Fail      bool `json:"fail"`
	PlainFail bool `json:"plain_fail"`
}

func mcpTypedHandler(_ context.Context, raw json.RawMessage) (string, error) {
	var args mcpTypedArgs
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", err
		}
	}
	if args.Fail {
		return "", &ToolArgsError{Tool: "typed_tool", Err: errors.New("missing required field")}
	}
	if args.PlainFail {
		return "", errors.New("typed tool exploded")
	}
	return "typed-ok", nil
}

// mcpRecordingExecutor is a scripted agentloop.ToolExecutor: it records the
// call and returns either a canned error or a canned/default result, so
// map_tool's success and failure branches are both directly observable.
type mcpRecordingExecutor struct {
	calls    int
	lastName string
	lastArgs map[string]string
	err      error
	result   string
}

func (e *mcpRecordingExecutor) Execute(_ context.Context, name string, args map[string]string) (string, error) {
	e.calls++
	e.lastName = name
	e.lastArgs = args
	if e.err != nil {
		return "", e.err
	}
	if e.result != "" {
		return e.result, nil
	}
	return "map-ok:" + name, nil
}

// mcpTestDeps builds MCPServeDeps wired with mcpTestRegistry, a typed
// executor owning "typed_tool", and the given authenticator/limiter/executor.
func mcpTestDeps(authn auth.Authenticator, limiter *memory.Limiter, executor agentloop.ToolExecutor) MCPServeDeps {
	typed := NewTypedToolExecutor(map[string]TypedToolHandler{"typed_tool": mcpTypedHandler})
	return MCPServeDeps{
		Authn:    authn,
		Registry: mcpTestRegistry(),
		Typed:    typed,
		Executor: executor,
		Limiter:  limiter,
	}
}

// mcpPost issues a POST to the MCP serve handler with the given tenant/user
// dev headers (consumed by auth.NoAuth via mcpHTTPAuthRequest) and body.
func mcpPost(t *testing.T, h http.Handler, tenant, user string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	if tenant != "" {
		req.Header.Set("X-Aisotropy-Tenant-Id", tenant)
	}
	if user != "" {
		req.Header.Set("X-Aisotropy-User-Id", user)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// mcpPostCtx is mcpPost with a caller-supplied request context (used to force
// ctx.Err() inside the quota gate).
func mcpPostCtx(t *testing.T, h http.Handler, ctx context.Context, tenant, user string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/mcp", bytes.NewReader(body))
	if tenant != "" {
		req.Header.Set("X-Aisotropy-Tenant-Id", tenant)
	}
	if user != "" {
		req.Header.Set("X-Aisotropy-User-Id", user)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// mcpRPCCall marshals a JSON-RPC request body with the given id (nil for a
// notification), method, and raw params.
func mcpRPCCall(t *testing.T, id any, method string, params any) []byte {
	t.Helper()
	req := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		req["id"] = id
	}
	if params != nil {
		req["params"] = params
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal rpc call: %v", err)
	}
	return b
}

func decodeRPCResponse(t *testing.T, rec *httptest.ResponseRecorder) mcpRPCResponse {
	t.Helper()
	var resp mcpRPCResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode rpc response %q: %v", rec.Body.String(), err)
	}
	return resp
}

func decodeToolResult(t *testing.T, result any) mcp.ToolResult {
	t.Helper()
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("remarshal result: %v", err)
	}
	var tr mcp.ToolResult
	if err := json.Unmarshal(b, &tr); err != nil {
		t.Fatalf("decode tool result %q: %v", string(b), err)
	}
	return tr
}

// ---- NewMCPServeHandler -------------------------------------------------

// TestNewMCPServeHandler_DefaultsServerNameVersion covers the empty->default
// fallback for ServerName/ServerVersion, observed through the initialize
// handshake's serverInfo.
func TestNewMCPServeHandler_DefaultsServerNameVersion(t *testing.T) {
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, &mcpRecordingExecutor{}))
	rec := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "1", "initialize", nil))
	resp := decodeRPCResponse(t, rec)
	if resp.Error != nil {
		t.Fatalf("initialize returned an error: %+v", resp.Error)
	}
	var ir mcp.InitializeResult
	b, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(b, &ir); err != nil {
		t.Fatalf("decode InitializeResult: %v", err)
	}
	if ir.ServerInfo == nil || ir.ServerInfo.Name != "aisotropy" || ir.ServerInfo.Version != "0.1.0" {
		t.Fatalf("default serverInfo = %+v, want name=aisotropy version=0.1.0", ir.ServerInfo)
	}
	if ir.Instructions == "" {
		t.Error("initialize result should carry non-empty Instructions")
	}
	if ir.Capabilities.Tools == nil {
		t.Error("initialize result should advertise tool capability")
	}
}

// TestNewMCPServeHandler_CustomServerNameVersion covers the pass-through case:
// non-empty ServerName/ServerVersion are used verbatim, not overridden.
func TestNewMCPServeHandler_CustomServerNameVersion(t *testing.T) {
	deps := mcpTestDeps(auth.NoAuth(), nil, &mcpRecordingExecutor{})
	deps.ServerName = "custom-server"
	deps.ServerVersion = "9.9.9"
	h := NewMCPServeHandler(deps)
	rec := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "1", "initialize", nil))
	resp := decodeRPCResponse(t, rec)
	var ir mcp.InitializeResult
	b, _ := json.Marshal(resp.Result)
	_ = json.Unmarshal(b, &ir)
	if ir.ServerInfo == nil || ir.ServerInfo.Name != "custom-server" || ir.ServerInfo.Version != "9.9.9" {
		t.Fatalf("custom serverInfo = %+v, want name=custom-server version=9.9.9", ir.ServerInfo)
	}
}

// ---- ServeHTTP method dispatch ------------------------------------------

func TestMCPServeHandler_ServeHTTP_DeleteIsNoContent(t *testing.T) {
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, &mcpRecordingExecutor{}))
	req := httptest.NewRequest(http.MethodDelete, "/mcp", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", rec.Code)
	}
}

func TestMCPServeHandler_ServeHTTP_GetIsMethodNotAllowed(t *testing.T) {
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, &mcpRecordingExecutor{}))
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "method not allowed") {
		t.Errorf("GET body = %q, want it to mention method not allowed", rec.Body.String())
	}
}

// ---- authenticate --------------------------------------------------------

func TestMCPServeHandler_Authenticate_NilAuthnFailsClosed(t *testing.T) {
	h := NewMCPServeHandler(mcpTestDeps(nil, nil, &mcpRecordingExecutor{}))
	rec := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "1", "ping", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("nil-Authn status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "authentication not configured") {
		t.Errorf("nil-Authn body = %q, want it to mention authentication not configured", rec.Body.String())
	}
}

func TestMCPServeHandler_Authenticate_ForbiddenMapsTo403(t *testing.T) {
	h := NewMCPServeHandler(mcpTestDeps(fakeAuth{err: auth.ErrForbidden}, nil, &mcpRecordingExecutor{}))
	rec := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "1", "ping", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("ErrForbidden status = %d, want 403", rec.Code)
	}
}

func TestMCPServeHandler_Authenticate_GenericErrorMapsTo401(t *testing.T) {
	h := NewMCPServeHandler(mcpTestDeps(fakeAuth{err: errors.New("boom")}, nil, &mcpRecordingExecutor{}))
	rec := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "1", "ping", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("generic-error status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unauthenticated") {
		t.Errorf("generic-error body = %q, want it to mention unauthenticated", rec.Body.String())
	}
}

func TestMCPServeHandler_Authenticate_EmptyTenantRejected(t *testing.T) {
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, &mcpRecordingExecutor{}))
	rec := mcpPost(t, h, "", "alice", mcpRPCCall(t, "1", "ping", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("empty-tenant status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no valid tenant identity") {
		t.Errorf("empty-tenant body = %q, want it to mention tenant identity", rec.Body.String())
	}
}

func TestMCPServeHandler_Authenticate_EmptyUserRejected(t *testing.T) {
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, &mcpRecordingExecutor{}))
	rec := mcpPost(t, h, "acme", "", mcpRPCCall(t, "1", "ping", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("empty-user status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no valid user identity") {
		t.Errorf("empty-user body = %q, want it to mention user identity", rec.Body.String())
	}
}

// TestMCPServeHandler_Authenticate_UnverifiedScopesYieldNilScopes covers the
// ScopesVerified=false branch: NoAuth's principal never activates the scope
// gate with its own Scopes field, so only always-available tools are listed —
// exercised end to end via tools/list.
func TestMCPServeHandler_Authenticate_UnverifiedScopesYieldNilScopes(t *testing.T) {
	// NoAuth never populates Scopes at all; assert indirectly that admin_tool
	// is never listed for it regardless of headers (there is no scope channel
	// on MCP requests at all — this is the "MCP carries none" invariant).
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, &mcpRecordingExecutor{}))
	rec := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "1", "tools/list", nil))
	resp := decodeRPCResponse(t, rec)
	b, _ := json.Marshal(resp.Result)
	var out struct {
		Tools []mcp.Tool `json:"tools"`
	}
	_ = json.Unmarshal(b, &out)
	for _, tool := range out.Tools {
		if tool.Name == "admin_tool" {
			t.Fatalf("NoAuth must never list a scope-gated tool, got %+v", out.Tools)
		}
	}
}

// ---- handlePost: body decoding, notifications --------------------------

func TestMCPServeHandler_HandlePost_InvalidJSONBody(t *testing.T) {
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, &mcpRecordingExecutor{}))
	rec := mcpPost(t, h, "acme", "alice", []byte("{not json"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid JSON-RPC request") {
		t.Errorf("invalid JSON body = %q", rec.Body.String())
	}
}

// TestMCPServeHandler_HandlePost_NotificationNoID covers the id-absent
// notification path: 202 Accepted, no response body, and — critically — no
// dispatch happens (the recording executor is never invoked even though the
// method names a real tool call).
func TestMCPServeHandler_HandlePost_NotificationNoID(t *testing.T) {
	exec := &mcpRecordingExecutor{}
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, exec))
	rec := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, nil, "tools/call", map[string]any{"name": "map_tool"}))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("notification status = %d, want 202", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("notification body = %q, want empty", rec.Body.String())
	}
	if exec.calls != 0 {
		t.Errorf("notification must not dispatch, executor calls = %d", exec.calls)
	}
}

// TestMCPServeHandler_HandlePost_NotificationNullID covers the explicit
// "id":null variant of a notification (also no response).
func TestMCPServeHandler_HandlePost_NotificationNullID(t *testing.T) {
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, &mcpRecordingExecutor{}))
	body := []byte(`{"jsonrpc":"2.0","id":null,"method":"ping"}`)
	rec := mcpPost(t, h, "acme", "alice", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("null-id notification status = %d, want 202", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("null-id notification body = %q, want empty", rec.Body.String())
	}
}

// TestMCPServeHandler_HandlePost_EchoesRequestID proves writeResult echoes the
// caller's id verbatim (string id here) alongside a successful result.
func TestMCPServeHandler_HandlePost_EchoesRequestID(t *testing.T) {
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, &mcpRecordingExecutor{}))
	rec := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "req-42", "ping", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("ping status = %d, want 200", rec.Code)
	}
	resp := decodeRPCResponse(t, rec)
	if string(resp.ID) != `"req-42"` {
		t.Errorf("response id = %s, want \"req-42\"", resp.ID)
	}
	if resp.JSONRPC != "2.0" {
		t.Errorf("response jsonrpc = %q, want 2.0", resp.JSONRPC)
	}
}

// ---- dispatch: method routing -------------------------------------------

func TestMCPServeHandler_Dispatch_Ping(t *testing.T) {
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, &mcpRecordingExecutor{}))
	rec := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "1", "ping", nil))
	resp := decodeRPCResponse(t, rec)
	if resp.Error != nil {
		t.Fatalf("ping returned error: %+v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok || len(m) != 0 {
		t.Fatalf("ping result = %#v, want empty object", resp.Result)
	}
}

func TestMCPServeHandler_Dispatch_UnknownMethod(t *testing.T) {
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, &mcpRecordingExecutor{}))
	rec := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "1", "totally/bogus", nil))
	resp := decodeRPCResponse(t, rec)
	if resp.Error == nil || resp.Error.Code != rpcMethodNotFound {
		t.Fatalf("unknown method error = %+v, want code %d", resp.Error, rpcMethodNotFound)
	}
	if !strings.Contains(resp.Error.Message, "totally/bogus") {
		t.Errorf("unknown method message = %q, want it to name the method", resp.Error.Message)
	}
}

// ---- tools/list: scope gating + schema default --------------------------

func TestMCPServeHandler_ToolsList_ScopeGatingAndSchemaDefault(t *testing.T) {
	// Unverified (NoAuth) principal -> no admin scope -> admin_tool absent;
	// open_tool has no declared Parameters, so it gets the default
	// permissive object schema; typed_tool keeps its explicit schema.
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, &mcpRecordingExecutor{}))
	rec := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "1", "tools/list", nil))
	resp := decodeRPCResponse(t, rec)
	b, _ := json.Marshal(resp.Result)
	var out struct {
		Tools []mcp.Tool `json:"tools"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode tools/list result: %v", err)
	}

	byName := map[string]mcp.Tool{}
	for _, tool := range out.Tools {
		byName[tool.Name] = tool
	}
	if _, ok := byName["admin_tool"]; ok {
		t.Fatalf("admin_tool must not be listed without the admin scope, got %+v", out.Tools)
	}
	open, ok := byName["open_tool"]
	if !ok {
		t.Fatal("open_tool must be listed (no RequiredScope)")
	}
	if strings.TrimSpace(string(open.InputSchema)) != `{"type":"object"}` {
		t.Errorf("open_tool default schema = %s, want {\"type\":\"object\"}", open.InputSchema)
	}
	typedT, ok := byName["typed_tool"]
	if !ok {
		t.Fatal("typed_tool must be listed")
	}
	var schema map[string]any
	if err := json.Unmarshal(typedT.InputSchema, &schema); err != nil {
		t.Fatalf("decode typed_tool schema: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("typed_tool schema = %v, want its explicit object schema preserved", schema)
	}
}

func TestMCPServeHandler_ToolsList_AdminScopeGrantsAdminTool(t *testing.T) {
	authn := fakeAuth{principal: auth.Principal{TenantID: "acme", UserID: "alice", Scopes: []string{"admin"}, ScopesVerified: true}}
	h := NewMCPServeHandler(mcpTestDeps(authn, nil, &mcpRecordingExecutor{}))
	rec := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "1", "tools/list", nil))
	resp := decodeRPCResponse(t, rec)
	b, _ := json.Marshal(resp.Result)
	var out struct {
		Tools []mcp.Tool `json:"tools"`
	}
	_ = json.Unmarshal(b, &out)
	var found bool
	for _, tool := range out.Tools {
		if tool.Name == "admin_tool" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a verified admin principal must be offered admin_tool, got %+v", out.Tools)
	}
}

// ---- tools/call: params decoding + serve-side scope gate -----------------

func TestMCPServeHandler_ToolsCall_InvalidParams(t *testing.T) {
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, &mcpRecordingExecutor{}))
	body := []byte(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":"not-an-object"}`)
	rec := mcpPost(t, h, "acme", "alice", body)
	resp := decodeRPCResponse(t, rec)
	if resp.Error == nil || resp.Error.Code != rpcInvalidParams {
		t.Fatalf("invalid params error = %+v, want code %d", resp.Error, rpcInvalidParams)
	}
}

func TestMCPServeHandler_ToolsCall_UnknownToolRefused(t *testing.T) {
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, &mcpRecordingExecutor{}))
	rec := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "1", "tools/call", map[string]any{"name": "does_not_exist"}))
	resp := decodeRPCResponse(t, rec)
	if resp.Error == nil || resp.Error.Code != rpcMethodNotFound {
		t.Fatalf("unknown tool error = %+v, want code %d", resp.Error, rpcMethodNotFound)
	}
	if !strings.Contains(resp.Error.Message, "does_not_exist") {
		t.Errorf("unknown tool message = %q, want it to name the tool", resp.Error.Message)
	}
}

// TestMCPServeHandler_ToolsCall_ScopeGatedToolRefusedIndistinguishably proves
// governance by absence: a caller lacking the admin scope gets the exact same
// "unknown tool" error for admin_tool as for a genuinely nonexistent tool, and
// the tool is never executed.
func TestMCPServeHandler_ToolsCall_ScopeGatedToolRefusedIndistinguishably(t *testing.T) {
	exec := &mcpRecordingExecutor{}
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, exec))
	rec := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "1", "tools/call", map[string]any{"name": "admin_tool"}))
	resp := decodeRPCResponse(t, rec)
	if resp.Error == nil || resp.Error.Code != rpcMethodNotFound || !strings.Contains(resp.Error.Message, "unknown tool: admin_tool") {
		t.Fatalf("scope-gated tool error = %+v, want unknown-tool refusal", resp.Error)
	}
	if exec.calls != 0 {
		t.Errorf("scope-gated tool must never execute, calls = %d", exec.calls)
	}
}

// ---- tools/call: quota gate ----------------------------------------------

// TestMCPServeHandler_ToolsCall_NoLimiterSkipsGate covers Limiter==nil: the
// call proceeds straight to execution (used throughout the other tests too,
// asserted explicitly here).
func TestMCPServeHandler_ToolsCall_NoLimiterSkipsGate(t *testing.T) {
	exec := &mcpRecordingExecutor{}
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, exec))
	rec := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "1", "tools/call", map[string]any{"name": "map_tool"}))
	resp := decodeRPCResponse(t, rec)
	if resp.Error != nil {
		t.Fatalf("nil-limiter call returned error: %+v", resp.Error)
	}
	if exec.calls != 1 {
		t.Fatalf("nil-limiter call should execute the tool, calls = %d", exec.calls)
	}
}

// TestMCPServeHandler_ToolsCall_QuotaExhaustedRefusesBeforeRunning covers the
// constitutional "reject/degrade before spending" gate on the serve side: a
// drained bucket refuses the call with rpcServerError and the tool NEVER
// runs.
func TestMCPServeHandler_ToolsCall_QuotaExhaustedRefusesBeforeRunning(t *testing.T) {
	wm := newTestWorkingMemory(t)
	limiter := wm.NewLimiter(0, 1) // capacity 1, no refill
	exec := &mcpRecordingExecutor{}
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), limiter, exec))

	// First call drains the single token.
	rec1 := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "1", "tools/call", map[string]any{"name": "map_tool"}))
	resp1 := decodeRPCResponse(t, rec1)
	if resp1.Error != nil {
		t.Fatalf("first call should be allowed: %+v", resp1.Error)
	}
	if exec.calls != 1 {
		t.Fatalf("first call should execute, calls = %d", exec.calls)
	}

	// Second call is over quota -> refused, tool not executed again.
	rec2 := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "2", "tools/call", map[string]any{"name": "map_tool"}))
	resp2 := decodeRPCResponse(t, rec2)
	if resp2.Error == nil || resp2.Error.Code != rpcServerError || !strings.Contains(resp2.Error.Message, "rate limit exceeded") {
		t.Fatalf("second call error = %+v, want rate-limit refusal", resp2.Error)
	}
	if exec.calls != 1 {
		t.Errorf("quota-denied call must not execute the tool, calls = %d", exec.calls)
	}
}

// TestMCPServeHandler_ToolsCall_QuotaCheckErrorSurfaces covers Limiter.Allow's
// own error path (distinct from a clean "denied"): a request whose context is
// already cancelled makes the quota check itself fail, mapped to
// rpcServerError "quota check failed".
func TestMCPServeHandler_ToolsCall_QuotaCheckErrorSurfaces(t *testing.T) {
	wm := newTestWorkingMemory(t)
	limiter := wm.NewLimiter(10, 10)
	exec := &mcpRecordingExecutor{}
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), limiter, exec))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := mcpRPCCall(t, "1", "tools/call", map[string]any{"name": "map_tool"})
	rec := mcpPostCtx(t, h, ctx, "acme", "alice", body)
	resp := decodeRPCResponse(t, rec)
	if resp.Error == nil || resp.Error.Code != rpcServerError || !strings.Contains(resp.Error.Message, "quota check failed") {
		t.Fatalf("cancelled-ctx call error = %+v, want quota-check-failed", resp.Error)
	}
	if exec.calls != 0 {
		t.Errorf("a failed quota check must not execute the tool, calls = %d", exec.calls)
	}
}

// ---- tools/call: typed tool dispatch --------------------------------------

func TestMCPServeHandler_ToolsCall_TypedToolSuccess(t *testing.T) {
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, &mcpRecordingExecutor{}))
	rec := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "1", "tools/call", map[string]any{"name": "typed_tool", "arguments": map[string]any{}}))
	resp := decodeRPCResponse(t, rec)
	if resp.Error != nil {
		t.Fatalf("typed_tool success returned error: %+v", resp.Error)
	}
	tr := decodeToolResult(t, resp.Result)
	if tr.IsError {
		t.Fatalf("typed_tool success should not be IsError, got %+v", tr)
	}
	if len(tr.Content) != 1 || tr.Content[0].Text != "typed-ok" {
		t.Fatalf("typed_tool result content = %+v, want [typed-ok]", tr.Content)
	}
}

func TestMCPServeHandler_ToolsCall_TypedToolArgsErrorIsResultNotProtocolError(t *testing.T) {
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, &mcpRecordingExecutor{}))
	rec := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "1", "tools/call", map[string]any{"name": "typed_tool", "arguments": map[string]any{"fail": true}}))
	resp := decodeRPCResponse(t, rec)
	if resp.Error != nil {
		t.Fatalf("a *ToolArgsError must surface as a tool RESULT, not a protocol error: %+v", resp.Error)
	}
	tr := decodeToolResult(t, resp.Result)
	if !tr.IsError {
		t.Fatal("ToolArgsError result must be flagged IsError")
	}
	if len(tr.Content) != 1 || !strings.HasPrefix(tr.Content[0].Text, "invalid arguments: ") {
		t.Fatalf("ToolArgsError content = %+v, want an 'invalid arguments: ...' prefix", tr.Content)
	}
}

func TestMCPServeHandler_ToolsCall_TypedToolPlainErrorIsResult(t *testing.T) {
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, &mcpRecordingExecutor{}))
	rec := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "1", "tools/call", map[string]any{"name": "typed_tool", "arguments": map[string]any{"plain_fail": true}}))
	resp := decodeRPCResponse(t, rec)
	if resp.Error != nil {
		t.Fatalf("a plain runtime error must surface as a tool RESULT: %+v", resp.Error)
	}
	tr := decodeToolResult(t, resp.Result)
	if !tr.IsError || len(tr.Content) != 1 || tr.Content[0].Text != "typed tool exploded" {
		t.Fatalf("plain-error result = %+v, want IsError with the raw error text", tr)
	}
}

// ---- tools/call: map executor fallback dispatch ---------------------------

func TestMCPServeHandler_ToolsCall_MapToolSuccess(t *testing.T) {
	exec := &mcpRecordingExecutor{result: "map-result"}
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, exec))
	rec := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "1", "tools/call", map[string]any{"name": "map_tool", "arguments": map[string]any{"k": "v"}}))
	resp := decodeRPCResponse(t, rec)
	if resp.Error != nil {
		t.Fatalf("map_tool success returned error: %+v", resp.Error)
	}
	tr := decodeToolResult(t, resp.Result)
	if tr.IsError || len(tr.Content) != 1 || tr.Content[0].Text != "map-result" {
		t.Fatalf("map_tool result = %+v, want [map-result]", tr)
	}
	if exec.lastName != "map_tool" || exec.lastArgs["k"] != "v" {
		t.Errorf("executor received name=%q args=%v, want map_tool/{k:v}", exec.lastName, exec.lastArgs)
	}
}

func TestMCPServeHandler_ToolsCall_MapToolErrorIsResult(t *testing.T) {
	exec := &mcpRecordingExecutor{err: errors.New("map tool blew up")}
	h := NewMCPServeHandler(mcpTestDeps(auth.NoAuth(), nil, exec))
	rec := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "1", "tools/call", map[string]any{"name": "map_tool"}))
	resp := decodeRPCResponse(t, rec)
	if resp.Error != nil {
		t.Fatalf("map_tool runtime failure must surface as a tool RESULT: %+v", resp.Error)
	}
	tr := decodeToolResult(t, resp.Result)
	if !tr.IsError || len(tr.Content) != 1 || tr.Content[0].Text != "map tool blew up" {
		t.Fatalf("map_tool error result = %+v, want IsError with the raw error text", tr)
	}
}

// TestMCPServeHandler_ToolsCall_NoHandlerToolIsResult covers runTool's final
// fallback: a tool the registry permits but NEITHER dispatcher owns (Typed
// lacks it, Executor is nil) surfaces as a tool RESULT naming the tool, not a
// panic or protocol error.
func TestMCPServeHandler_ToolsCall_NoHandlerToolIsResult(t *testing.T) {
	deps := mcpTestDeps(auth.NoAuth(), nil, nil) // nil executor
	h := NewMCPServeHandler(deps)
	rec := mcpPost(t, h, "acme", "alice", mcpRPCCall(t, "1", "tools/call", map[string]any{"name": "orphan_tool"}))
	resp := decodeRPCResponse(t, rec)
	if resp.Error != nil {
		t.Fatalf("no-handler tool must surface as a tool RESULT: %+v", resp.Error)
	}
	tr := decodeToolResult(t, resp.Result)
	if !tr.IsError || len(tr.Content) != 1 || !strings.Contains(tr.Content[0].Text, `"orphan_tool"`) {
		t.Fatalf("no-handler result = %+v, want IsError naming orphan_tool", tr)
	}
}

// ---- mcpScopedContext -----------------------------------------------------

// TestMcpScopedContext_CarriesVerifiedIdentity proves the served ctx uses the
// SAME carriers the auth middleware's withVerifiedPrincipal uses, so a served
// tool's TenantFromContext/PrincipalFromContext reads are identical to a
// chat-route tool's.
func TestMcpScopedContext_CarriesVerifiedIdentity(t *testing.T) {
	p := auth.Principal{TenantID: "acme", UserID: "alice", Scopes: []string{"read"}, ScopesVerified: true}
	ctx := mcpScopedContext(context.Background(), p)

	tenant, ok := TenantFromContext(ctx)
	if !ok || tenant != "acme" {
		t.Fatalf("TenantFromContext = (%q,%v), want (acme,true)", tenant, ok)
	}
	gotP, ok := PrincipalFromContext(ctx)
	if !ok || gotP.TenantID != p.TenantID || gotP.UserID != p.UserID || gotP.ScopesVerified != p.ScopesVerified {
		t.Fatalf("PrincipalFromContext = (%+v,%v), want (%+v,true)", gotP, ok, p)
	}
}

// ---- isNotification -------------------------------------------------------

func TestIsNotification(t *testing.T) {
	cases := []struct {
		name string
		id   json.RawMessage
		want bool
	}{
		{"nil id", nil, true},
		{"empty id", json.RawMessage(``), true},
		{"whitespace id", json.RawMessage(`   `), true},
		{"explicit null", json.RawMessage(`null`), true},
		{"string id", json.RawMessage(`"abc"`), false},
		{"numeric id", json.RawMessage(`1`), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isNotification(c.id); got != c.want {
				t.Errorf("isNotification(%q) = %v, want %v", c.id, got, c.want)
			}
		})
	}
}

// ---- mcpHTTPAuthRequest accessors -----------------------------------------

// TestMcpHTTPAuthRequest_Accessors directly exercises the auth.Request adapter
// methods (PathTenant/PathUser/Header/TLS), including the non-nil TLS branch
// that no test above triggers via HTTP (httptest requests are plaintext).
func TestMcpHTTPAuthRequest_Accessors(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("X-Aisotropy-Tenant-Id", "acme")
	req.Header.Set("X-Aisotropy-User-Id", "alice")
	req.Header.Set("X-Custom", "value")
	req.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}

	a := mcpHTTPAuthRequest{r: req}
	if a.PathTenant() != "acme" {
		t.Errorf("PathTenant() = %q, want acme", a.PathTenant())
	}
	if a.PathUser() != "alice" {
		t.Errorf("PathUser() = %q, want alice", a.PathUser())
	}
	if a.Header("X-Custom") != "value" {
		t.Errorf("Header(X-Custom) = %q, want value", a.Header("X-Custom"))
	}
	if a.TLS() == nil || a.TLS().Version != tls.VersionTLS13 {
		t.Errorf("TLS() = %+v, want the connection state set on the request", a.TLS())
	}
}

func TestMcpHTTPAuthRequest_TLSNilWhenPlaintext(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	a := mcpHTTPAuthRequest{r: req}
	if a.TLS() != nil {
		t.Errorf("TLS() on a plaintext request = %+v, want nil", a.TLS())
	}
}
