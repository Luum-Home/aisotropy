package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── T4: ReasoningState value semantics ─────────────────────────────────────

func TestReasoningState_ValueSemantics(t *testing.T) {
	var empty ReasoningState
	if !empty.IsEmpty() {
		t.Fatal("zero ReasoningState must be empty")
	}
	if empty.ReasoningText() != "" {
		t.Fatalf("empty state text = %q, want \"\"", empty.ReasoningText())
	}

	s := NewTextReasoningState("hello thoughts")
	if s.IsEmpty() {
		t.Fatal("state with a block must not be empty")
	}
	if s.ReasoningText() != "hello thoughts" {
		t.Fatalf("text = %q", s.ReasoningText())
	}

	// Clone is an independent copy: mutating the clone's slice must not touch s.
	cl := s.Clone()
	cl.Blocks = append(cl.Blocks, newTextReasoningBlock("extra"))
	if len(s.Blocks) != 1 {
		t.Fatalf("Clone aliased the backing slice: original grew to %d", len(s.Blocks))
	}
	if NewTextReasoningState("").IsEmpty() != true {
		t.Fatal("empty text must yield an empty state")
	}
}

func TestReasoningState_ReasoningText_SkipsRedacted(t *testing.T) {
	s := ReasoningState{Blocks: []ReasoningBlock{
		newThinkingBlock("signed thoughts", "sig"),
		newRedactedThinkingBlock("ENCRYPTED-OPAQUE-BYTES"),
		newTextReasoningBlock("plain thoughts"),
	}}
	got := s.ReasoningText()
	if !strings.Contains(got, "signed thoughts") || !strings.Contains(got, "plain thoughts") {
		t.Fatalf("ReasoningText missing thinking/text: %q", got)
	}
	if strings.Contains(got, "ENCRYPTED-OPAQUE-BYTES") {
		t.Fatal("redacted (opaque) reasoning must never surface as text")
	}
}

// ── T7: reasoning-effort ladder under quota pressure ───────────────────────

func TestReasoningEffortForPressure_Ladder(t *testing.T) {
	cases := []struct {
		configured string
		pressure   float64
		want       string
	}{
		{ReasoningEffortHigh, 0.0, ReasoningEffortHigh},
		{ReasoningEffortHigh, 0.79, ReasoningEffortHigh},
		{ReasoningEffortHigh, 0.80, ReasoningEffortMedium}, // step down one rung
		{ReasoningEffortMedium, 0.85, ReasoningEffortLow},
		{ReasoningEffortLow, 0.90, ReasoningEffortOff},
		{ReasoningEffortHigh, 0.95, ReasoningEffortOff}, // cheap-model threshold: off
		{ReasoningEffortHigh, 1.0, ReasoningEffortOff},
		{ReasoningEffortOff, 0.0, ReasoningEffortOff}, // never steps up
		{ReasoningEffortOff, 0.85, ReasoningEffortOff},
	}
	for _, c := range cases {
		if got := ReasoningEffortForPressure(c.configured, c.pressure); got != c.want {
			t.Errorf("ReasoningEffortForPressure(%q, %.2f) = %q, want %q", c.configured, c.pressure, got, c.want)
		}
	}
}

func TestReasoningEnv(t *testing.T) {
	t.Setenv(reasoningEnvVar, "true")
	if !ReasoningEnabledFromEnv() {
		t.Fatal("AISOTROPY_REASONING=true must enable reasoning")
	}
	t.Setenv(reasoningEnvVar, "")
	if ReasoningEnabledFromEnv() {
		t.Fatal("unset AISOTROPY_REASONING must be OFF (the default)")
	}
	t.Setenv(reasoningEffortEnvVar, "HIGH")
	if ReasoningEffortFromEnv() != ReasoningEffortHigh {
		t.Fatalf("effort = %q, want high", ReasoningEffortFromEnv())
	}
	t.Setenv(reasoningEffortEnvVar, "garbage")
	if ReasoningEffortFromEnv() != ReasoningEffortMedium {
		t.Fatalf("unrecognized effort must default to medium, got %q", ReasoningEffortFromEnv())
	}
}

// ── T3: OpenAI-compat reasoning parse + graceful degradation ───────────────

func TestApplyStreamChunk_Reasoning(t *testing.T) {
	var usage *Usage
	// delta.reasoning
	if text, r := applyStreamChunk(`{"choices":[{"delta":{"reasoning":"step one"}}]}`, &usage); text != "" || r != "step one" {
		t.Errorf("delta.reasoning: text=%q reasoning=%q", text, r)
	}
	// delta.reasoning_content (DeepSeek-R1 shape)
	if _, r := applyStreamChunk(`{"choices":[{"delta":{"reasoning_content":"rc"}}]}`, &usage); r != "rc" {
		t.Errorf("reasoning_content: %q", r)
	}
	// provider_specific_fields.reasoning
	if _, r := applyStreamChunk(`{"choices":[{"delta":{"provider_specific_fields":{"reasoning":"psf"}}}]}`, &usage); r != "psf" {
		t.Errorf("provider_specific_fields.reasoning: %q", r)
	}
	// reasoning_tokens on the usage chunk
	usage = nil
	applyStreamChunk(`{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":40,"completion_tokens_details":{"reasoning_tokens":30}}}`, &usage)
	if usage == nil || usage.ReasoningTokens != 30 || usage.OutputTokens != 40 {
		t.Fatalf("usage reasoning_tokens not folded: %+v", usage)
	}
}

// TestApplyStreamChunk_AgnesNullNoOp is the graceful-degradation blocker (T3):
// a no-reasoning endpoint (agnes returns reasoning:null / omits the field) must
// yield reasoning == "" and identical text behavior to today.
func TestApplyStreamChunk_AgnesNullNoOp(t *testing.T) {
	var usage *Usage
	if text, r := applyStreamChunk(`{"choices":[{"delta":{"reasoning":null,"content":"answer"}}]}`, &usage); text != "answer" || r != "" {
		t.Fatalf("agnes null reasoning must be a no-op: text=%q reasoning=%q", text, r)
	}
	if text, r := applyStreamChunk(`{"choices":[{"delta":{"content":"answer"}}]}`, &usage); text != "answer" || r != "" {
		t.Fatalf("absent reasoning field must be a no-op: text=%q reasoning=%q", text, r)
	}
}

// TestOpenAICompatStreamChat_ReasoningSeparateChannel drives the full SSE path:
// reasoning deltas land on Token.Reasoning, answer deltas on Token.Text — never
// mixed — and usage carries reasoning_tokens.
func TestOpenAICompatStreamChat_ReasoningSeparateChannel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning\":\"let me think\"}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"the \"}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":9,\"completion_tokens_details\":{\"reasoning_tokens\":4}}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := NewOpenAICompatClient("k", "m", srv.URL, "test")
	ch, err := c.StreamChat(context.Background(), "", "q")
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var text, reasoning strings.Builder
	var usage *Usage
	for tok := range ch {
		text.WriteString(tok.Text)
		reasoning.WriteString(tok.Reasoning)
		if tok.Done {
			usage = tok.Usage
		}
	}
	if text.String() != "the answer" {
		t.Errorf("text = %q, want %q (reasoning must NOT be mixed in)", text.String(), "the answer")
	}
	if reasoning.String() != "let me think" {
		t.Errorf("reasoning = %q", reasoning.String())
	}
	if usage == nil || usage.ReasoningTokens != 4 {
		t.Fatalf("usage reasoning_tokens = %+v, want 4", usage)
	}
}

// TestOpenAICompatCallToolsReasoning_RoundTrip: prior reasoning is reinjected
// into the request, and the response's reasoning is captured into the returned
// ReasoningState (T4 stateless carry-forward).
func TestOpenAICompatCallToolsReasoning_RoundTrip(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"","reasoning":"fresh reasoning","tool_calls":[{"function":{"name":"noop","arguments":"{}"}}]}}]}`)
	}))
	defer srv.Close()

	c := NewOpenAICompatClient("k", "m", srv.URL, "test")
	prior := NewTextReasoningState("earlier train of thought")
	res, next, err := c.CallToolsReasoning(context.Background(), "sys", "do it",
		[]ToolSpec{{Name: "noop", Description: "n"}}, prior)
	if err != nil {
		t.Fatalf("CallToolsReasoning: %v", err)
	}
	if !strings.Contains(gotBody, "earlier train of thought") {
		t.Errorf("prior reasoning was not reinjected into the request: %s", gotBody)
	}
	if len(res.Calls) != 1 || res.Calls[0].Name != "noop" {
		t.Fatalf("tool call not returned: %+v", res.Calls)
	}
	if next.ReasoningText() != "fresh reasoning" {
		t.Fatalf("captured reasoning = %q, want %q", next.ReasoningText(), "fresh reasoning")
	}
}

// TestOpenAICompatCallToolsReasoning_NoReasoningNoOp: a no-reasoning endpoint
// returns an empty ReasoningState — graceful degradation identical to CallTools.
func TestOpenAICompatCallToolsReasoning_NoReasoningNoOp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"just an answer"}}]}`)
	}))
	defer srv.Close()

	c := NewOpenAICompatClient("k", "m", srv.URL, "test")
	res, next, err := c.CallToolsReasoning(context.Background(), "", "q", nil, ReasoningState{})
	if err != nil {
		t.Fatalf("CallToolsReasoning: %v", err)
	}
	if !next.IsEmpty() {
		t.Fatalf("no-reasoning endpoint must yield an empty state, got %+v", next)
	}
	if res.Message != "just an answer" {
		t.Fatalf("message = %q", res.Message)
	}
}

// ── T2/T4: Anthropic native signed-thinking-block re-attach round-trip ─────

// TestAnthropic_ReattachSignedThinkingBlock asserts buildAnthropicThinkingBlocks
// re-materializes a prior signed thinking block as a NATIVE thinking content
// block on the next request — signature preserved verbatim (Anthropic's hard
// requirement; flattening to text would lose it).
func TestAnthropic_ReattachSignedThinkingBlock(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)
		writeSSE(w, "message_stop", `{"type":"message_stop"}`)
	}))
	defer srv.Close()

	c := newTestAnthropicClient(srv.URL, "")
	c.EnableReasoning(ReasoningEffortMedium)
	prior := ReasoningState{Blocks: []ReasoningBlock{newThinkingBlock("prior chain of thought", "SIG-XYZ-123")}}

	ch, err := c.streamChat(context.Background(), "", "continue", prior)
	if err != nil {
		t.Fatalf("streamChat: %v", err)
	}
	for range ch { //nolint:revive // drain
	}
	if !strings.Contains(gotBody, "prior chain of thought") || !strings.Contains(gotBody, "SIG-XYZ-123") {
		t.Fatalf("signed thinking block not re-attached natively (signature must survive): %s", gotBody)
	}
	if !strings.Contains(gotBody, `"thinking"`) {
		t.Fatalf("re-attached block is not a native thinking block: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"budget_tokens"`) {
		t.Fatalf("reasoning-on request must carry a Thinking budget: %s", gotBody)
	}
}

// TestAnthropicEventToken_ThinkingDelta: with reasoning ON a thinking_delta maps
// to Token.Reasoning; with reasoning OFF it is discarded (no reasoning leaks and
// the text path is unchanged).
func TestAnthropicEventToken_ThinkingDelta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`)
		writeSSE(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer"}}`)
		writeSSE(w, "message_stop", `{"type":"message_stop"}`)
	}))
	defer srv.Close()

	// Reasoning ON.
	on := newTestAnthropicClient(srv.URL, "")
	on.EnableReasoning(ReasoningEffortLow)
	ch, _ := on.StreamChat(context.Background(), "", "p")
	var txt, rsn strings.Builder
	for tok := range ch {
		txt.WriteString(tok.Text)
		rsn.WriteString(tok.Reasoning)
	}
	if txt.String() != "answer" || rsn.String() != "hmm" {
		t.Fatalf("reasoning ON: text=%q reasoning=%q", txt.String(), rsn.String())
	}

	// Reasoning OFF (default): the thinking delta is discarded, text unchanged.
	off := newTestAnthropicClient(srv.URL, "")
	ch2, _ := off.StreamChat(context.Background(), "", "p")
	var txt2, rsn2 strings.Builder
	for tok := range ch2 {
		txt2.WriteString(tok.Text)
		rsn2.WriteString(tok.Reasoning)
	}
	if txt2.String() != "answer" || rsn2.String() != "" {
		t.Fatalf("reasoning OFF: text=%q reasoning=%q (reasoning must be discarded)", txt2.String(), rsn2.String())
	}
}
