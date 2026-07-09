package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/memory"
)

// newTestRouter builds the same wiring as cmd/aisotropyd/main.go (chi +
// humachi + Register + RegisterStream) so these tests exercise the real
// route table, not a hand-rolled substitute. Facts memory uses an ephemeral
// in-memory SQLiteStore + the deterministic HeuristicExtractor, so these
// tests never depend on network access or external services.
func newTestRouter(t *testing.T, client llm.Client, wm *memory.WorkingMemory) http.Handler {
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
	humaAPI := humachi.New(router, huma.DefaultConfig("Aisotropy-test", "0.1.0"))
	Register(humaAPI, client, wm, factsStore, nil, nil)
	RegisterStream(humaAPI, client, wm, factsStore, nil, nil)
	return router
}

// newTestWorkingMemory returns an in-memory WorkingMemory using the
// deterministic NoopSummarizer, so tests never depend on network access or
// API credentials.
func newTestWorkingMemory(t *testing.T) *memory.WorkingMemory {
	t.Helper()
	wm, err := memory.OpenInMemory(0, memory.NoopSummarizer{})
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() {
		if err := wm.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return wm
}

// sseEvent is one parsed "event: ...\ndata: ...\n\n" block from an SSE body.
type sseEvent struct {
	Event string
	Data  json.RawMessage
}

// parseSSE splits a raw SSE response body into its individual events.
func parseSSE(t *testing.T, body []byte) []sseEvent {
	t.Helper()
	var events []sseEvent
	for _, block := range strings.Split(string(body), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var ev sseEvent
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				ev.Event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ev.Data = json.RawMessage(strings.TrimPrefix(line, "data: "))
			}
		}
		if ev.Data != nil {
			events = append(events, ev)
		}
	}
	return events
}

// postChatStream issues a single non-streaming-client POST against
// /v1/tenant/{tenant}/user/{user}/chat/stream and returns the parsed SSE
// events. httptest.ResponseRecorder implements http.Flusher directly (no
// wrapping needed), so sse.Register's synchronous send() calls work against
// it exactly as they would against a real net/http connection.
func postChatStream(t *testing.T, handler http.Handler, tenant, user, prompt, sessionID string) []sseEvent {
	t.Helper()
	reqBody, err := json.Marshal(ChatRequest{Prompt: prompt, SessionID: sessionID})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	path := "/v1/tenant/" + tenant + "/user/" + user + "/chat/stream"
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST %s: status = %d, body = %s", path, rec.Code, rec.Body.String())
	}
	return parseSSE(t, rec.Body.Bytes())
}

// assistantText concatenates every "token" event's Text field, reconstructing
// the full assistant reply the same way chat_stream.go's RegisterStream does
// internally before persisting it.
func assistantText(t *testing.T, events []sseEvent) string {
	t.Helper()
	var sb strings.Builder
	for _, ev := range events {
		if ev.Event != "token" {
			continue
		}
		var tok StreamTokenEvent
		if err := json.Unmarshal(ev.Data, &tok); err != nil {
			t.Fatalf("unmarshal token event: %v", err)
		}
		sb.WriteString(tok.Text)
	}
	return sb.String()
}

// TestChatStream_TokensThenDone verifies a stream request returns SSE token
// events followed by a done event (acceptance criterion 1).
func TestChatStream_TokensThenDone(t *testing.T) {
	wm := newTestWorkingMemory(t)
	handler := newTestRouter(t, llm.NewStubClient(), wm)

	events := postChatStream(t, handler, "acme", "alice", "hello there", "s1")
	if len(events) < 2 {
		t.Fatalf("expected at least one token event and a done event, got %d events", len(events))
	}

	var sawToken, sawDone bool
	for i, ev := range events {
		switch ev.Event {
		case "token":
			sawToken = true
			if sawDone {
				t.Errorf("event %d: token event arrived after done event", i)
			}
		case "done":
			sawDone = true
		case "error":
			t.Fatalf("event %d: unexpected error event: %s", i, ev.Data)
		default:
			t.Errorf("event %d: unexpected event type %q", i, ev.Event)
		}
	}
	if !sawToken {
		t.Error("expected at least one token event")
	}
	if !sawDone {
		t.Error("expected a done event")
	}
	if events[len(events)-1].Event != "done" {
		t.Errorf("expected the last event to be done, got %q", events[len(events)-1].Event)
	}
}

// TestChatStream_PersistsUserAndAssistantTurns verifies that both the user
// message and the completed assistant response are persisted to working
// memory once the stream finishes successfully (acceptance criterion 2).
func TestChatStream_PersistsUserAndAssistantTurns(t *testing.T) {
	wm := newTestWorkingMemory(t)
	handler := newTestRouter(t, llm.NewStubClient(), wm)

	events := postChatStream(t, handler, "acme", "alice", "hello there", "s1")
	wantReply := assistantText(t, events)
	if wantReply == "" {
		t.Fatal("no assistant text accumulated from token events")
	}

	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
	got, err := wm.Recent(context.Background(), key, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Recent returned %d messages, want 2 (user + assistant): %+v", len(got), got)
	}
	if got[0].Role != "user" || got[0].Content != "hello there" {
		t.Errorf("message 0 = %+v, want user message %q", got[0], "hello there")
	}
	if got[1].Role != "assistant" || got[1].Content != wantReply {
		t.Errorf("message 1 = %+v, want assistant message %q", got[1], wantReply)
	}
}

// TestChatStream_SecondRequestSeesPriorHistory verifies that a second
// request on the same tenant/user/session accumulates history: the system
// prompt fed to the LLM (and hence, via the stub's echo, the returned
// tokens) includes the first turn's content (acceptance criterion 3).
func TestChatStream_SecondRequestSeesPriorHistory(t *testing.T) {
	wm := newTestWorkingMemory(t)
	handler := newTestRouter(t, llm.NewStubClient(), wm)

	firstPrompt := "remember the launch date is unique-marker-77"
	postChatStream(t, handler, "acme", "alice", firstPrompt, "s1")

	secondEvents := postChatStream(t, handler, "acme", "alice", "what did I just tell you?", "s1")
	secondReply := assistantText(t, secondEvents)

	// StubClient's echoed reply includes the system prompt verbatim (see
	// internal/llm/stub.go's buildStubWords: `system=%q`), so if
	// hydrateSystemPrompt folded the first turn's Recent() messages into the
	// system prompt, the marker from turn one surfaces in turn two's tokens.
	if !strings.Contains(secondReply, "unique-marker-77") {
		t.Errorf("second request's reply does not reflect first turn's history: %q", secondReply)
	}

	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
	got, err := wm.Recent(context.Background(), key, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("Recent returned %d messages after two turns, want 4 (accumulated history): %+v", len(got), got)
	}
}

// TestChatStream_TenantIsolation verifies that a different tenant's key does
// not see the first tenant's messages (acceptance criterion 4).
func TestChatStream_TenantIsolation(t *testing.T) {
	wm := newTestWorkingMemory(t)
	handler := newTestRouter(t, llm.NewStubClient(), wm)

	postChatStream(t, handler, "acme", "alice", "acme-only secret prompt", "s1")

	otherKey := memory.Key{Tenant: "globex", User: "alice", Session: "s1"}
	got, err := wm.Recent(context.Background(), otherKey, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected tenant %q to see 0 messages from tenant %q, got %d: %+v", "globex", "acme", len(got), got)
	}

	// Confirm the second tenant can independently converse without any
	// cross-tenant leakage.
	events := postChatStream(t, handler, "globex", "alice", "globex-only prompt", "s1")
	if len(events) == 0 {
		t.Fatal("expected globex tenant's own request to stream normally")
	}
	got, err = wm.Recent(context.Background(), otherKey, 10)
	if err != nil {
		t.Fatalf("Recent (after globex request): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Recent for globex tenant returned %d messages, want 2: %+v", len(got), got)
	}
	for _, m := range got {
		if strings.Contains(m.Content, "acme-only") {
			t.Errorf("globex tenant's buffer leaked acme content: %+v", m)
		}
	}

	acmeKey := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
	acmeGot, err := wm.Recent(context.Background(), acmeKey, 10)
	if err != nil {
		t.Fatalf("Recent (acme after globex request): %v", err)
	}
	if len(acmeGot) != 2 {
		t.Fatalf("acme tenant's history changed after globex's request: got %d messages, want 2: %+v", len(acmeGot), acmeGot)
	}
}
