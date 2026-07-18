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

// OpenAICompatClient implements Client (streaming chat), ToolCaller (native
// tool-calling, ADR-0012), and ReasoningToolCaller (stateless reasoning
// carry-forward, ADR-0061).
var (
	_ ToolCaller          = (*OpenAICompatClient)(nil)
	_ ReasoningToolCaller = (*OpenAICompatClient)(nil)
)

// OpenAICompatClient speaks the OpenAI /chat/completions protocol with stream=true.
type OpenAICompatClient struct {
	apiKey    string
	model     string
	baseURL   string
	name      string
	maxTokens int
	http      *http.Client

	// reasoning gates reasoning-model support (ADR-0061): when false (default)
	// the request sends no reasoning_effort and behaves byte-for-byte as before.
	// Reading reasoning fields OFF the response is always safe (a no-reasoning
	// endpoint like agnes returns them null → structural no-op), so it is not
	// gated. Set via EnableReasoning at composition when AISOTROPY_REASONING is on.
	reasoning       bool
	reasoningEffort string
}

// EnableReasoning turns on reasoning-model support at the given effort
// ("high"|"medium"|"low"|"off"). It makes the request advertise reasoning_effort
// so an o-series / reasoning-capable model spends a thinking budget. "off" is a
// no-op that leaves reasoning disabled. Called at composition (registry.go) only
// when AISOTROPY_REASONING is set — reasoning stays OFF by default. Reading the
// provider's reasoning fields off the response does not require this (see the
// reasoning field doc).
func (c *OpenAICompatClient) EnableReasoning(effort string) {
	effort = normalizeReasoningEffort(effort)
	if effort == ReasoningEffortOff {
		c.reasoning = false
		return
	}
	c.reasoning = true
	c.reasoningEffort = effort
}

// NewOpenRouterClient creates a client pointing at OpenRouter.
func NewOpenRouterClient(apiKey, model string) *OpenAICompatClient {
	if model == "" {
		model = DefaultOpenRouterModel
	}
	return newOpenAICompat(apiKey, model, openRouterBaseURL, providerOpenRouter)
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
	req, err := c.newStreamRequest(ctx, system, prompt)
	if err != nil {
		return nil, err
	}

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
	go c.consumeStream(ctx, resp.Body, ch)
	return ch, nil
}

// newStreamRequest builds the POST /chat/completions request for a streaming
// call: the system/user messages, stream:true, and stream_options.include_usage
// (so the provider emits one extra SSE chunk right before [DONE] carrying the
// real prompt/completion token counts — the OpenAI streaming usage contract;
// without it, streaming responses carry no usage data at all).
func (c *OpenAICompatClient) newStreamRequest(ctx context.Context, system, prompt string) (*http.Request, error) {
	msgs := make([]chatMessage, 0, 2)
	if system != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: system})
	}
	msgs = append(msgs, chatMessage{Role: "user", Content: prompt})

	payload := map[string]any{
		"model":          c.model,
		"stream":         true,
		"max_tokens":     c.maxTokens,
		"messages":       msgs,
		"stream_options": map[string]any{"include_usage": true},
	}
	// Reasoning-effort request param (ADR-0061), only when reasoning is enabled.
	// A model that ignores it is unaffected; the field is omitted otherwise, so
	// the default request is byte-for-byte unchanged.
	if c.reasoning && c.reasoningEffort != "" {
		payload["reasoning_effort"] = c.reasoningEffort
	}
	body, err := json.Marshal(payload)
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
	return req, nil
}

// consumeStream scans body line by line, emitting a Token per text delta and
// a terminal Done/Err Token, then closes ch. It owns body's lifetime (closes
// it via defer) and is meant to run in its own goroutine, started by
// StreamChat.
func (c *OpenAICompatClient) consumeStream(ctx context.Context, body io.ReadCloser, ch chan<- Token) {
	defer close(ch)
	defer func() { _ = body.Close() }()

	var usage *Usage

	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if ctx.Err() != nil {
			return
		}
		data, ok := sseLineData(sc.Text())
		if !ok {
			continue
		}
		if data == "[DONE]" {
			sendToken(ctx, ch, Token{Done: true, Usage: usage})
			return
		}
		text, reasoning := applyStreamChunk(data, &usage)
		// Reasoning is a SEPARATE channel: emit it as its own Token so a text-only
		// reader (which appends only tok.Text) never sees chain-of-thought. A chunk
		// carrying both (rare) yields two tokens, reasoning first.
		if reasoning != "" && !sendToken(ctx, ch, Token{Reasoning: reasoning}) {
			return
		}
		if text != "" && !sendToken(ctx, ch, Token{Text: text}) {
			return
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
}

// sseLineData extracts the "data:" payload from one SSE line. ok is false for
// a non-"data:" line (blank/keep-alive/comment), which the caller should skip.
func sseLineData(line string) (data string, ok bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "data:")), true
}

// applyStreamChunk decodes one SSE "data:" JSON payload, folding any usage it
// carries into *usage (usage arrives on its own chunk, with empty Choices,
// right before [DONE] — see newStreamRequest's stream_options.include_usage),
// and returns the text delta AND the reasoning delta from this chunk (either or
// both may be ""). Reasoning is read from the three shapes gateways expose
// (ADR-0061 Piece 1c): delta.reasoning, delta.reasoning_content, and
// provider_specific_fields.reasoning; the reasoning-token count from
// usage.completion_tokens_details.reasoning_tokens. A no-reasoning endpoint
// (agnes returns these null/absent) is a structural no-op: reasoning stays "".
func applyStreamChunk(data string, usage **Usage) (text, reasoning string) {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content          string `json:"content"`
				Reasoning        string `json:"reasoning"`
				ReasoningContent string `json:"reasoning_content"`
				ProviderSpecific struct {
					Reasoning string `json:"reasoning"`
				} `json:"provider_specific_fields"`
			} `json:"delta"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens           int `json:"prompt_tokens"`
			CompletionTokens       int `json:"completion_tokens"`
			CompletionTokensDetail struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return "", "" // keep-alive / comment / non-JSON line
	}
	if chunk.Usage != nil {
		*usage = &Usage{
			InputTokens:     chunk.Usage.PromptTokens,
			OutputTokens:    chunk.Usage.CompletionTokens,
			ReasoningTokens: chunk.Usage.CompletionTokensDetail.ReasoningTokens,
		}
	}
	if len(chunk.Choices) > 0 {
		d := chunk.Choices[0].Delta
		text = d.Content
		// First non-empty of the three reasoning shapes wins.
		reasoning = firstNonEmpty(d.Reasoning, d.ReasoningContent, d.ProviderSpecific.Reasoning)
	}
	return text, reasoning
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
	payload := c.buildToolCallPayload(system, prompt, toolSpecs)

	out, err := c.sendToolCallRequest(ctx, payload)
	if err != nil {
		return ToolCallResult{}, err
	}
	if len(out.Choices) == 0 {
		return ToolCallResult{}, nil
	}

	msg := out.Choices[0].Message
	res := ToolCallResult{Message: msg.Content}
	for _, tc := range msg.ToolCalls {
		// Args: flattened string view (compat / map executor). RawArgs: the
		// provider's exact JSON, preserved so a typed tool can decode nested /
		// typed fields the flattening drops (ADR-0016 lever #1).
		res.Calls = append(res.Calls, ToolCall{
			Name:    tc.Function.Name,
			Args:    parseToolArgs(tc.Function.Arguments),
			RawArgs: rawToolArgs(tc.Function.Arguments),
		})
	}
	return res, nil
}

// CallToolsReasoning implements ReasoningToolCaller (ADR-0061): stateless
// reasoning carry-forward for the OpenAI-compat seam. prior is reinjected as
// OPTIONAL context (there is no signed block to preserve, unlike Anthropic): any
// text-kind reasoning it carries is prepended to the prompt so the model can see
// its earlier train of thought. The response's reasoning (delta.reasoning /
// reasoning_content / provider_specific_fields.reasoning, folded here from the
// non-streaming message) is returned as the fresh ReasoningState for the next
// step. On a no-reasoning endpoint the response carries none, so the returned
// state is empty — a structural no-op that degrades to plain CallTools behavior.
func (c *OpenAICompatClient) CallToolsReasoning(ctx context.Context, system, prompt string, toolSpecs []ToolSpec, prior ReasoningState) (ToolCallResult, ReasoningState, error) {
	// Reinject prior reasoning as optional leading context (text-kind only).
	prompt = prependPriorReasoning(prompt, prior)

	payload := c.buildToolCallPayload(system, prompt, toolSpecs)
	if c.reasoning && c.reasoningEffort != "" {
		payload["reasoning_effort"] = c.reasoningEffort
	}

	out, err := c.sendToolCallRequest(ctx, payload)
	if err != nil {
		return ToolCallResult{}, ReasoningState{}, err
	}
	if len(out.Choices) == 0 {
		return ToolCallResult{}, ReasoningState{}, nil
	}

	res, next := toolResultAndReasoning(out)
	return res, next, nil
}

// prependPriorReasoning folds a prior ReasoningState's text-kind reasoning into
// the prompt as clearly-labelled optional context. Non-text (signed/redacted)
// blocks are skipped — they are Anthropic-native and meaningless as OpenAI text.
// Empty prior returns prompt unchanged.
func prependPriorReasoning(prompt string, prior ReasoningState) string {
	var sb strings.Builder
	for _, b := range prior.Blocks {
		if b.kind == reasoningKindText && b.text != "" {
			sb.WriteString(b.text)
			sb.WriteString("\n")
		}
	}
	if sb.Len() == 0 {
		return prompt
	}
	return "[prior reasoning]\n" + sb.String() + "\n" + prompt
}

// toolResultAndReasoning maps a non-streaming tool response's first choice into
// the ToolCallResult (mirroring CallTools) and extracts any reasoning the
// message carried into a fresh ReasoningState (text-kind, no signature).
func toolResultAndReasoning(out toolCallResponse) (ToolCallResult, ReasoningState) {
	msg := out.Choices[0].Message
	res := ToolCallResult{Message: msg.Content}
	for _, tc := range msg.ToolCalls {
		res.Calls = append(res.Calls, ToolCall{
			Name:    tc.Function.Name,
			Args:    parseToolArgs(tc.Function.Arguments),
			RawArgs: rawToolArgs(tc.Function.Arguments),
		})
	}
	var next ReasoningState
	if r := firstNonEmpty(msg.Reasoning, msg.ReasoningContent, msg.ProviderSpecific.Reasoning); r != "" {
		next.Blocks = append(next.Blocks, newTextReasoningBlock(r))
	}
	return res, next
}

// buildToolCallPayload assembles the /chat/completions request body for a
// CallTools invocation: the system/user messages plus, when toolSpecs is
// non-empty, the "tools" + "tool_choice":"auto" envelope the provider expects.
func (c *OpenAICompatClient) buildToolCallPayload(system, prompt string, toolSpecs []ToolSpec) map[string]any {
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
	if len(toolSpecs) == 0 {
		return payload
	}

	tools := make([]map[string]any, 0, len(toolSpecs))
	for _, t := range toolSpecs {
		// When the ToolSpec declares a Parameters schema (ADR-0016 lever #1:
		// struct-as-contract for tool inputs), advertise it verbatim so the
		// model knows the exact argument shape. When it does not, fall back to
		// the permissive object the ADR-0012 minimal seam used: the model may
		// call the tool with or without free-form arguments.
		var params any
		if len(t.Parameters) > 0 {
			params = t.Parameters // json.RawMessage marshals as its raw JSON
		} else {
			params = map[string]any{
				jsonKeyType:            jsonSchemaTypeObject,
				"properties":           map[string]any{},
				"additionalProperties": true,
			}
		}
		tools = append(tools, map[string]any{
			jsonKeyType: "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  params,
			},
		})
	}
	payload["tools"] = tools
	payload["tool_choice"] = "auto"
	return payload
}

// toolCallResponse is the subset of the /chat/completions response shape
// CallTools needs: the first choice's message content plus any tool calls the
// provider returned.
type toolCallResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
			// Reasoning shapes (ADR-0061): the three keys gateways use for a
			// non-streaming message's reasoning. All "" on a no-reasoning endpoint.
			Reasoning        string `json:"reasoning"`
			ReasoningContent string `json:"reasoning_content"`
			ProviderSpecific struct {
				Reasoning string `json:"reasoning"`
			} `json:"provider_specific_fields"`
			ToolCalls []struct {
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

// sendToolCallRequest POSTs payload to the /chat/completions endpoint and
// decodes the response into a toolCallResponse, translating a non-2xx status
// or a transport/decode failure into a descriptive error.
func (c *OpenAICompatClient) sendToolCallRequest(ctx context.Context, payload map[string]any) (toolCallResponse, error) {
	var out toolCallResponse

	body, err := json.Marshal(payload)
	if err != nil {
		return out, fmt.Errorf("openai-compat: marshal tool request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return out, fmt.Errorf("openai-compat: build tool request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return out, fmt.Errorf("openai-compat: tool request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return out, fmt.Errorf("openai-compat: tool call status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("openai-compat: decode tool response: %w", err)
	}
	return out, nil
}

// rawToolArgs returns the model's tool arguments as raw JSON bytes for
// ToolCall.RawArgs, preserving nested/typed structure for typed tools (ADR-0016
// lever #1). It returns nil for empty or invalid JSON so a caller can cleanly
// fall back to the flattened Args map.
func rawToolArgs(argsJSON string) json.RawMessage {
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" {
		return nil
	}
	if !json.Valid([]byte(trimmed)) {
		return nil
	}
	return json.RawMessage(trimmed)
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
