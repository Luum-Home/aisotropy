// OpenAI-compatible LLM client (OpenRouter, OpenAI, or any endpoint that
// speaks the /chat/completions protocol with SSE streaming).
//
// Implemented with the stdlib (net/http + encoding/json) on purpose: zero new
// dependencies, full control over the payload and the bytes on the wire
// (anti-magic discipline, see docs/vision/thesis.md). OpenRouter is the
// preferred integration point because a single OpenAI-compatible route
// covers OpenRouter, OpenAI, and —via model ids like "google/gemini-..."—
// Google/Gemini too.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultOpenRouterModel: capable, low-cost agentic default (strong at tool
	// use and structured output, which the reflection loop depends on). Override
	// with OPENROUTER_MODEL / AISOTROPY_MODEL.
	DefaultOpenRouterModel   = "qwen/qwen3.7-plus"
	openRouterBaseURL        = "https://openrouter.ai/api/v1"
	openAIBaseURL            = "https://api.openai.com/v1"
	defaultOpenAICompatModel = "gpt-4o-mini"
	// defaultMaxTokens caps the output: avoids OpenRouter's 402 when the
	// key's budget is small, and is good cost-control practice (see
	// docs/architecture/granular-control.md, cost axis). Override:
	// OPENROUTER_MAX_TOKENS / AISOTROPY_MAX_TOKENS.
	defaultMaxTokens = 1024
)

// OpenAICompatClient implements both Client (streaming chat) and ToolCaller
// (native tool-calling, ADR-0012).
var _ ToolCaller = (*OpenAICompatClient)(nil)

// OpenAICompatClient speaks the OpenAI /chat/completions protocol with stream=true.
type OpenAICompatClient struct {
	apiKey    string
	model     string
	baseURL   string
	name      string
	maxTokens int
	http      *http.Client
}

// NewOpenRouterClient creates a client pointing at OpenRouter.
func NewOpenRouterClient(apiKey, model string) *OpenAICompatClient {
	if model == "" {
		model = DefaultOpenRouterModel
	}
	return newOpenAICompat(apiKey, model, openRouterBaseURL, "openrouter")
}

// NewOpenAICompatClient creates a generic OpenAI-compatible client (allows
// overriding baseURL, e.g. direct OpenAI or a self-hosted gateway).
func NewOpenAICompatClient(apiKey, model, baseURL, name string) *OpenAICompatClient {
	if model == "" {
		model = defaultOpenAICompatModel
	}
	if baseURL == "" {
		baseURL = openAIBaseURL
	}
	if name == "" {
		name = "openai-compat"
	}
	return newOpenAICompat(apiKey, model, baseURL, name)
}

func newOpenAICompat(apiKey, model, baseURL, name string) *OpenAICompatClient {
	max := defaultMaxTokens
	for _, env := range []string{"OPENROUTER_MAX_TOKENS", "AISOTROPY_MAX_TOKENS"} {
		if v := os.Getenv(env); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				max = n
				break
			}
		}
	}
	return &OpenAICompatClient{
		apiKey:    apiKey,
		model:     model,
		baseURL:   strings.TrimRight(baseURL, "/"),
		name:      name,
		maxTokens: max,
		http:      &http.Client{Timeout: 120 * time.Second},
	}
}

// Name identifies the implementation (includes the model for observability).
func (c *OpenAICompatClient) Name() string { return c.name + ":" + c.model }

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// StreamChat opens the SSE stream and emits incremental tokens. Respects
// ctx.Done() both on read and on channel send (immediate spend cutoff).
func (c *OpenAICompatClient) StreamChat(ctx context.Context, system, prompt string) (<-chan Token, error) {
	msgs := make([]chatMessage, 0, 2)
	if system != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: system})
	}
	msgs = append(msgs, chatMessage{Role: "user", Content: prompt})

	body, err := json.Marshal(map[string]any{
		"model":      c.model,
		"stream":     true,
		"max_tokens": c.maxTokens,
		"messages":   msgs,
		// include_usage asks the provider to emit one extra SSE chunk right
		// before [DONE] carrying the real prompt/completion token counts (the
		// OpenAI streaming usage contract) — without it, streaming responses
		// carry no usage data at all.
		"stream_options": map[string]any{"include_usage": true},
	})
	if err != nil {
		return nil, fmt.Errorf("openai-compat: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai-compat: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	//nolint:bodyclose // Body is closed on every path: the non-200 branch below
	// closes it explicitly, and the success path hands it to the streaming
	// goroutine, which closes it via defer. bodyclose can't track the latter.
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai-compat: request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("openai-compat: status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	ch := make(chan Token)
	go func() {
		defer close(ch)
		defer func() { _ = resp.Body.Close() }()

		var usage *Usage

		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			if ctx.Err() != nil {
				return
			}
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				sendToken(ctx, ch, Token{Done: true, Usage: usage})
				return
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
				// Usage arrives on its own chunk (empty Choices), right before
				// [DONE], because of the stream_options.include_usage request
				// option set above.
				Usage *struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue // keep-alive / comment / non-JSON line
			}
			if chunk.Usage != nil {
				usage = &Usage{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens}
			}
			if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
				if !sendToken(ctx, ch, Token{Text: chunk.Choices[0].Delta.Content}) {
					return
				}
			}
		}
		// A scanner stop can mean EOF (clean) OR a read error: check sc.Err().
		if err := sc.Err(); err != nil && ctx.Err() == nil {
			// Mid-stream read failure is NOT a clean end — propagate it so the
			// consumer surfaces a truncated response as an error, not success.
			sendToken(ctx, ch, Token{Err: fmt.Errorf("openai-compat: stream read: %w", err)})
			return
		}
		// Stream closed cleanly without an explicit [DONE]: signal normal end.
		sendToken(ctx, ch, Token{Done: true, Usage: usage})
	}()
	return ch, nil
}

// CallTools implements ToolCaller (ADR-0012) over the OpenAI /chat/completions
// protocol: it makes a single NON-streaming request advertising the offered
// tools (OpenAI "function" tools) and returns the tool calls the model chose. A
// tool-call reply is a small structured object, not a token stream, so unlike
// StreamChat this buffers the one response. It honors ctx cancellation via the
// request context.
//
// The tools passed in MUST already be scope-filtered by the caller, and the
// caller MUST re-validate each returned ToolCall.Name against the offered set
// before executing it — this method only relays what the provider returned (see
// the ToolCaller security note; the agent endpoint's runNativeToolLoop enforces
// the re-check).
func (c *OpenAICompatClient) CallTools(ctx context.Context, system, prompt string, toolSpecs []ToolSpec) (ToolCallResult, error) {
	msgs := make([]chatMessage, 0, 2)
	if system != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: system})
	}
	msgs = append(msgs, chatMessage{Role: "user", Content: prompt})

	payload := map[string]any{
		"model":      c.model,
		"max_tokens": c.maxTokens,
		"messages":   msgs,
	}
	if len(toolSpecs) > 0 {
		tools := make([]map[string]any, 0, len(toolSpecs))
		for _, t := range toolSpecs {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					// ToolSpec carries no parameter schema (ADR-0012 keeps the seam
					// minimal), so advertise a permissive object: the model may call
					// the tool with or without free-form arguments.
					"parameters": map[string]any{
						"type":                 "object",
						"properties":           map[string]any{},
						"additionalProperties": true,
					},
				},
			})
		}
		payload["tools"] = tools
		payload["tool_choice"] = "auto"
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return ToolCallResult{}, fmt.Errorf("openai-compat: marshal tool request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ToolCallResult{}, fmt.Errorf("openai-compat: build tool request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return ToolCallResult{}, fmt.Errorf("openai-compat: tool request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return ToolCallResult{}, fmt.Errorf("openai-compat: tool call status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ToolCallResult{}, fmt.Errorf("openai-compat: decode tool response: %w", err)
	}
	if len(out.Choices) == 0 {
		return ToolCallResult{}, nil
	}

	msg := out.Choices[0].Message
	res := ToolCallResult{Message: msg.Content}
	for _, tc := range msg.ToolCalls {
		res.Calls = append(res.Calls, ToolCall{Name: tc.Function.Name, Args: parseToolArgs(tc.Function.Arguments)})
	}
	return res, nil
}

// parseToolArgs converts an OpenAI tool_call arguments JSON string into the
// string-keyed map ToolCall uses. Scalar string values pass through; any
// non-string value is re-encoded as its compact JSON so no information is lost.
// Returns nil for empty/malformed arguments (a tool call with no usable args).
func parseToolArgs(argsJSON string) map[string]string {
	if strings.TrimSpace(argsJSON) == "" {
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
		return nil
	}
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
			continue
		}
		b, _ := json.Marshal(v)
		out[k] = string(b)
	}
	return out
}

// sendToken sends while respecting cancellation; returns false if ctx was cancelled.
func sendToken(ctx context.Context, ch chan<- Token, t Token) bool {
	select {
	case ch <- t:
		return true
	case <-ctx.Done():
		return false
	}
}
