package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/Luum-Home/aisotropy/internal/auth"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// newCalculatorAgentRouter mounts /chat/agent behind NoAuth with the given
// ToolCaller as the client (so the handler takes the native tool-calling path)
// and a registry that offers a single always-permitted "calculator" tool. It is
// the adoption-surface scenario: a third party registers a tool but the model may
// still choose to answer directly.
func newCalculatorAgentRouter(t *testing.T, tc *fakeToolCaller) http.Handler {
	t.Helper()
	router := chi.NewRouter()
	api := humachi.New(router, huma.DefaultConfig("agent-answer-test", "0.1.0"))
	api.UseMiddleware(BindAuthMiddleware(api, auth.NoAuth()))
	reg := tools.NewRegistry(tools.Tool{Name: "calculator", Description: "evaluate an arithmetic expression"})
	RegisterAgent(api, tc, reg, DemoExecutor{}, (*TypedToolExecutor)(nil), nil, noop.NewTracerProvider(), 3, nil, nil)
	return router
}

// postAgentDecoded drives /chat/agent and decodes the full response body,
// including the additive final_answer field.
func postAgentDecoded(t *testing.T, h http.Handler, tenant, user, prompt string) (int, struct {
	TenantID    string      `json:"tenant_id"`
	Steps       []AgentStep `json:"steps"`
	FinalAnswer string      `json:"final_answer"`
}) {
	t.Helper()
	var out struct {
		TenantID    string      `json:"tenant_id"`
		Steps       []AgentStep `json:"steps"`
		FinalAnswer string      `json:"final_answer"`
	}
	body, err := json.Marshal(map[string]any{"prompt": prompt, "product_id": "analytics-v1", "session_id": "s1"})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/tenant/"+tenant+"/user/"+user+"/chat/agent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode agent body %q: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, out
}

// TestChatAgent_ReturnsFinalAnswerWhenNoToolCalled is the adoption-surface
// regression the whole change exists to close (native ToolCaller path): a third
// party asks "what's the capital of Australia" with a calculator tool registered;
// the model correctly answers "Canberra" WITHOUT calling the tool. The endpoint
// must return HTTP 200 with empty steps AND the model's final answer in
// final_answer — previously the answer was discarded and the bot had nothing to
// show. Driven end-to-end through the real Huma handler, no network (scripted
// ToolCaller over a stub client).
func TestChatAgent_ReturnsFinalAnswerWhenNoToolCalled(t *testing.T) {
	tc := &fakeToolCaller{
		Client: llm.NewStubClient(),
		// No Calls: the model answers directly. Message is gollem's final
		// free-text output (core.Agent[string].Run's result.Output).
		script: []llm.ToolCallResult{{Message: "Canberra"}},
	}
	h := newCalculatorAgentRouter(t, tc)

	code, out := postAgentDecoded(t, h, "acme", "alice", "what's the capital of Australia")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(out.Steps) != 0 {
		t.Fatalf("steps = %+v, want none (the model answered without a tool)", out.Steps)
	}
	if out.FinalAnswer != "Canberra" {
		t.Fatalf("final_answer = %q, want %q", out.FinalAnswer, "Canberra")
	}
	if out.TenantID != "acme" {
		t.Fatalf("tenant_id = %q, want acme (identity from the path, not the body)", out.TenantID)
	}
}

// TestChatAgent_ToolCallStillPopulatesSteps proves no regression on the
// tool-calling case: when the model DOES call a tool, Steps is still populated
// exactly as before (the tool, its args, and result), and the closing answer the
// model synthesizes after the tool result lands in final_answer.
func TestChatAgent_ToolCallStillPopulatesSteps(t *testing.T) {
	tc := &fakeToolCaller{
		Client: llm.NewStubClient(),
		script: []llm.ToolCallResult{
			{Calls: []llm.ToolCall{{Name: "calculator", Args: map[string]string{"expr": "2+2"}}}},
			{Message: "The result is 4."}, // closing answer after the tool result
		},
	}
	h := newCalculatorAgentRouter(t, tc)

	code, out := postAgentDecoded(t, h, "acme", "alice", "what is 2+2")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(out.Steps) != 1 {
		t.Fatalf("steps = %+v, want exactly the one calculator step", out.Steps)
	}
	if out.Steps[0].Tool != "calculator" {
		t.Fatalf("step tool = %q, want calculator", out.Steps[0].Tool)
	}
	if out.Steps[0].Args["expr"] != "2+2" {
		t.Fatalf("step args = %+v, want expr=2+2", out.Steps[0].Args)
	}
	if out.Steps[0].Result == "" {
		t.Fatal("expected the tool step to carry a result")
	}
	if out.FinalAnswer != "The result is 4." {
		t.Fatalf("final_answer = %q, want the post-tool closing answer", out.FinalAnswer)
	}
}
