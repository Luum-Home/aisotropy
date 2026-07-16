package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
)

// newAGUITestRouter builds a chi + humachi router with ONLY the AG-UI route
// mounted (RegisterAGUI), so these tests exercise the real route table and the
// real StreamCore pipeline — not a hand-rolled substitute. All optional tiers
// (facts/limiter/tools/meter/semantic/persona/gateway/guard) are nil, exactly as
// StreamCore documents them to degrade, so the tests depend on no network or
// external service. Working memory is the only stateful dependency (isolation +
// persistence assertions read it back).
func newAGUITestRouter(t *testing.T, client llm.Client, wm *memory.WorkingMemory) http.Handler {
	t.Helper()
	router := chi.NewRouter()
	humaAPI := humachi.New(router, huma.DefaultConfig("Aisotropy-agui-test", "0.1.0"))
	RegisterAGUI(humaAPI, client, wm,
		nil, // store
		nil, // limiter
		nil, // toolReg
		nil, // meter
		nil, // cheapClient
		nil, // semStore
		nil, // embedder
		nil, // reranker
		nil, // persona
		0,   // inPricePer1M
		0,   // outPricePer1M
		nil, // bptSampler
		nil, // gw
		nil, // guard
	)
	return router
}

// scriptedAGUIStub is an llm.Client whose StreamChat emits a fixed token script,
// in order, respecting ctx cancellation. It ignores the system/prompt so the
// wire output is fully deterministic — the basis for the golden-bytes test. A
// token carrying a non-nil Err reproduces a mid-stream provider failure.
type scriptedAGUIStub struct {
	name   string
	tokens []llm.Token
}

func (s *scriptedAGUIStub) Name() string {
	if s.name != "" {
		return s.name
	}
	return "scripted-agui-stub"
}

func (s *scriptedAGUIStub) StreamChat(ctx context.Context, _, _ string) (<-chan llm.Token, error) {
	out := make(chan llm.Token)
	toks := s.tokens
	go func() {
		defer close(out)
		for _, tk := range toks {
			select {
			case out <- tk:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// aguiUserBody builds a minimal AG-UI RunAgentInput JSON body carrying a single
// user message. Built as a map (not the internal aguiRunAgentInput struct) to
// mirror what an external client actually sends over the wire.
func aguiUserBody(threadID, runID, userText string) []byte {
	body := map[string]any{
		"threadId": threadID,
		"runId":    runID,
		"messages": []map[string]any{
			{"role": "user", "content": userText},
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return b
}

// aguiFrame is one parsed AG-UI SSE frame: its "type" discriminator plus the raw
// JSON payload (kept raw so golden tests can assert exact bytes).
type aguiFrame struct {
	Type string
	Raw  json.RawMessage
}

// parseAGUIFrames splits a raw AG-UI SSE body into its individual `data: {json}`
// frames. Unlike aisotropy's own SSE (which uses named `event:` lines), every
// AG-UI frame is a lone `data:` line whose JSON carries the "type" — so this
// asserts that shape too: any block without a `data: ` prefix, or whose payload
// is not valid JSON, fails the test.
func parseAGUIFrames(t *testing.T, body []byte) []aguiFrame {
	t.Helper()
	var frames []aguiFrame
	for _, block := range strings.Split(string(body), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		if !strings.HasPrefix(block, "data: ") {
			t.Fatalf("AG-UI frame missing 'data: ' prefix: %q", block)
		}
		raw := json.RawMessage(strings.TrimPrefix(block, "data: "))
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			t.Fatalf("AG-UI frame is not valid JSON: %q: %v", raw, err)
		}
		frames = append(frames, aguiFrame{Type: head.Type, Raw: raw})
	}
	return frames
}

// postAGUI issues a single AG-UI POST against the /chat/agui route and returns
// the recorder. Content-Type application/json + Accept text/event-stream mirror
// a real AG-UI/CopilotKit client. httptest.ResponseRecorder implements
// http.Flusher, so huma.StreamResponse's synchronous writes work against it as
// they would against a real connection.
func postAGUI(t *testing.T, handler http.Handler, tenant, user string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	path := "/v1/tenant/" + tenant + "/user/" + user + "/chat/agui"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// mustJSON marshals v the same way writeAGUIFrame does (encoding/json, struct
// field order), so a golden test can compare a frame's raw bytes to the marshal
// of the exact event struct that should have produced it.
func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return json.RawMessage(b)
}

// TestAGUI_GoldenEventSequence is the golden wire-format test (acceptance
// criterion 1): a normal two-token run must produce EXACTLY the ordered AG-UI
// floor frames, byte-for-byte — RUN_STARTED, TEXT_MESSAGE_START, one
// TEXT_MESSAGE_CONTENT per token, TEXT_MESSAGE_END, RUN_FINISHED — each a lone
// `data: {json}\n\n` frame with the right "type" and fields. newAGUIMessageID is
// pinned so the assistant messageId is deterministic and the bytes are exact.
func TestAGUI_GoldenEventSequence(t *testing.T) {
	orig := newAGUIMessageID
	newAGUIMessageID = func() string { return "m1" }
	defer func() { newAGUIMessageID = orig }()

	wm := newTestWorkingMemory(t)
	stub := &scriptedAGUIStub{tokens: []llm.Token{
		{Text: "Hello"},
		{Text: " world"},
		{Done: true, Usage: &llm.Usage{InputTokens: 3, OutputTokens: 2}},
	}}
	handler := newAGUITestRouter(t, stub, wm)

	rec := postAGUI(t, handler, "acme", "alice", aguiUserBody("t1", "r1", "hi"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	frames := parseAGUIFrames(t, rec.Body.Bytes())
	want := []json.RawMessage{
		mustJSON(t, aguiRunStarted{Type: aguiEventRunStarted, ThreadID: "t1", RunID: "r1"}),
		mustJSON(t, aguiTextMessageStart{Type: aguiEventTextMessageStart, MessageID: "m1", Role: "assistant"}),
		mustJSON(t, aguiTextMessageContent{Type: aguiEventTextMessageContent, MessageID: "m1", Delta: "Hello"}),
		mustJSON(t, aguiTextMessageContent{Type: aguiEventTextMessageContent, MessageID: "m1", Delta: " world"}),
		mustJSON(t, aguiTextMessageEnd{Type: aguiEventTextMessageEnd, MessageID: "m1"}),
		mustJSON(t, aguiRunFinished{Type: aguiEventRunFinished, ThreadID: "t1", RunID: "r1"}),
	}
	if len(frames) != len(want) {
		t.Fatalf("got %d frames, want %d:\n%s", len(frames), len(want), rec.Body.String())
	}
	for i := range want {
		if !bytes.Equal(frames[i].Raw, want[i]) {
			t.Errorf("frame %d bytes mismatch:\n got: %s\nwant: %s", i, frames[i].Raw, want[i])
		}
	}

	// The exact "type" order is the AG-UI floor: assert it explicitly too, so a
	// regression that keeps valid JSON but reorders events is caught named.
	wantTypes := []string{
		aguiEventRunStarted, aguiEventTextMessageStart,
		aguiEventTextMessageContent, aguiEventTextMessageContent,
		aguiEventTextMessageEnd, aguiEventRunFinished,
	}
	for i, wt := range wantTypes {
		if frames[i].Type != wt {
			t.Errorf("frame %d type = %q, want %q", i, frames[i].Type, wt)
		}
	}
}

// TestAGUI_ContentDeltasReconstructReply is acceptance criterion 2: the ordered
// TEXT_MESSAGE_CONTENT deltas concatenate to the full assistant reply, and that
// same reply is what StreamCore persisted to working memory (proving the adapter
// re-encodes the SAME pipeline output, not a divergent copy).
func TestAGUI_ContentDeltasReconstructReply(t *testing.T) {
	wm := newTestWorkingMemory(t)
	stub := &scriptedAGUIStub{tokens: []llm.Token{
		{Text: "The "},
		{Text: "quick "},
		{Text: "brown fox"},
		{Done: true},
	}}
	handler := newAGUITestRouter(t, stub, wm)

	rec := postAGUI(t, handler, "acme", "alice", aguiUserBody("t1", "r1", "hi"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	frames := parseAGUIFrames(t, rec.Body.Bytes())

	var reconstructed strings.Builder
	for _, f := range frames {
		if f.Type != aguiEventTextMessageContent {
			continue
		}
		var c aguiTextMessageContent
		if err := json.Unmarshal(f.Raw, &c); err != nil {
			t.Fatalf("unmarshal content frame: %v", err)
		}
		reconstructed.WriteString(c.Delta)
	}
	const wantReply = "The quick brown fox"
	if reconstructed.String() != wantReply {
		t.Errorf("reconstructed reply = %q, want %q", reconstructed.String(), wantReply)
	}

	// The persisted assistant turn must equal the delivered reply.
	key := memory.Key{Tenant: "acme", User: "alice", Session: "t1"}
	got, err := wm.Recent(context.Background(), key, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Recent returned %d messages, want 2 (user + assistant): %+v", len(got), got)
	}
	if got[0].Role != roleUser || got[0].Content != "hi" {
		t.Errorf("message 0 = %+v, want user %q", got[0], "hi")
	}
	if got[1].Role != roleAssistant || got[1].Content != wantReply {
		t.Errorf("message 1 = %+v, want assistant %q", got[1], wantReply)
	}
}

// TestAGUI_ErrorPathEmitsRunError is acceptance criterion 3: a mid-stream
// pipeline failure terminates the run with a RUN_ERROR frame carrying the error
// message and NO RUN_FINISHED. Any TEXT_MESSAGE_* frames emitted before the
// failure are fine; the terminal frame is what matters.
func TestAGUI_ErrorPathEmitsRunError(t *testing.T) {
	wm := newTestWorkingMemory(t)
	stub := &scriptedAGUIStub{tokens: []llm.Token{
		{Text: "Hel"},
		{Err: errors.New("boom-upstream-failure")},
	}}
	handler := newAGUITestRouter(t, stub, wm)

	rec := postAGUI(t, handler, "acme", "alice", aguiUserBody("t1", "r1", "hi"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	frames := parseAGUIFrames(t, rec.Body.Bytes())
	if len(frames) == 0 {
		t.Fatal("expected at least a RUN_STARTED and a RUN_ERROR frame")
	}

	var sawError, sawFinished bool
	for _, f := range frames {
		switch f.Type {
		case aguiEventRunError:
			sawError = true
			var e aguiRunError
			if err := json.Unmarshal(f.Raw, &e); err != nil {
				t.Fatalf("unmarshal RUN_ERROR: %v", err)
			}
			if !strings.Contains(e.Message, "boom-upstream-failure") {
				t.Errorf("RUN_ERROR message = %q, want it to contain the upstream error", e.Message)
			}
		case aguiEventRunFinished:
			sawFinished = true
		}
	}
	if !sawError {
		t.Error("expected a RUN_ERROR frame")
	}
	if sawFinished {
		t.Error("RUN_FINISHED must not be emitted when the run errors")
	}
	if last := frames[len(frames)-1].Type; last != aguiEventRunError {
		t.Errorf("last frame = %q, want RUN_ERROR (terminal)", last)
	}

	// A failed turn persists only the user message, never a partial assistant one.
	key := memory.Key{Tenant: "acme", User: "alice", Session: "t1"}
	got, err := wm.Recent(context.Background(), key, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	for _, m := range got {
		if m.Role == roleAssistant {
			t.Errorf("assistant turn persisted despite mid-stream error: %+v", m)
		}
	}
}

// TestAGUI_EmptyAndMalformedInputAre4xx is acceptance criterion 4: a request
// whose body is empty, malformed JSON, or carries no user message is rejected
// with a 4xx BEFORE any SSE streaming begins (a clean HTTP error, not a
// RUN_ERROR frame).
func TestAGUI_EmptyAndMalformedInputAre4xx(t *testing.T) {
	wm := newTestWorkingMemory(t)
	handler := newAGUITestRouter(t, llm.NewStubClient(), wm)

	noUserMsg := func() []byte {
		b, _ := json.Marshal(map[string]any{
			"threadId": "t1", "runId": "r1",
			"messages": []map[string]any{{"role": "assistant", "content": "prior reply"}},
		})
		return b
	}()

	cases := []struct {
		name string
		body []byte
	}{
		{"empty body", []byte("")},
		{"malformed json", []byte("{not valid json")},
		{"empty messages array", []byte(`{"threadId":"t1","runId":"r1","messages":[]}`)},
		{"no user message", noUserMsg},
		{"blank user message", aguiUserBody("t1", "r1", "   ")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postAGUI(t, handler, "acme", "alice", tc.body)
			if rec.Code < 400 || rec.Code >= 500 {
				t.Fatalf("status = %d, want a 4xx; body = %s", rec.Code, rec.Body.String())
			}
			// A pre-stream rejection must NOT have produced an SSE stream.
			if strings.Contains(rec.Body.String(), "RUN_STARTED") {
				t.Errorf("expected no SSE stream on a rejected request, got: %s", rec.Body.String())
			}
		})
	}
}

// TestAGUI_ThreadIdRunIdEcho is acceptance criterion 5: the request's threadId
// and runId are echoed verbatim into both RUN_STARTED and RUN_FINISHED, so the
// client can correlate the stream with the run it started.
func TestAGUI_ThreadIdRunIdEcho(t *testing.T) {
	wm := newTestWorkingMemory(t)
	stub := &scriptedAGUIStub{tokens: []llm.Token{{Text: "ok"}, {Done: true}}}
	handler := newAGUITestRouter(t, stub, wm)

	const threadID = "thread-abc-123"
	const runID = "run-xyz-789"
	rec := postAGUI(t, handler, "acme", "alice", aguiUserBody(threadID, runID, "hi"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	frames := parseAGUIFrames(t, rec.Body.Bytes())

	var started aguiRunStarted
	var finished aguiRunFinished
	var sawStarted, sawFinished bool
	for _, f := range frames {
		switch f.Type {
		case aguiEventRunStarted:
			if err := json.Unmarshal(f.Raw, &started); err != nil {
				t.Fatalf("unmarshal RUN_STARTED: %v", err)
			}
			sawStarted = true
		case aguiEventRunFinished:
			if err := json.Unmarshal(f.Raw, &finished); err != nil {
				t.Fatalf("unmarshal RUN_FINISHED: %v", err)
			}
			sawFinished = true
		}
	}
	if !sawStarted || !sawFinished {
		t.Fatalf("missing RUN_STARTED (%v) or RUN_FINISHED (%v)", sawStarted, sawFinished)
	}
	if started.ThreadID != threadID || started.RunID != runID {
		t.Errorf("RUN_STARTED echo = {thread:%q run:%q}, want {thread:%q run:%q}", started.ThreadID, started.RunID, threadID, runID)
	}
	if finished.ThreadID != threadID || finished.RunID != runID {
		t.Errorf("RUN_FINISHED echo = {thread:%q run:%q}, want {thread:%q run:%q}", finished.ThreadID, finished.RunID, threadID, runID)
	}
}

// TestAGUI_MultiMessageInputUsesLatestUser is the multi-message edge case: when
// the RunAgentInput carries a conversation (prior user/assistant turns plus a
// new user turn), the adapter runs the LATEST user message — not an earlier one,
// not an assistant message. The stub echoes the prompt it received, so the
// delivered reply reveals which message drove the turn.
func TestAGUI_MultiMessageInputUsesLatestUser(t *testing.T) {
	wm := newTestWorkingMemory(t)
	handler := newAGUITestRouter(t, llm.NewStubClient(), wm)

	body, err := json.Marshal(map[string]any{
		"threadId": "t1", "runId": "r1",
		"messages": []map[string]any{
			{"role": "user", "content": "EARLIER-USER-MARKER"},
			{"role": "assistant", "content": "ASSISTANT-MARKER"},
			{"role": "user", "content": "LATEST-USER-MARKER"},
		},
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	rec := postAGUI(t, handler, "acme", "alice", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	frames := parseAGUIFrames(t, rec.Body.Bytes())

	var reply strings.Builder
	for _, f := range frames {
		if f.Type != aguiEventTextMessageContent {
			continue
		}
		var c aguiTextMessageContent
		if err := json.Unmarshal(f.Raw, &c); err != nil {
			t.Fatalf("unmarshal content frame: %v", err)
		}
		reply.WriteString(c.Delta)
	}
	// The stub echoes the prompt verbatim (internal/llm/stub.go), so the latest
	// user marker must appear and the earlier one must not have driven the turn.
	if !strings.Contains(reply.String(), "LATEST-USER-MARKER") {
		t.Errorf("reply did not reflect the latest user message: %q", reply.String())
	}

	// The persisted user turn is the latest user message, confirming that is what
	// StreamCore ran.
	key := memory.Key{Tenant: "acme", User: "alice", Session: "t1"}
	got, err := wm.Recent(context.Background(), key, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) == 0 || got[0].Role != roleUser || got[0].Content != "LATEST-USER-MARKER" {
		t.Fatalf("persisted user turn = %+v, want the latest user message", got)
	}
}

// TestAGUI_TenantIsolation is acceptance criterion 6: a run served for one
// tenant never leaks into another tenant's working memory, and a second tenant
// can converse independently — the isolation the shared StreamCore pipeline
// guarantees (identity->ctx) holds through the AG-UI encoding.
func TestAGUI_TenantIsolation(t *testing.T) {
	wm := newTestWorkingMemory(t)
	handler := newAGUITestRouter(t, llm.NewStubClient(), wm)

	if rec := postAGUI(t, handler, "acme", "alice", aguiUserBody("s1", "r1", "acme-only-marker-42")); rec.Code != http.StatusOK {
		t.Fatalf("acme run status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Another tenant, same session key, sees nothing.
	otherKey := memory.Key{Tenant: "globex", User: "alice", Session: "s1"}
	got, err := wm.Recent(context.Background(), otherKey, 10)
	if err != nil {
		t.Fatalf("Recent (globex): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("globex saw %d messages before its own request, want 0: %+v", len(got), got)
	}

	// The second tenant converses independently, with no acme leakage.
	if rec := postAGUI(t, handler, "globex", "alice", aguiUserBody("s1", "r1", "globex-only-prompt")); rec.Code != http.StatusOK {
		t.Fatalf("globex run status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got, err = wm.Recent(context.Background(), otherKey, 10)
	if err != nil {
		t.Fatalf("Recent (globex, after run): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("globex returned %d messages, want 2 (user + assistant): %+v", len(got), got)
	}
	for _, m := range got {
		if strings.Contains(m.Content, "acme-only-marker-42") {
			t.Errorf("globex buffer leaked acme content: %+v", m)
		}
	}

	// acme's own history is unchanged by globex's request.
	acmeKey := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
	acmeGot, err := wm.Recent(context.Background(), acmeKey, 10)
	if err != nil {
		t.Fatalf("Recent (acme): %v", err)
	}
	if len(acmeGot) != 2 {
		t.Fatalf("acme history changed after globex's run: got %d, want 2: %+v", len(acmeGot), acmeGot)
	}
}
