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

// captureToolServer is an httptest server that records the last request body
// and replies with the supplied JSON (status 200) or a canned error.
func toolServer(t *testing.T, status int, respJSON string, capture *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		b, _ := io.ReadAll(r.Body)
		if capture != nil {
			*capture = string(b)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respJSON)
	}))
}

// TestCallTools_ReturnsToolCalls covers the full CallTools happy path: the
// request advertises the offered tools (with a declared Parameters schema and a
// permissive fallback), and the returned tool calls round-trip through both the
// flattened Args and the preserved RawArgs.
func TestCallTools_ReturnsToolCalls(t *testing.T) {
	resp := `{"choices":[{"message":{"content":"calling","tool_calls":[
		{"function":{"name":"get_weather","arguments":"{\"city\":\"Paris\",\"days\":3}"}},
		{"function":{"name":"noargs","arguments":""}}
	]}}]}`
	var body string
	srv := toolServer(t, http.StatusOK, resp, &body)
	defer srv.Close()

	c := NewOpenAICompatClient("test-key", "m", srv.URL, "test")
	tools := []ToolSpec{
		{Name: "get_weather", Description: "weather", Parameters: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`)},
		{Name: "noargs", Description: "no declared params"},
	}
	res, err := c.CallTools(context.Background(), "sys", "what is the weather", tools)
	if err != nil {
		t.Fatalf("CallTools: %v", err)
	}
	if res.Message != "calling" {
		t.Errorf("Message = %q", res.Message)
	}
	if len(res.Calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(res.Calls))
	}
	if res.Calls[0].Name != "get_weather" {
		t.Errorf("call0 name = %q", res.Calls[0].Name)
	}
	if res.Calls[0].Args["city"] != "Paris" {
		t.Errorf("call0 city arg = %q", res.Calls[0].Args["city"])
	}
	// days is a non-string value: re-encoded as compact JSON in the flattened map.
	if res.Calls[0].Args["days"] != "3" {
		t.Errorf("call0 days arg = %q, want \"3\"", res.Calls[0].Args["days"])
	}
	if string(res.Calls[0].RawArgs) != `{"city":"Paris","days":3}` {
		t.Errorf("call0 RawArgs = %s", res.Calls[0].RawArgs)
	}
	// noargs: empty arguments → nil Args and nil RawArgs.
	if res.Calls[1].Args != nil {
		t.Errorf("call1 Args = %v, want nil", res.Calls[1].Args)
	}
	if res.Calls[1].RawArgs != nil {
		t.Errorf("call1 RawArgs = %v, want nil", res.Calls[1].RawArgs)
	}

	// Request body must carry the tool envelope: declared schema verbatim + the
	// permissive fallback for the tool with no Parameters.
	if !strings.Contains(body, `"tool_choice":"auto"`) {
		t.Errorf("body missing tool_choice: %s", body)
	}
	if !strings.Contains(body, `"additionalProperties":true`) {
		t.Errorf("body missing permissive fallback schema: %s", body)
	}
	if !strings.Contains(body, `"name":"get_weather"`) {
		t.Errorf("body missing tool name: %s", body)
	}
}

// TestCallTools_NoTools covers buildToolCallPayload's early return: with no
// tools offered, the payload carries no "tools"/"tool_choice" keys, and a
// content-only reply comes back with zero Calls.
func TestCallTools_NoTools(t *testing.T) {
	var body string
	srv := toolServer(t, http.StatusOK, `{"choices":[{"message":{"content":"just text"}}]}`, &body)
	defer srv.Close()

	c := NewOpenAICompatClient("test-key", "m", srv.URL, "test")
	res, err := c.CallTools(context.Background(), "", "hi", nil)
	if err != nil {
		t.Fatalf("CallTools: %v", err)
	}
	if res.Message != "just text" || len(res.Calls) != 0 {
		t.Errorf("res = %+v", res)
	}
	if strings.Contains(body, "tools") {
		t.Errorf("body must not advertise tools: %s", body)
	}
}

// TestCallTools_NoChoices covers the empty-choices branch (provider returned a
// well-formed but choice-less response).
func TestCallTools_NoChoices(t *testing.T) {
	srv := toolServer(t, http.StatusOK, `{"choices":[]}`, nil)
	defer srv.Close()

	c := NewOpenAICompatClient("test-key", "m", srv.URL, "test")
	res, err := c.CallTools(context.Background(), "s", "p", nil)
	if err != nil {
		t.Fatalf("CallTools: %v", err)
	}
	if len(res.Calls) != 0 || res.Message != "" {
		t.Errorf("res = %+v, want empty", res)
	}
}

// TestCallTools_ErrorStatus covers the non-200 branch of sendToolCallRequest.
func TestCallTools_ErrorStatus(t *testing.T) {
	srv := toolServer(t, http.StatusTooManyRequests, `{"error":"rate limited"}`, nil)
	defer srv.Close()

	c := NewOpenAICompatClient("test-key", "m", srv.URL, "test")
	_, err := c.CallTools(context.Background(), "s", "p", nil)
	if err == nil {
		t.Fatal("expected an error for status 429")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error = %v, want it to mention 429", err)
	}
}

// TestCallTools_BadJSON covers the decode-failure branch of sendToolCallRequest.
func TestCallTools_BadJSON(t *testing.T) {
	srv := toolServer(t, http.StatusOK, `not json at all`, nil)
	defer srv.Close()

	c := NewOpenAICompatClient("test-key", "m", srv.URL, "test")
	_, err := c.CallTools(context.Background(), "s", "p", nil)
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("expected a decode error, got %v", err)
	}
}

// TestCallTools_TransportError covers the http.Do failure branch (server closed).
func TestCallTools_TransportError(t *testing.T) {
	srv := toolServer(t, http.StatusOK, `{}`, nil)
	url := srv.URL
	srv.Close() // close immediately so the connection is refused

	c := NewOpenAICompatClient("test-key", "m", url, "test")
	_, err := c.CallTools(context.Background(), "s", "p", nil)
	if err == nil {
		t.Fatal("expected a transport error against a closed server")
	}
}

// TestParseToolArgs covers the arg-flattening helper directly across the shapes
// it must handle: empty, malformed, empty-object, string passthrough, and
// non-string re-encoding.
func TestParseToolArgs(t *testing.T) {
	if got := parseToolArgs(""); got != nil {
		t.Errorf("empty = %v, want nil", got)
	}
	if got := parseToolArgs("{bad"); got != nil {
		t.Errorf("malformed = %v, want nil", got)
	}
	if got := parseToolArgs("{}"); got != nil {
		t.Errorf("empty object = %v, want nil", got)
	}
	got := parseToolArgs(`{"s":"str","n":42,"arr":[1,2],"obj":{"k":"v"}}`)
	if got["s"] != "str" {
		t.Errorf("string passthrough = %q", got["s"])
	}
	if got["n"] != "42" {
		t.Errorf("number = %q, want 42", got["n"])
	}
	if got["arr"] != "[1,2]" {
		t.Errorf("array = %q", got["arr"])
	}
	if got["obj"] != `{"k":"v"}` {
		t.Errorf("object = %q", got["obj"])
	}
}

// TestRawToolArgs covers the raw-args preservation helper: valid JSON passes
// through verbatim; empty and invalid inputs collapse to nil.
func TestRawToolArgs(t *testing.T) {
	if got := rawToolArgs("  "); got != nil {
		t.Errorf("blank = %v, want nil", got)
	}
	if got := rawToolArgs("{not valid"); got != nil {
		t.Errorf("invalid = %v, want nil", got)
	}
	if got := rawToolArgs(`{"a":1}`); string(got) != `{"a":1}` {
		t.Errorf("valid = %s", got)
	}
}
