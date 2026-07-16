package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
)

// newAISDKTestRouter builds the same chi + humachi wiring the real binary uses,
// registering ONLY the AI SDK adapter route with the same nil-defaulted
// dependencies chat_stream_test.go passes to RegisterStream (in-memory facts,
// no limiter/tools/meter/semantic/guard). It exercises the real route table and
// the real StreamCore pipeline — the adapter is verified end-to-end, never
// against a hand-rolled substitute.
func newAISDKTestRouter(t *testing.T, client llm.Client, wm *memory.WorkingMemory) http.Handler {
	t.Helper()
	factsStore, err := facts.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("facts.OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := factsStore.Close(); err != nil {
			t.Errorf("facts store Close: %v", err)
		}
	})

	router := chi.NewRouter()
	humaAPI := humachi.New(router, huma.DefaultConfig("Aisotropy-aisdk-test", "0.1.0"))
	RegisterAISDK(humaAPI, client, wm, factsStore, nil, nil, nil, nil, nil, nil, nil, nil, 0, 0, nil, nil, nil)
	return router
}

// aisdkTextMessage builds a single UIMessage with one text part, the v5+
// {role, parts:[{type:"text", text}]} shape (NOT a flat content string).
func aisdkTextMessage(role, id, text string) map[string]any {
	return map[string]any{
		"id":   id,
		"role": role,
		"parts": []map[string]any{
			{"type": "text", "text": text},
		},
	}
}

// postAISDK issues a POST against .../chat/aisdk with the given JSON body and
// returns the raw recorder (so tests can assert exact bytes AND headers AND
// status). httptest.ResponseRecorder implements http.Flusher, so the adapter's
// live per-part flushing works against it exactly as against a real connection.
func postAISDK(t *testing.T, handler http.Handler, tenant, user string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	path := "/v1/tenant/" + tenant + "/user/" + user + "/chat/aisdk"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// aisdkPart is one decoded UI Message Stream part (the JSON object inside a
// `data: {...}` frame). Only the fields this text-only adapter emits are
// modeled; unknown fields are ignored.
type aisdkPart struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Delta     string `json:"delta"`
	ErrorText string `json:"errorText"`
}

// parseAISDK splits a UI Message Stream body into its ordered parts plus a
// sawDone flag for the literal `data: [DONE]` terminator. It asserts every frame
// is `data: `-prefixed and separated by a blank line, so a framing regression
// (missing prefix, wrong separator) fails here rather than silently.
func parseAISDK(t *testing.T, body []byte) (parts []aisdkPart, sawDone bool) {
	t.Helper()
	for _, frame := range strings.Split(string(body), "\n\n") {
		frame = strings.TrimSpace(frame)
		if frame == "" {
			continue
		}
		if !strings.HasPrefix(frame, "data: ") {
			t.Fatalf("frame is not data:-prefixed: %q", frame)
		}
		payload := strings.TrimPrefix(frame, "data: ")
		if payload == "[DONE]" {
			sawDone = true
			continue
		}
		if sawDone {
			t.Fatalf("frame %q arrived after the [DONE] terminator", frame)
		}
		var p aisdkPart
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			t.Fatalf("unmarshal part %q: %v", payload, err)
		}
		parts = append(parts, p)
	}
	return parts, sawDone
}

// aisdkReconstruct concatenates the deltas of every text-delta part, rebuilding
// the assistant reply the client renders.
func aisdkReconstruct(parts []aisdkPart) string {
	var sb strings.Builder
	for _, p := range parts {
		if p.Type == aisdkPartTextDelta {
			sb.WriteString(p.Delta)
		}
	}
	return sb.String()
}

// aisdkFixedStub is an llm.Client emitting a caller-supplied, deterministic
// token sequence followed by a clean Done token. Deterministic output is what
// makes the golden byte-format assertion possible (the real echo stub's reply
// depends on the persona prompt string, which is not the adapter's contract).
type aisdkFixedStub struct{ tokens []string }

func (s *aisdkFixedStub) Name() string { return "aisdk-fixed-stub" }

func (s *aisdkFixedStub) StreamChat(ctx context.Context, _, _ string) (<-chan llm.Token, error) {
	out := make(chan llm.Token)
	go func() {
		defer close(out)
		for _, tk := range s.tokens {
			select {
			case <-ctx.Done():
				return
			case out <- llm.Token{Text: tk}:
			}
		}
		select {
		case <-ctx.Done():
		case out <- llm.Token{Done: true, Usage: &llm.Usage{InputTokens: 1, OutputTokens: 1}}:
		}
	}()
	return out, nil
}

// aisdkErrorStub is an llm.Client whose StreamChat fails to start, so StreamCore
// returns a pipeline error and the adapter must surface it as an `error` part.
type aisdkErrorStub struct{}

func (aisdkErrorStub) Name() string { return "aisdk-error-stub" }

func (aisdkErrorStub) StreamChat(context.Context, string, string) (<-chan llm.Token, error) {
	return nil, errBoom
}

var errBoom = &aisdkStubErr{}

type aisdkStubErr struct{}

func (*aisdkStubErr) Error() string { return "boom-upstream-unavailable" }

// aisdkCancelStub emits one token, signals firstTokenSent, then blocks on
// ctx.Done() so a mid-stream client cancellation is the only way it returns —
// the setup TestAISDK_ClientCancellationEmitsAbort needs to drive the abort
// path. Self-contained (not shared with chat_stream_test.go) to avoid coupling
// across concurrently-edited files.
type aisdkCancelStub struct {
	firstTokenSent chan struct{}
	observedCancel chan struct{}
}

func newAISDKCancelStub() *aisdkCancelStub {
	return &aisdkCancelStub{
		firstTokenSent: make(chan struct{}),
		observedCancel: make(chan struct{}),
	}
}

func (c *aisdkCancelStub) Name() string { return "aisdk-cancel-stub" }

func (c *aisdkCancelStub) StreamChat(ctx context.Context, _, _ string) (<-chan llm.Token, error) {
	out := make(chan llm.Token)
	go func() {
		defer close(out)
		select {
		case out <- llm.Token{Text: "partial "}:
		case <-ctx.Done():
			close(c.observedCancel)
			return
		}
		close(c.firstTokenSent)
		select {
		case <-ctx.Done():
			close(c.observedCancel)
		case <-time.After(5 * time.Second):
		}
	}()
	return out, nil
}

// TestAISDK_GoldenWireFormat is acceptance criterion (1): the exact response
// header, the exact ordered `data:`-framed parts, and the trailing
// `data: [DONE]`. Driven by a fixed-token stub so the ENTIRE body is
// byte-deterministic — including one delta with an embedded quote to prove JSON
// escaping and one with non-ASCII to prove UTF-8 passes through unescaped.
func TestAISDK_GoldenWireFormat(t *testing.T) {
	wm := newTestWorkingMemory(t)
	stub := &aisdkFixedStub{tokens: []string{"Hola", " món ", `she said "hi"`}}
	handler := newAISDKTestRouter(t, stub, wm)

	rec := postAISDK(t, handler, "acme", "alice", map[string]any{
		"id": "chat-1",
		"messages": []map[string]any{
			aisdkTextMessage("user", "m1", "hello"),
		},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	if got := rec.Header().Get("x-vercel-ai-ui-message-stream"); got != "v1" {
		t.Errorf("x-vercel-ai-ui-message-stream = %q, want v1", got)
	}

	// The full expected wire body, byte-for-byte. json.Marshal/Encoder escapes
	// the embedded quote to \" and leaves món (UTF-8) verbatim (HTML escaping is
	// disabled in the encoder).
	want := strings.Join([]string{
		`data: {"type":"start"}`, "",
		`data: {"type":"text-start","id":"0"}`, "",
		`data: {"type":"text-delta","id":"0","delta":"Hola"}`, "",
		`data: {"type":"text-delta","id":"0","delta":" món "}`, "",
		`data: {"type":"text-delta","id":"0","delta":"she said \"hi\""}`, "",
		`data: {"type":"text-end","id":"0"}`, "",
		`data: {"type":"finish"}`, "",
		"data: [DONE]", "",
		"",
	}, "\n")

	if got := rec.Body.String(); got != want {
		t.Errorf("wire body mismatch:\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

// TestAISDK_TextDeltaReconstructsReply is acceptance criterion (2): the ordered
// text-delta parts concatenate back into the exact assistant reply.
func TestAISDK_TextDeltaReconstructsReply(t *testing.T) {
	wm := newTestWorkingMemory(t)
	stub := &aisdkFixedStub{tokens: []string{"The ", "quick ", "brown ", "fox"}}
	handler := newAISDKTestRouter(t, stub, wm)

	rec := postAISDK(t, handler, "acme", "alice", map[string]any{
		"id":       "chat-1",
		"messages": []map[string]any{aisdkTextMessage("user", "m1", "hi")},
	})
	parts, sawDone := parseAISDK(t, rec.Body.Bytes())

	if !sawDone {
		t.Error("expected a trailing [DONE] terminator")
	}
	if got := aisdkReconstruct(parts); got != "The quick brown fox" {
		t.Errorf("reconstructed reply = %q, want %q", got, "The quick brown fox")
	}

	// Structural order guard: start, text-start, deltas..., text-end, finish.
	if len(parts) < 5 {
		t.Fatalf("expected at least 5 parts, got %d: %+v", len(parts), parts)
	}
	if parts[0].Type != aisdkPartStart {
		t.Errorf("first part = %q, want start", parts[0].Type)
	}
	if parts[1].Type != aisdkPartTextStart {
		t.Errorf("second part = %q, want text-start", parts[1].Type)
	}
	if parts[len(parts)-1].Type != aisdkPartFinish {
		t.Errorf("last part = %q, want finish", parts[len(parts)-1].Type)
	}
	if parts[len(parts)-2].Type != aisdkPartTextEnd {
		t.Errorf("second-to-last part = %q, want text-end", parts[len(parts)-2].Type)
	}
	// Every text part shares the one block id.
	for _, p := range parts {
		if (p.Type == aisdkPartTextStart || p.Type == aisdkPartTextDelta || p.Type == aisdkPartTextEnd) && p.ID != aisdkTextPartID {
			t.Errorf("text part %q has id %q, want %q", p.Type, p.ID, aisdkTextPartID)
		}
	}
}

// TestAISDK_ErrorPart is acceptance criterion (3): a pipeline failure surfaces
// as an in-band `error` part carrying the failure text (never an HTTP 5xx —
// the stream is already 200 with the body open).
func TestAISDK_ErrorPart(t *testing.T) {
	wm := newTestWorkingMemory(t)
	handler := newAISDKTestRouter(t, aisdkErrorStub{}, wm)

	rec := postAISDK(t, handler, "acme", "alice", map[string]any{
		"id":       "chat-1",
		"messages": []map[string]any{aisdkTextMessage("user", "m1", "trigger the failure")},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (errors are in-band parts); body = %s", rec.Code, rec.Body.String())
	}

	parts, _ := parseAISDK(t, rec.Body.Bytes())
	var errPart *aisdkPart
	for i := range parts {
		if parts[i].Type == aisdkPartError {
			errPart = &parts[i]
		}
	}
	if errPart == nil {
		t.Fatalf("expected an error part, got: %+v", parts)
	}
	if !strings.Contains(errPart.ErrorText, "boom-upstream-unavailable") {
		t.Errorf("error part errorText = %q, want it to contain the upstream failure", errPart.ErrorText)
	}
	// No `finish` on the failure path — the turn did not complete successfully.
	for _, p := range parts {
		if p.Type == aisdkPartFinish {
			t.Errorf("unexpected finish part on the error path: %+v", parts)
		}
	}
}

// TestAISDK_EmptyAndMalformedBody is acceptance criterion (4): a request with no
// usable user text, or malformed JSON, is rejected with a 4xx BEFORE any stream
// opens (no 200 + streaming body).
func TestAISDK_EmptyAndMalformedBody(t *testing.T) {
	wm := newTestWorkingMemory(t)
	handler := newAISDKTestRouter(t, llm.NewStubClient(), wm)

	cases := []struct {
		name string
		body any
		raw  string // when non-empty, sent verbatim instead of body (malformed JSON)
	}{
		{name: "empty messages", body: map[string]any{"id": "c1", "messages": []map[string]any{}}},
		{name: "no user message", body: map[string]any{"id": "c1", "messages": []map[string]any{aisdkTextMessage("assistant", "a1", "hi")}}},
		{name: "user message with empty text", body: map[string]any{"id": "c1", "messages": []map[string]any{aisdkTextMessage("user", "m1", "   ")}}},
		{name: "missing conversation id", body: map[string]any{"messages": []map[string]any{aisdkTextMessage("user", "m1", "hi")}}},
		{name: "malformed json", raw: `{"messages": [`},
	}

	path := "/v1/tenant/acme/user/alice/chat/aisdk"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var reader *bytes.Reader
			if tc.raw != "" {
				reader = bytes.NewReader([]byte(tc.raw))
			} else {
				raw, err := json.Marshal(tc.body)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				reader = bytes.NewReader(raw)
			}
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, reader)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code < 400 || rec.Code >= 500 {
				t.Fatalf("status = %d, want a 4xx; body = %s", rec.Code, rec.Body.String())
			}
			// A rejected request must not leak an SSE stream body.
			if strings.Contains(rec.Body.String(), "data: ") {
				t.Errorf("4xx response unexpectedly contains an SSE stream: %s", rec.Body.String())
			}
		})
	}
}

// TestAISDK_MultiMessageUsesLatestUserTurn is acceptance criterion (5): given a
// full transcript, only the LATEST user message's text becomes the prompt. The
// echo stub reflects the prompt, and the persisted user turn confirms it.
func TestAISDK_MultiMessageUsesLatestUserTurn(t *testing.T) {
	wm := newTestWorkingMemory(t)
	handler := newAISDKTestRouter(t, llm.NewStubClient(), wm)

	rec := postAISDK(t, handler, "acme", "alice", map[string]any{
		"id": "chat-1",
		"messages": []map[string]any{
			aisdkTextMessage("user", "m1", "STALE-EARLIER-QUESTION"),
			aisdkTextMessage("assistant", "a1", "an earlier answer"),
			aisdkTextMessage("user", "m2", "FRESH-LATEST-QUESTION"),
		},
	})
	parts, _ := parseAISDK(t, rec.Body.Bytes())
	reply := aisdkReconstruct(parts)

	// The echo stub reflects the prompt verbatim, so the latest turn surfaces
	// and the stale earlier turn does not.
	if !strings.Contains(reply, "FRESH-LATEST-QUESTION") {
		t.Errorf("reply does not reflect the latest user turn: %q", reply)
	}
	if strings.Contains(reply, "STALE-EARLIER-QUESTION") {
		t.Errorf("reply reflects a STALE earlier user turn (should use only the latest): %q", reply)
	}

	// The persisted user turn is the latest message's text, not the transcript.
	key := memory.Key{Tenant: "acme", User: "alice", Session: "chat-1"}
	got, err := wm.Recent(context.Background(), key, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Recent returned %d messages, want 2 (user + assistant): %+v", len(got), got)
	}
	if got[0].Role != roleUser || got[0].Content != "FRESH-LATEST-QUESTION" {
		t.Errorf("persisted user turn = %+v, want %q", got[0], "FRESH-LATEST-QUESTION")
	}
}

// TestAISDK_TenantIsolation is acceptance criterion (6): a request under one
// tenant never surfaces another tenant's data. TenantA writes a marker; TenantB
// (same session id, same user id) must neither see it in its own working memory
// nor have it echoed back through the hydrated system prompt, and tenantA's
// buffer must be left intact.
func TestAISDK_TenantIsolation(t *testing.T) {
	wm := newTestWorkingMemory(t)
	handler := newAISDKTestRouter(t, llm.NewStubClient(), wm)

	const marker = "SECRET-MARKER-ALPHA-42"

	// TenantA writes the marker.
	postAISDK(t, handler, "acme", "alice", map[string]any{
		"id":       "s1",
		"messages": []map[string]any{aisdkTextMessage("user", "m1", marker)},
	})

	// TenantB (globex), same session/user ids, asks a question. Its reply is
	// hydrated only from globex's (empty) memory — the marker must not appear.
	rec := postAISDK(t, handler, "globex", "alice", map[string]any{
		"id":       "s1",
		"messages": []map[string]any{aisdkTextMessage("user", "m1", "what did I tell you earlier?")},
	})
	parts, _ := parseAISDK(t, rec.Body.Bytes())
	if reply := aisdkReconstruct(parts); strings.Contains(reply, marker) {
		t.Fatalf("cross-tenant leak: globex's reply echoed acme's marker: %q", reply)
	}

	// TenantB's working memory holds only its own turn.
	globexKey := memory.Key{Tenant: "globex", User: "alice", Session: "s1"}
	globexGot, err := wm.Recent(context.Background(), globexKey, 10)
	if err != nil {
		t.Fatalf("Recent (globex): %v", err)
	}
	if len(globexGot) != 2 {
		t.Fatalf("globex buffer = %d messages, want 2: %+v", len(globexGot), globexGot)
	}
	for _, m := range globexGot {
		if strings.Contains(m.Content, marker) {
			t.Errorf("globex buffer leaked acme's marker: %+v", m)
		}
	}

	// TenantA's buffer is intact and still holds the marker as its user turn.
	acmeKey := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
	acmeGot, err := wm.Recent(context.Background(), acmeKey, 10)
	if err != nil {
		t.Fatalf("Recent (acme): %v", err)
	}
	if len(acmeGot) != 2 {
		t.Fatalf("acme buffer changed after globex's request: got %d messages, want 2: %+v", len(acmeGot), acmeGot)
	}
	if acmeGot[0].Role != roleUser || acmeGot[0].Content != marker {
		t.Errorf("acme user turn = %+v, want marker %q", acmeGot[0], marker)
	}
}

// TestAISDK_ClientCancellationEmitsAbort exercises the ctx-cancellation path:
// StreamCore stops the upstream LLM (constitutional streaming gate) and the
// adapter emits an `abort` part (no `finish`, no `[DONE]`), and no assistant
// turn is persisted.
func TestAISDK_ClientCancellationEmitsAbort(t *testing.T) {
	wm := newTestWorkingMemory(t)
	stub := newAISDKCancelStub()
	handler := newAISDKTestRouter(t, stub, wm)

	raw, err := json.Marshal(map[string]any{
		"id":       "s1",
		"messages": []map[string]any{aisdkTextMessage("user", "m1", "hello")},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := "/v1/tenant/acme/user/alice/chat/aisdk"
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(rec, req)
	}()

	select {
	case <-stub.firstTokenSent:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first token")
	}

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTP did not return after client cancellation")
	}

	// The upstream producer itself observed the cancellation (not merely the
	// HTTP handler stopping its read) — the constitutional streaming gate.
	select {
	case <-stub.observedCancel:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream producer never observed ctx cancellation")
	}

	parts, sawDone := parseAISDK(t, rec.Body.Bytes())
	var sawAbort, sawFinish bool
	for _, p := range parts {
		switch p.Type {
		case aisdkPartAbort:
			sawAbort = true
		case aisdkPartFinish:
			sawFinish = true
		}
	}
	if !sawAbort {
		t.Errorf("expected an abort part on cancellation, got: %+v", parts)
	}
	if sawFinish {
		t.Errorf("unexpected finish part on the cancellation path: %+v", parts)
	}
	if sawDone {
		t.Errorf("unexpected [DONE] terminator on the cancellation path")
	}

	// No assistant turn persisted — only the user turn (appended before the LLM
	// call), per StreamCore's ordering.
	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
	got, err := wm.Recent(context.Background(), key, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	for _, m := range got {
		if m.Role == roleAssistant {
			t.Errorf("assistant turn persisted despite cancellation: %+v", m)
		}
	}
}
