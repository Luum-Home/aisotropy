// agnes_client.go — a usage-capturing llm.Client that speaks the OpenAI
// /chat/completions streaming protocol against agnes-2.0-flash.
//
// WHY THIS EXISTS (and is not "reimplementing the provider"): aisotropy's public
// façade exposes App.Turn, whose ChatOutput carries only {Reply, TenantID, Stub}
// — it deliberately does NOT surface per-turn provider token usage to the
// embedder. The live-arena contract, however, requires input_tokens/output_tokens
// from the provider's reported usage. The only public seam that lets an embedder
// observe usage is aisotropy.WithLLM(client), which injects an llm.Client whose
// StreamChat the whole pipeline (GollemReflect → ChatCore) drives. So we inject a
// client that (a) reads OPENAI_* env to select the real agnes model — exactly the
// provider-agnostic selection ADR-0009 describes, WithLLM being that seam — and
// (b) records the provider's stream-final usage so the adapter can report it.
//
// stdlib only (net/http + encoding/json): zero new dependencies, MIT-clean.
package main

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
	"sync"
	"time"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

// compile-time proof the injected type satisfies the public façade's LLMClient.
var _ aisotropy.LLMClient = (*agnesClient)(nil)

// agnesClient calls agnes-2.0-flash over its OpenAI-compatible endpoint and
// accumulates provider-reported usage across every StreamChat it serves (a
// single App.Turn may drive StreamChat more than once via the reflection loop;
// summing is the honest per-turn spend).
type agnesClient struct {
	apiKey    string
	model     string
	baseURL   string
	maxTokens int
	http      *http.Client

	mu           sync.Mutex
	inputTokens  int
	outputTokens int
	sawUsage     bool
}

// newAgnesClient builds the client from OPENAI_* env. The key is read from the
// environment and never logged or embedded in error strings.
//
// ARENA_STUB=1 (stubMode, app.go) short-circuits this entirely: it clears the
// real-backend-tier env vars this process may have inherited from run.sh
// (clearBackendTierEnv) and returns (nil, nil) instead of erroring on a
// missing OPENAI_API_KEY. Every mode's call site already does
// `agnes, err := newAgnesClient(); if err != nil { return err }` followed by
// `newApp(agnes, ...)` / `setUsage(out, agnes)` — both nil-safe (app.go) — so
// a nil, nil-error return here is a normal, successful "run offline" signal
// that needs no change at any of the 11 call sites: newApp then omits
// WithLLM entirely, so aisotropy.New() falls back to its own env-driven
// llm.FromEnv(), which (with every provider key unset) resolves to
// aisotropy's NATIVE keyless stub client.
func newAgnesClient() (*agnesClient, error) {
	if stubMode() {
		clearBackendTierEnv()
		return nil, nil
	}
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is not set")
	}
	base := strings.TrimRight(os.Getenv("OPENAI_BASE_URL"), "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "agnes-2.0-flash"
	}
	max := 1024
	if v := os.Getenv("AISOTROPY_MAX_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			max = n
		}
	}
	return &agnesClient{
		apiKey:    key,
		model:     model,
		baseURL:   base,
		maxTokens: max,
		http:      &http.Client{Timeout: 120 * time.Second},
	}, nil
}

// Name identifies the client for observability (includes the model).
func (c *agnesClient) Name() string { return "agnes:" + c.model }

// usage returns the accumulated provider-reported token counts. When the
// provider never reported usage, it returns (-1, -1) per the arena contract.
func (c *agnesClient) usage() (in, out int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.sawUsage {
		return -1, -1
	}
	return c.inputTokens, c.outputTokens
}

func (c *agnesClient) addUsage(in, out int) {
	c.mu.Lock()
	c.inputTokens += in
	c.outputTokens += out
	c.sawUsage = true
	c.mu.Unlock()
}

// StreamChat opens the SSE stream against agnes and emits incremental text
// tokens, folding the provider's stream-final usage into the accumulator and
// attaching it to the terminal Token (the llm.Client contract). Honors ctx
// cancellation on both read and channel send.
func (c *agnesClient) StreamChat(ctx context.Context, system, prompt string) (<-chan aisotropy.Token, error) {
	msgs := make([]map[string]string, 0, 2)
	if system != "" {
		msgs = append(msgs, map[string]string{"role": "system", "content": system})
	}
	msgs = append(msgs, map[string]string{"role": "user", "content": prompt})

	body, err := json.Marshal(map[string]any{
		"model":          c.model,
		"stream":         true,
		"max_tokens":     c.maxTokens,
		"messages":       msgs,
		"stream_options": map[string]any{"include_usage": true},
	})
	if err != nil {
		return nil, fmt.Errorf("agnes: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("agnes: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(req) //nolint:bodyclose // closed by consumeStream via defer
	if err != nil {
		return nil, fmt.Errorf("agnes: request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("agnes: status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	ch := make(chan aisotropy.Token)
	go c.consumeStream(ctx, resp.Body, ch)
	return ch, nil
}

func (c *agnesClient) consumeStream(ctx context.Context, body io.ReadCloser, ch chan<- aisotropy.Token) {
	defer close(ch)
	defer func() { _ = body.Close() }()

	var usage *aisotropy.Usage
	sc := bufio.NewScanner(body)
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
			if usage != nil {
				c.addUsage(usage.InputTokens, usage.OutputTokens)
			}
			c.send(ctx, ch, aisotropy.Token{Done: true, Usage: usage})
			return
		}
		text, u := decodeChunk(data)
		if u != nil {
			usage = u
		}
		if text != "" && !c.send(ctx, ch, aisotropy.Token{Text: text}) {
			return
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		c.send(ctx, ch, aisotropy.Token{Err: fmt.Errorf("agnes: stream read: %w", err)})
		return
	}
	if usage != nil {
		c.addUsage(usage.InputTokens, usage.OutputTokens)
	}
	c.send(ctx, ch, aisotropy.Token{Done: true, Usage: usage})
}

// decodeChunk decodes one SSE data payload: the text delta and, on the
// usage-only chunk agnes emits before [DONE], the provider token counts.
func decodeChunk(data string) (text string, usage *aisotropy.Usage) {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return "", nil // keep-alive / comment / non-JSON line
	}
	if chunk.Usage != nil {
		usage = &aisotropy.Usage{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens}
	}
	if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
		text = chunk.Choices[0].Delta.Content
	}
	return text, usage
}

func (c *agnesClient) send(ctx context.Context, ch chan<- aisotropy.Token, t aisotropy.Token) bool {
	select {
	case ch <- t:
		return true
	case <-ctx.Done():
		return false
	}
}
