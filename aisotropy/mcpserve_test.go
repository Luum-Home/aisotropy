package aisotropy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	gmcp "github.com/fugue-labs/gollem/ext/mcp"

	"github.com/Luum-Home/aisotropy/internal/api"
)

// mcpServeSecret is a 32-byte HS256 signing secret (auth.MinHS256SecretBytes) so
// the JWT authenticator accepts the tokens these tests mint.
var mcpServeSecret = []byte("mcp-serve-test-secret-0123456789")

// noArgs is the argument struct for the tools these tests register (they take no
// input; a served tool's behavior is proven by its scope gate and the identity
// it observes, not by its arguments).
type noArgs struct{}

// startMCPServeApp boots an App with the MCP serve endpoint enabled, a JWT
// authenticator, and four typed tools whose RequiredScope spans the gate:
// whoami/public_ping (always available), write_note (needs "write"),
// admin_action (needs "admin"). whoami echoes the ctx-derived tenant so the
// isolation test can prove which identity a served call ran under.
func startMCPServeApp(t *testing.T) string {
	t.Helper()
	addr := freeAddr(t)

	whoami := func(ctx context.Context, _ noArgs) (string, error) {
		tn, _ := api.TenantFromContext(ctx)
		return tn, nil
	}
	ok := func(reply string) func(context.Context, noArgs) (string, error) {
		return func(_ context.Context, _ noArgs) (string, error) { return reply, nil }
	}

	app := New(
		WithAddr(addr),
		WithLLM(fastStub()),
		WithRateLimit(1e9, 1e9),
		WithAuthenticator(NewJWTAuthenticator(mcpServeSecret)),
		WithMCPServe("/mcp"),
		WithTypedTool[noArgs]("whoami", "returns the caller's tenant", "", whoami),
		WithTypedTool[noArgs]("public_ping", "always available", "", ok("pong")),
		WithTypedTool[noArgs]("write_note", "requires the write scope", "write", ok("noted")),
		WithTypedTool[noArgs]("admin_action", "requires the admin scope", "admin", ok("done")),
	)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	base := "http://" + addr
	waitForServer(t, base+"/healthz")

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("Run returned error on shutdown: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("Run did not return after ctx cancellation")
		}
	})
	return base
}

// mcpToken mints an HS256 JWT carrying the given identity and space-delimited
// scope claim, valid for an hour.
func mcpToken(t *testing.T, tenant, user, scope string) string {
	t.Helper()
	claims := map[string]any{
		"tenant_id": tenant,
		"user_id":   user,
		"exp":       time.Now().Add(time.Hour).Unix(),
	}
	if scope != "" {
		claims["scope"] = scope
	}
	return makeHS256(t, mcpServeSecret, claims)
}

// rpcEnvelope is the JSON-RPC response envelope the serve endpoint returns.
type rpcEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// mcpPost issues one JSON-RPC request against the serve endpoint with an optional
// bearer token, returning the HTTP status and (on 200) the decoded envelope.
func mcpPost(t *testing.T, base, token, method string, params any) (int, rpcEnvelope) {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, base+"/mcp", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, rpcEnvelope{}
	}
	var env rpcEnvelope
	if derr := json.NewDecoder(resp.Body).Decode(&env); derr != nil {
		t.Fatalf("decode envelope: %v", derr)
	}
	return resp.StatusCode, env
}

// listedToolNames returns the sorted tool names a caller with the given token
// sees from tools/list.
func listedToolNames(t *testing.T, base, token string) []string {
	t.Helper()
	status, env := mcpPost(t, base, token, "tools/list", nil)
	if status != http.StatusOK {
		t.Fatalf("tools/list status = %d, want 200", status)
	}
	if env.Error != nil {
		t.Fatalf("tools/list rpc error: %+v", env.Error)
	}
	var res struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(env.Result, &res); err != nil {
		t.Fatalf("unmarshal tools/list result: %v", err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	return names
}

// TestWithMCPServe_DefaultPath covers WithMCPServe's empty-path default branch:
// an empty path mounts at "/mcp" rather than leaving the spec unset or mounting
// at an empty route.
func TestWithMCPServe_DefaultPath(t *testing.T) {
	app := New(WithLLM(fastStub()), WithMCPServe(""))
	if app.mcpServe == nil {
		t.Fatal("WithMCPServe(\"\") must still install a mcpServeSpec")
	}
	if app.mcpServe.path != "/mcp" {
		t.Errorf("mcpServe.path = %q, want the default \"/mcp\"", app.mcpServe.path)
	}
}

// TestMCPServe_ScopeGate proves the constitutional scope gate governs the serve
// side: a served MCP client with scopes S sees ONLY tools.Registry.Permitted(S)
// in tools/list, and a tools/call for a tool outside S is refused (never run).
func TestMCPServe_ScopeGate(t *testing.T) {
	base := startMCPServeApp(t)

	// A caller with the "write" scope: sees the always-available tools + write_note,
	// but NOT admin_action.
	writeTok := mcpToken(t, "tenantW", "userW", "write")
	got := listedToolNames(t, base, writeTok)
	want := []string{"public_ping", "whoami", "write_note"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("write-scope tools/list = %v, want %v (admin_action must be absent, write_note present)", got, want)
	}

	// A caller with NO scopes: only the always-available tools.
	noScopeTok := mcpToken(t, "tenantN", "userN", "")
	gotNone := listedToolNames(t, base, noScopeTok)
	wantNone := []string{"public_ping", "whoami"}
	if fmt.Sprint(gotNone) != fmt.Sprint(wantNone) {
		t.Fatalf("no-scope tools/list = %v, want %v", gotNone, wantNone)
	}

	// Calling a non-permitted tool is REFUSED as an unknown tool (governance by
	// absence) — the write-scoped caller cannot invoke admin_action.
	status, env := mcpPost(t, base, writeTok, "tools/call", map[string]any{"name": "admin_action", "arguments": map[string]any{}})
	if status != http.StatusOK {
		t.Fatalf("tools/call status = %d, want 200 (rpc-level refusal)", status)
	}
	if env.Error == nil {
		t.Fatalf("call to non-permitted admin_action was NOT refused: %s", string(env.Result))
	}

	// A permitted tool call succeeds and runs the handler.
	status, env = mcpPost(t, base, writeTok, "tools/call", map[string]any{"name": "write_note", "arguments": map[string]any{}})
	if status != http.StatusOK || env.Error != nil {
		t.Fatalf("permitted write_note call failed: status=%d err=%+v", status, env.Error)
	}
	if txt := toolResultText(t, env.Result); txt != "noted" {
		t.Fatalf("write_note result = %q, want %q", txt, "noted")
	}

	// An unauthenticated request is rejected (fail closed), never served tools.
	if status, _ := mcpPost(t, base, "", "tools/list", nil); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated tools/list status = %d, want 401", status)
	}
}

// TestMCPServe_TenantIsolation proves that two tenants' served tool calls never
// cross: each caller's whoami runs under a ctx carrying ONLY its own verified
// tenant, under heavy concurrency (run with -race).
func TestMCPServe_TenantIsolation(t *testing.T) {
	base := startMCPServeApp(t)
	tokA := mcpToken(t, "tenantA", "userA", "")
	tokB := mcpToken(t, "tenantB", "userB", "")

	const perTenant = 60
	var wg sync.WaitGroup
	errs := make(chan error, 2*perTenant)

	call := func(token, wantTenant string) {
		defer wg.Done()
		status, env := mcpPost(t, base, token, "tools/call", map[string]any{"name": "whoami", "arguments": map[string]any{}})
		if status != http.StatusOK || env.Error != nil {
			errs <- fmt.Errorf("whoami failed: status=%d err=%+v", status, env.Error)
			return
		}
		if got := toolResultText(t, env.Result); got != wantTenant {
			errs <- fmt.Errorf("served whoami saw tenant %q, want %q — ISOLATION BREACH", got, wantTenant)
		}
	}

	for i := 0; i < perTenant; i++ {
		wg.Add(2)
		go call(tokA, "tenantA")
		go call(tokB, "tenantB")
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// toolResultText extracts the concatenated text of an MCP ToolResult payload.
func toolResultText(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var res struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("unmarshal tool result: %v", err)
	}
	if len(res.Content) == 0 {
		return ""
	}
	return res.Content[0].Text
}

// TestMCPServe_GollemClientInterop drives the endpoint with gollem's OWN MCP
// streamable-HTTP client (the vendored ext/mcp primitive's client half), proving
// the served endpoint is a real, spec-compatible MCP server — a served MCP client
// with scope "write" lists exactly its permitted tools and calling a
// non-permitted tool is refused.
func TestMCPServe_GollemClientInterop(t *testing.T) {
	base := startMCPServeApp(t)
	token := mcpToken(t, "tenantG", "userG", "write")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := gmcp.NewHTTPClient(ctx, base+"/mcp",
		gmcp.WithHeaders(map[string]string{"Authorization": "Bearer " + token}))
	if err != nil {
		t.Fatalf("gollem MCP client connect: %v", err)
	}
	defer client.Close()

	list, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("gollem ListTools: %v", err)
	}
	names := make([]string, 0, len(list))
	for _, tl := range list {
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	want := []string{"public_ping", "whoami", "write_note"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Fatalf("gollem-client tools/list = %v, want %v", names, want)
	}

	// A permitted call succeeds.
	res, err := client.CallTool(ctx, "write_note", map[string]any{})
	if err != nil {
		t.Fatalf("gollem CallTool(write_note): %v", err)
	}
	if txt := res.TextContent(); txt != "noted" {
		t.Fatalf("gollem write_note result = %q, want %q", txt, "noted")
	}

	// A non-permitted tool is refused (never executed).
	if _, cerr := client.CallTool(ctx, "admin_action", map[string]any{}); cerr == nil {
		t.Fatal("gollem CallTool(admin_action) succeeded, want refusal (tool not permitted)")
	}
}
