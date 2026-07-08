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
				sendToken(ctx, ch, Token{Done: true})
				return
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue // keep-alive / comment / non-JSON line
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
		sendToken(ctx, ch, Token{Done: true})
	}()
	return ch, nil
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
