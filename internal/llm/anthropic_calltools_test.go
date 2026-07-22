package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// anthropicToolServer is an httptest server standing in for api.anthropic.com's
// non-streaming Messages endpoint: it records the last request body and replies
// with the supplied JSON Message (status 200) or a canned error. It mirrors
// openai_compat_calltools_test.go's toolServer for the Anthropic SDK path.
func anthropicToolServer(t *testing.T, status int, respJSON string, capture *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-Api-Key"); got != "test-key" {
			t.Errorf("X-Api-Key = %q, want test-key", got)
		}
		b, _ := io.ReadAll(r.Body)
		if capture != nil {
			*capture = string(b)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respJSON)
	}))
}

// TestAnthropicClient_ImplementsToolCaller is the compile-time + runtime proof
// that AnthropicClient satisfies the ToolCaller seam (ADR-0063), so
// client.(ToolCaller) at the agent endpoint succeeds for a Claude deployment.
func TestAnthropicClient_ImplementsToolCaller(t *testing.T) {
	var c Client = NewAnthropicClient("k", "")
	if _, ok := c.(ToolCaller); !ok {
		t.Fatal("AnthropicClient does not implement ToolCaller")
	}
}

// TestAnthropicCallTools_ReturnsToolCalls covers the CallTools happy path
// against a mocked Messages endpoint: the request advertises the offered tools
// (a declared input_schema and a permissive fallback), and a returned tool_use
// block round-trips through both the flattened Args and the preserved RawArgs —
// single-shot, mirroring TestCallTools_ReturnsToolCalls on the OpenAI path.
func TestAnthropicCallTools_ReturnsToolCalls(t *testing.T) {
	resp := `{
		"id":"msg_1","type":"message","role":"assistant","model":"claude-test",
		"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5},
		"content":[
			{"type":"text","text":"checking"},
			{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Paris","days":3}}
		]
	}`
	var body string
	srv := anthropicToolServer(t, http.StatusOK, resp, &body)
	defer srv.Close()

	c := newTestAnthropicClient(srv.URL, "claude-test")
	tools := []ToolSpec{
		{Name: "get_weather", Description: "weather", Parameters: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)},
		{Name: "noargs", Description: "no declared params"},
	}
	res, err := c.CallTools(context.Background(), "sys", "what is the weather", tools)
	if err != nil {
		t.Fatalf("CallTools: %v", err)
	}
	if res.Message != "checking" {
		t.Errorf("Message = %q, want %q", res.Message, "checking")
	}
	if len(res.Calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(res.Calls))
	}
	if res.Calls[0].Name != "get_weather" {
		t.Errorf("call name = %q", res.Calls[0].Name)
	}
	if res.Calls[0].Args["city"] != "Paris" {
		t.Errorf("city arg = %q", res.Calls[0].Args["city"])
	}
	// days is a non-string value: re-encoded as compact JSON in the flattened map.
	if res.Calls[0].Args["days"] != "3" {
		t.Errorf("days arg = %q, want \"3\"", res.Calls[0].Args["days"])
	}
	if string(res.Calls[0].RawArgs) != `{"city":"Paris","days":3}` {
		t.Errorf("RawArgs = %s, want {\"city\":\"Paris\",\"days\":3}", res.Calls[0].RawArgs)
	}
	// Provider token accounting is surfaced on ToolCallResult.Usage (Anthropic
	// bills thinking inside output_tokens, so ReasoningTokens stays 0).
	if res.Usage == nil || res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 5 {
		t.Errorf("res.Usage = %+v, want {InputTokens:10 OutputTokens:5 ReasoningTokens:0}", res.Usage)
	}
	if res.Usage != nil && res.Usage.ReasoningTokens != 0 {
		t.Errorf("ReasoningTokens = %d, want 0 (Anthropic bills thinking inside output_tokens)", res.Usage.ReasoningTokens)
	}

	// Request body must carry the native tool envelope: the declared schema
	// (name + input_schema with the city property + required) AND the permissive
	// fallback for the tool with no Parameters. Only offered tools appear — the
	// structural scope-gate-by-absence.
	if !strings.Contains(body, `"name":"get_weather"`) {
		t.Errorf("body missing offered tool name: %s", body)
	}
	if !strings.Contains(body, `"input_schema"`) {
		t.Errorf("body missing input_schema envelope: %s", body)
	}
	if !strings.Contains(body, `"required":["city"]`) {
		t.Errorf("body missing declared required field: %s", body)
	}
	if !strings.Contains(body, `"name":"noargs"`) {
		t.Errorf("body missing untyped tool name: %s", body)
	}
}

// TestAnthropicCallTools_NoTools covers the no-tools branch: with no tools
// offered the request carries no "tools" key, and a text-only reply comes back
// with zero Calls and the text folded into Message.
func TestAnthropicCallTools_NoTools(t *testing.T) {
	resp := `{"id":"m","type":"message","role":"assistant","model":"claude-test",
		"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2},
		"content":[{"type":"text","text":"just text"}]}`
	var body string
	srv := anthropicToolServer(t, http.StatusOK, resp, &body)
	defer srv.Close()

	c := newTestAnthropicClient(srv.URL, "claude-test")
	res, err := c.CallTools(context.Background(), "", "hi", nil)
	if err != nil {
		t.Fatalf("CallTools: %v", err)
	}
	if res.Message != "just text" || len(res.Calls) != 0 {
		t.Errorf("res = %+v, want {Message:\"just text\" Calls:[]}", res)
	}
	if strings.Contains(body, `"tools"`) {
		t.Errorf("body must not advertise tools when none offered: %s", body)
	}
}

// TestAnthropicCallTools_MultipleToolUse covers a reply with more than one
// tool_use block (parallel tool calls): each block becomes its own ToolCall,
// preserving order.
func TestAnthropicCallTools_MultipleToolUse(t *testing.T) {
	resp := `{"id":"m","type":"message","role":"assistant","model":"claude-test",
		"stop_reason":"tool_use","usage":{"input_tokens":4,"output_tokens":6},
		"content":[
			{"type":"tool_use","id":"t1","name":"alpha","input":{"x":"1"}},
			{"type":"tool_use","id":"t2","name":"beta","input":{}}
		]}`
	srv := anthropicToolServer(t, http.StatusOK, resp, nil)
	defer srv.Close()

	c := newTestAnthropicClient(srv.URL, "claude-test")
	res, err := c.CallTools(context.Background(), "s", "p", []ToolSpec{{Name: "alpha"}, {Name: "beta"}})
	if err != nil {
		t.Fatalf("CallTools: %v", err)
	}
	if len(res.Calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(res.Calls))
	}
	if res.Calls[0].Name != "alpha" || res.Calls[1].Name != "beta" {
		t.Errorf("calls out of order: %q, %q", res.Calls[0].Name, res.Calls[1].Name)
	}
	if res.Calls[0].Args["x"] != "1" {
		t.Errorf("alpha x arg = %q", res.Calls[0].Args["x"])
	}
	// Anthropic sends an empty JSON OBJECT ({}) for a no-arg tool_use (unlike the
	// OpenAI path, where empty arguments arrive as ""). parseToolArgs collapses an
	// empty object to a nil Args map, while rawToolArgs preserves the valid "{}"
	// bytes verbatim — the faithful contract for the Anthropic shape.
	if res.Calls[1].Args != nil {
		t.Errorf("beta Args = %v, want nil", res.Calls[1].Args)
	}
	if string(res.Calls[1].RawArgs) != "{}" {
		t.Errorf("beta RawArgs = %q, want {}", res.Calls[1].RawArgs)
	}
}

// TestAnthropicCallTools_ErrorStatus covers the non-2xx branch: an API error
// surfaces as an error from CallTools, not a silent empty result.
func TestAnthropicCallTools_ErrorStatus(t *testing.T) {
	srv := anthropicToolServer(t, http.StatusTooManyRequests, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, nil)
	defer srv.Close()

	c := newTestAnthropicClient(srv.URL, "claude-test")
	_, err := c.CallTools(context.Background(), "s", "p", []ToolSpec{{Name: "t"}})
	if err == nil {
		t.Fatal("expected an error for status 429")
	}
}

// TestAnthropicToolInputSchema covers the schema translation helper directly:
// a declared schema surfaces its properties/required; an empty spec falls back
// to a permissive empty-object schema.
func TestAnthropicToolInputSchema(t *testing.T) {
	got := anthropicToolInputSchema(json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`))
	if got.Properties == nil {
		t.Fatal("declared schema: Properties is nil")
	}
	if len(got.Required) != 1 || got.Required[0] != "a" {
		t.Errorf("declared schema Required = %v, want [a]", got.Required)
	}

	fallback := anthropicToolInputSchema(nil)
	m, ok := fallback.Properties.(map[string]any)
	if !ok || len(m) != 0 {
		t.Errorf("fallback Properties = %#v, want empty map[string]any", fallback.Properties)
	}
	if len(fallback.Required) != 0 {
		t.Errorf("fallback Required = %v, want empty", fallback.Required)
	}
}
