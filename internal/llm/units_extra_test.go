package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOpenAICompat_StreamHappyPath drives the full StreamChat SSE path: several
// text deltas, a trailing usage-only chunk (stream_options.include_usage), and
// the explicit [DONE] sentinel. It asserts the concatenated text, the terminal
// Usage folded from the usage chunk, and Name().
func TestOpenAICompat_StreamHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
		io.WriteString(w, ": keep-alive comment\n\n")                                   // non-data line, skipped
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n") // second delta
		io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":3}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := NewOpenAICompatClient("test-key", "gpt-x", srv.URL, "openai")
	if c.Name() != "openai:gpt-x" {
		t.Errorf("Name() = %q", c.Name())
	}
	ch, err := c.StreamChat(context.Background(), "sys", "hi")
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	text, usage, done, streamErr := collectTokens(ch)
	if streamErr != nil {
		t.Fatalf("stream error: %v", streamErr)
	}
	if text != "Hello" {
		t.Errorf("text = %q", text)
	}
	if !done {
		t.Error("expected Done")
	}
	if usage == nil || usage.InputTokens != 8 || usage.OutputTokens != 3 {
		t.Errorf("usage = %+v, want {8 3}", usage)
	}
}

// TestOpenAICompat_StreamErrorStatus covers the non-200 branch of StreamChat.
func TestOpenAICompat_StreamErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "bad key")
	}))
	defer srv.Close()

	c := NewOpenAICompatClient("test-key", "m", srv.URL, "openai")
	_, err := c.StreamChat(context.Background(), "", "hi")
	if err == nil {
		t.Fatal("expected an error for status 401")
	}
}

// TestOpenAICompat_StreamCleanCloseNoDONE covers the branch where the body ends
// (EOF) without an explicit [DONE] — the client must still emit a normal Done.
func TestOpenAICompat_StreamCleanCloseNoDONE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
		// no [DONE], just close
	}))
	defer srv.Close()

	c := NewOpenAICompatClient("test-key", "m", srv.URL, "openai")
	ch, err := c.StreamChat(context.Background(), "", "hi")
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	text, _, done, streamErr := collectTokens(ch)
	if streamErr != nil {
		t.Fatalf("stream error: %v", streamErr)
	}
	if text != "x" || !done {
		t.Errorf("text=%q done=%v", text, done)
	}
}

// TestApplyStreamChunk covers the decoder helper directly: a usage-only chunk,
// a text chunk, and a non-JSON keep-alive line.
func TestApplyStreamChunk(t *testing.T) {
	var usage *Usage
	if text, reasoning := applyStreamChunk(`{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":4}}`, &usage); text != "" || reasoning != "" {
		t.Errorf("usage-only chunk: text=%q reasoning=%q, want both empty", text, reasoning)
	}
	if usage == nil || usage.InputTokens != 2 || usage.OutputTokens != 4 {
		t.Fatalf("usage not folded: %+v", usage)
	}
	if text, _ := applyStreamChunk(`{"choices":[{"delta":{"content":"hi"}}]}`, &usage); text != "hi" {
		t.Errorf("text chunk: text=%q", text)
	}
	if text, reasoning := applyStreamChunk(`not json`, &usage); text != "" || reasoning != "" {
		t.Error("non-JSON line must not emit")
	}
	if text, _ := applyStreamChunk(`{"choices":[{"delta":{"content":""}}]}`, &usage); text != "" {
		t.Error("empty content must not emit")
	}
}

// TestSseLineData covers the SSE line parser.
func TestSseLineData(t *testing.T) {
	if d, ok := sseLineData("data: hello"); !ok || d != "hello" {
		t.Errorf("data line = %q,%v", d, ok)
	}
	if _, ok := sseLineData(": comment"); ok {
		t.Error("comment line must not be data")
	}
	if _, ok := sseLineData(""); ok {
		t.Error("blank line must not be data")
	}
}

// TestNewOpenAICompat_MaxTokensEnvOverride covers the env-driven maxTokens
// override branch in newOpenAICompat.
func TestNewOpenAICompat_MaxTokensEnvOverride(t *testing.T) {
	t.Setenv("OPENROUTER_MAX_TOKENS", "77")
	t.Setenv("AISOTROPY_MAX_TOKENS", "")
	c := NewOpenAICompatClient("k", "m", "http://x", "n")
	if c.maxTokens != 77 {
		t.Errorf("maxTokens = %d, want 77 (from OPENROUTER_MAX_TOKENS)", c.maxTokens)
	}

	// Invalid value is ignored → default.
	t.Setenv("OPENROUTER_MAX_TOKENS", "not-a-number")
	c2 := NewOpenAICompatClient("k", "m", "http://x", "n")
	if c2.maxTokens != defaultMaxTokens {
		t.Errorf("maxTokens = %d, want default %d for invalid env", c2.maxTokens, defaultMaxTokens)
	}
}

// TestNewOpenAICompatClient_Defaults covers the empty model/baseURL/name
// defaulting branches.
func TestNewOpenAICompatClient_Defaults(t *testing.T) {
	c := NewOpenAICompatClient("k", "", "", "")
	if c.model != defaultOpenAICompatModel {
		t.Errorf("model = %q, want default", c.model)
	}
	if c.baseURL != openAIBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, openAIBaseURL)
	}
	if c.name != "openai-compat" {
		t.Errorf("name = %q", c.name)
	}
}

// TestNewOpenRouterClient_DefaultModel covers the empty-model default for the
// OpenRouter constructor.
func TestNewOpenRouterClient_DefaultModel(t *testing.T) {
	c := NewOpenRouterClient("k", "")
	if c.model != DefaultOpenRouterModel {
		t.Errorf("model = %q, want %q", c.model, DefaultOpenRouterModel)
	}
	if c.baseURL != openRouterBaseURL {
		t.Errorf("baseURL = %q", c.baseURL)
	}
}

// TestToolParametersSchema covers the three branches of toolParametersSchema:
// a valid declared schema passes through; empty and invalid bytes fall back to
// the permissive open-object schema.
func TestToolParametersSchema(t *testing.T) {
	declared := json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)
	s := toolParametersSchema(declared)
	props, ok := s["properties"].(map[string]any)
	if !ok || props["a"] == nil {
		t.Errorf("declared schema not preserved: %v", s)
	}

	fb := toolParametersSchema(nil)
	if fb[jsonKeyType] != jsonSchemaTypeObject {
		t.Errorf("empty fallback schema = %v", fb)
	}
	if fb["additionalProperties"] == nil {
		t.Errorf("fallback missing additionalProperties: %v", fb)
	}

	// Invalid JSON → same permissive fallback (not a panic).
	bad := toolParametersSchema(json.RawMessage(`{not valid`))
	if bad[jsonKeyType] != jsonSchemaTypeObject {
		t.Errorf("invalid-json fallback = %v", bad)
	}
}

// TestStubValueFor covers every JSON-Schema type arm of stubValueFor.
func TestStubValueFor(t *testing.T) {
	cases := []struct {
		typ  string
		want any
	}{
		{"integer", 0},
		{"number", 0},
		{"boolean", false},
	}
	for _, tc := range cases {
		got := stubValueFor(map[string]any{"type": tc.typ}, "f", "u")
		if got != tc.want {
			t.Errorf("type %q → %v, want %v", tc.typ, got, tc.want)
		}
	}
	if arr, ok := stubValueFor(map[string]any{"type": "array"}, "f", "u").([]any); !ok || len(arr) != 0 {
		t.Error("array type must yield empty []any")
	}
	if obj, ok := stubValueFor(map[string]any{"type": "object"}, "f", "u").(map[string]any); !ok || len(obj) != 0 {
		t.Error("object type must yield empty map")
	}
	// default (string / unspecified): a non-empty canned value folding the field
	// name and user part.
	s, ok := stubValueFor(map[string]any{"type": "string"}, "reply", "ask").(string)
	if !ok || s == "" {
		t.Errorf("string type = %v", s)
	}
	s2 := stubValueFor(map[string]any{}, "reply", "ask").(string)
	if s2 == "" {
		t.Error("unspecified type must still yield a non-empty string")
	}
}

// TestCheapClientFromEnv covers each provider branch plus the two nil paths.
func TestCheapClientFromEnv(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("AISOTROPY_CHEAP_MODEL", "")

	// No cheap model configured → nil.
	if c := CheapClientFromEnv(); c != nil {
		t.Errorf("no cheap model → %v, want nil", c)
	}

	// Cheap model set but no provider key → nil (only stub available).
	t.Setenv("AISOTROPY_CHEAP_MODEL", "cheap-1")
	if c := CheapClientFromEnv(); c != nil {
		t.Errorf("cheap model, no key → %v, want nil", c)
	}

	// OpenRouter takes precedence.
	t.Setenv("OPENROUTER_API_KEY", "or")
	if c := CheapClientFromEnv(); c == nil || c.Name() != "openrouter:cheap-1" {
		t.Errorf("openrouter cheap = %v", c)
	}

	// OpenAI branch (no OpenRouter key).
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "oa")
	if c := CheapClientFromEnv(); c == nil || c.Name() != "openai-cheap:cheap-1" {
		t.Errorf("openai cheap = %v", c)
	}

	// Anthropic branch (no OpenRouter/OpenAI key).
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "an")
	if c := CheapClientFromEnv(); c == nil || c.Name() != "anthropic:cheap-1" {
		t.Errorf("anthropic cheap = %v", c)
	}
}
