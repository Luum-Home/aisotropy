package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// TestAnthropicThinkingBudget covers every anthropicThinkingBudget branch:
// high/medium/low map to their fixed budgets, and anything else (including
// "off") maps to 0 (no thinking).
func TestAnthropicThinkingBudget(t *testing.T) {
	cases := []struct {
		effort string
		want   int64
	}{
		{ReasoningEffortHigh, 8192},
		{ReasoningEffortMedium, 4096},
		{ReasoningEffortLow, 1024},
		{ReasoningEffortOff, 0},
		{"garbage", 0},
	}
	for _, tc := range cases {
		if got := anthropicThinkingBudget(tc.effort); got != tc.want {
			t.Errorf("anthropicThinkingBudget(%q) = %d, want %d", tc.effort, got, tc.want)
		}
	}
}

// TestAnthropicEnableReasoning_BumpsMaxTokensAboveBudget proves EnableReasoning
// raises maxTokens only when it would otherwise leave no room for the answer
// after the thinking budget (maxTokens <= budget), and leaves an already
// sufficient maxTokens untouched.
func TestAnthropicEnableReasoning_BumpsMaxTokensAboveBudget(t *testing.T) {
	t.Run("high effort bumps a too-small maxTokens", func(t *testing.T) {
		c := newTestAnthropicClient("http://unused", "")
		c.maxTokens = 1024 // default; <= the 8192 high-effort budget
		c.EnableReasoning(ReasoningEffortHigh)
		if !c.reasoning || c.reasoningEffort != ReasoningEffortHigh {
			t.Fatalf("expected reasoning enabled at high effort, got reasoning=%v effort=%q", c.reasoning, c.reasoningEffort)
		}
		if want := int64(8192 + 1024); c.maxTokens != want {
			t.Fatalf("maxTokens = %d, want %d (budget + headroom)", c.maxTokens, want)
		}
	})

	t.Run("already-sufficient maxTokens is left untouched", func(t *testing.T) {
		c := newTestAnthropicClient("http://unused", "")
		c.maxTokens = 20000 // already well above the 1024 low-effort budget
		c.EnableReasoning(ReasoningEffortLow)
		if c.maxTokens != 20000 {
			t.Fatalf("maxTokens = %d, want unchanged 20000", c.maxTokens)
		}
	})

	t.Run("off disables reasoning without touching maxTokens", func(t *testing.T) {
		c := newTestAnthropicClient("http://unused", "")
		c.maxTokens = 1024
		c.reasoning = true
		c.reasoningEffort = ReasoningEffortHigh
		c.EnableReasoning(ReasoningEffortOff)
		if c.reasoning {
			t.Fatal("expected reasoning disabled after EnableReasoning(off)")
		}
		if c.maxTokens != 1024 {
			t.Fatalf("maxTokens = %d, want unchanged 1024 on the off path", c.maxTokens)
		}
	})
}

// TestBuildAnthropicThinkingBlocks_RedactedBlockRoundTrips proves a redacted
// (encrypted) prior thinking block re-materializes as a native
// RedactedThinkingBlock — never decoded, opaque payload preserved — alongside
// a signed thinking block in the same prior state.
func TestBuildAnthropicThinkingBlocks_RedactedBlockRoundTrips(t *testing.T) {
	prior := ReasoningState{Blocks: []ReasoningBlock{
		newThinkingBlock("visible thoughts", "sig-1"),
		newRedactedThinkingBlock("OPAQUE-ENCRYPTED-PAYLOAD"),
	}}
	blocks := buildAnthropicThinkingBlocks(prior)
	if len(blocks) != 2 {
		t.Fatalf("buildAnthropicThinkingBlocks returned %d blocks, want 2", len(blocks))
	}
	if blocks[0].OfThinking == nil {
		t.Fatalf("blocks[0] is not a thinking block: %+v", blocks[0])
	}
	if blocks[1].OfRedactedThinking == nil {
		t.Fatalf("blocks[1] is not a redacted-thinking block: %+v", blocks[1])
	}
	if blocks[1].OfRedactedThinking.Data != "OPAQUE-ENCRYPTED-PAYLOAD" {
		t.Fatalf("redacted block data = %q, want the opaque payload preserved verbatim", blocks[1].OfRedactedThinking.Data)
	}
}

// newTestAnthropicClient builds an AnthropicClient whose underlying SDK client
// points at a test server instead of the real api.anthropic.com. The struct's
// fields are unexported but reachable from an in-package (_test.go) test, which
// is the whole reason this seam is testable without a real API key.
func newTestAnthropicClient(baseURL, model string) *AnthropicClient {
	if model == "" {
		model = "claude-test"
	}
	return &AnthropicClient{
		client: anthropic.NewClient(
			option.WithAPIKey("test-key"),
			option.WithBaseURL(baseURL),
			option.WithMaxRetries(0),
		),
		model:     model,
		maxTokens: 1024,
	}
}

// writeSSE writes one Anthropic-style SSE event (an "event:" line, one or more
// "data:" lines, then the mandatory blank separator line).
func writeSSE(w io.Writer, event, data string) {
	_, _ = io.WriteString(w, "event: "+event+"\n")
	_, _ = io.WriteString(w, "data: "+data+"\n\n")
}

// collectTokens drains ch into the concatenated text, the terminal usage (if
// any), whether Done was seen, and any error token.
func collectTokens(ch <-chan Token) (text string, usage *Usage, done bool, streamErr error) {
	for tok := range ch {
		switch {
		case tok.Err != nil:
			streamErr = tok.Err
		case tok.Done:
			done = true
			usage = tok.Usage
		default:
			text += tok.Text
		}
	}
	return text, usage, done, streamErr
}

// TestAnthropicStreamChat_HappyPath drives the real SDK streaming path against
// a mocked Messages endpoint: message_start (input usage) → two text deltas →
// message_delta (output usage) → message_stop. It asserts the deltas are
// concatenated, the terminal Done carries the folded Usage, and the request
// itself was well-formed (POST v1/messages with the system prompt).
func TestAnthropicStreamChat_HappyPath(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSE(w, "message_start", `{"type":"message_start","message":{"usage":{"input_tokens":11,"output_tokens":1}}}`)
		writeSSE(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`)
		writeSSE(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":", world"}}`)
		writeSSE(w, "message_delta", `{"type":"message_delta","usage":{"output_tokens":7}}`)
		writeSSE(w, "message_stop", `{"type":"message_stop"}`)
	}))
	defer srv.Close()

	c := newTestAnthropicClient(srv.URL, "claude-haiku-test")
	if c.Name() != "anthropic:claude-haiku-test" {
		t.Errorf("Name() = %q", c.Name())
	}

	ch, err := c.StreamChat(context.Background(), "be terse", "hi")
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	text, usage, done, streamErr := collectTokens(ch)

	if streamErr != nil {
		t.Fatalf("unexpected stream error: %v", streamErr)
	}
	if text != "Hello, world" {
		t.Errorf("text = %q, want %q", text, "Hello, world")
	}
	if !done {
		t.Error("expected a Done token")
	}
	if usage == nil {
		t.Fatal("expected Usage on the terminal token")
	}
	if usage.InputTokens != 11 || usage.OutputTokens != 7 {
		t.Errorf("usage = %+v, want {11 7}", *usage)
	}
	if !strings.Contains(gotBody, "be terse") {
		t.Errorf("request body did not carry the system prompt: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"hi"`) {
		t.Errorf("request body did not carry the user prompt: %s", gotBody)
	}
}

// TestAnthropicStreamChat_NoSystem covers the branch where system == "" (no
// System field added to the params) and a text delta with no usage events at
// all, so the terminal Done carries no Usage.
func TestAnthropicStreamChat_NoSystem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), `"system"`) {
			t.Errorf("empty system must not appear in body: %s", b)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"solo"}}`)
		writeSSE(w, "message_stop", `{"type":"message_stop"}`)
	}))
	defer srv.Close()

	c := newTestAnthropicClient(srv.URL, "")
	ch, err := c.StreamChat(context.Background(), "", "p")
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	text, usage, done, streamErr := collectTokens(ch)
	if streamErr != nil {
		t.Fatalf("unexpected error: %v", streamErr)
	}
	if text != "solo" {
		t.Errorf("text = %q", text)
	}
	if !done {
		t.Error("expected Done")
	}
	if usage != nil {
		t.Errorf("no usage events were sent, but got Usage %+v", *usage)
	}
}

// TestAnthropicStreamChat_NonTextDeltaSkipped asserts that a content_block_delta
// carrying a non-text delta type (e.g. input_json_delta) and an empty-text delta
// are both dropped (emit=false), while surrounding real text still flows.
func TestAnthropicStreamChat_NonTextDeltaSkipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{"}}`)
		writeSSE(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`)
		writeSSE(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"real"}}`)
		writeSSE(w, "message_stop", `{"type":"message_stop"}`)
	}))
	defer srv.Close()

	c := newTestAnthropicClient(srv.URL, "")
	ch, err := c.StreamChat(context.Background(), "", "p")
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	text, _, done, streamErr := collectTokens(ch)
	if streamErr != nil {
		t.Fatalf("unexpected error: %v", streamErr)
	}
	if text != "real" {
		t.Errorf("text = %q, want %q", text, "real")
	}
	if !done {
		t.Error("expected Done")
	}
}

// TestAnthropicStreamChat_MidStreamError asserts a provider "error" SSE event
// surfaces as a Token.Err (not masked as a clean Done). finishAnthropicStream
// must propagate stream.Err() when ctx was not cancelled.
func TestAnthropicStreamChat_MidStreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`)
		writeSSE(w, "error", `{"type":"error","error":{"type":"overloaded_error","message":"boom"}}`)
	}))
	defer srv.Close()

	c := newTestAnthropicClient(srv.URL, "")
	ch, err := c.StreamChat(context.Background(), "", "p")
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	text, _, done, streamErr := collectTokens(ch)
	if text != "partial" {
		t.Errorf("expected the pre-error token %q, got %q", "partial", text)
	}
	if streamErr == nil {
		t.Fatal("expected a Token.Err from the error SSE event")
	}
	if done {
		t.Error("an errored stream must NOT emit Done=true")
	}
}

// TestAnthropicStreamChat_CtxCancel cancels the context before the stream
// starts producing; the producer goroutine must stop and close the channel
// without leaking. A cancelled stream error is NOT propagated as a Token.Err
// (finishAnthropicStream masks it because ctx.Err() != nil).
func TestAnthropicStreamChat_CtxCancel(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release // hold the response open until the test cancels
	}))
	defer srv.Close()
	defer close(release)

	c := newTestAnthropicClient(srv.URL, "")
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := c.StreamChat(ctx, "", "p")
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	cancel()

	// Drain: the channel must close without hanging.
	for range ch { //nolint:revive // draining until close
	}
}

// TestAnthropicEventText_UnitTable exercises anthropicEventText directly for the
// event kinds and their usage-folding, independent of any HTTP transport.
func TestAnthropicEventText_UnitTable(t *testing.T) {
	// Non-emitting "message_stop" style event: an unknown/default type folds no
	// usage and emits nothing. We can only feed it through the union via JSON,
	// which the HTTP tests already cover; here we assert the finish helper.
	out := make(chan Token, 1)
	finishAnthropicStream(context.Background(), nil, out, Usage{InputTokens: 3, OutputTokens: 4}, true)
	close(out)
	tok := <-out
	if !tok.Done || tok.Usage == nil || tok.Usage.InputTokens != 3 {
		t.Errorf("finishAnthropicStream clean-with-usage = %+v", tok)
	}

	// Clean finish without usage: Done, no Usage attached.
	out2 := make(chan Token, 1)
	finishAnthropicStream(context.Background(), nil, out2, Usage{}, false)
	close(out2)
	tok2 := <-out2
	if !tok2.Done || tok2.Usage != nil {
		t.Errorf("finishAnthropicStream clean-no-usage = %+v", tok2)
	}
}
