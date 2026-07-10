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
	"github.com/Luum-Home/aisotropy/internal/semantic"
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
	Register(humaAPI, client, wm, factsStore, nil, nil, nil)
	RegisterStream(humaAPI, client, wm, factsStore, nil, nil, nil, nil, nil, nil, nil, 0, 0, nil)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, bytes.NewReader(reqBody))
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

// TestHydrateSystemPrompt_SemanticSearchExcludesTurnKind is the ADR-0017
// phase-3 DoD (acceptance criterion 7c): hydrateSystemPrompt's semantic RAG
// search targets only Document+Fact kinds, so a KindTurn point is never
// returned by the hydration search path even when it is the closer match.
func TestHydrateSystemPrompt_SemanticSearchExcludesTurnKind(t *testing.T) {
	ctx := context.Background()
	emb := semantic.NewHashEmbedder(0)
	semStore := semantic.NewInMemoryStore(emb.Dimension())

	scope, err := facts.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("facts.NewScope: %v", err)
	}
	semScope, err := semantic.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("semantic.NewScope: %v", err)
	}

	const query = "database migration plan"
	vecs, err := emb.Embed(ctx, []string{query})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	queryVec := vecs[0]

	// Both points carry the query's own vector, so a Turn/Fact-blind search
	// would rank them identically -- the kind filter is what must exclude
	// the Turn point, not similarity ranking.
	turnPoint := semantic.Point{
		ID: semantic.PointID(semScope, semantic.KindTurn, "turn-1"), TenantID: "acme", UserID: "alice",
		Kind: semantic.KindTurn, Content: "RAW-TURN-MARKER-SHOULD-NOT-APPEAR", Vector: queryVec,
	}
	factPoint := semantic.Point{
		ID: semantic.PointID(semScope, semantic.KindFact, "fact-1"), TenantID: "acme", UserID: "alice",
		Kind: semantic.KindFact, Content: "DISTILLED-FACT-MARKER-SHOULD-APPEAR", Vector: queryVec,
	}
	if err := semStore.Upsert(ctx, semScope, []semantic.Point{turnPoint, factPoint}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
	prompt, err := hydrateSystemPrompt(ctx, nil, nil, semStore, emb, nil, key, scope, "p1", query)
	if err != nil {
		t.Fatalf("hydrateSystemPrompt: %v", err)
	}

	if !strings.Contains(prompt, "DISTILLED-FACT-MARKER-SHOULD-APPEAR") {
		t.Fatalf("expected the Fact-kind point in the hydrated prompt, got: %s", prompt)
	}
	if strings.Contains(prompt, "RAW-TURN-MARKER-SHOULD-NOT-APPEAR") {
		t.Fatalf("KindTurn point leaked into hydration; the primary RAG injection must exclude it: %s", prompt)
	}
}

// TestHydrateSystemPrompt_MergesTenantSharedDocuments is the ADR-0019
// acceptance criterion: hydrateSystemPrompt merges a tenant-shared Document
// (ingested once, no user) with the requesting user's own semantic results,
// so a DIFFERENT user of the same tenant still sees it, but another tenant
// never does.
func TestHydrateSystemPrompt_MergesTenantSharedDocuments(t *testing.T) {
	ctx := context.Background()
	emb := semantic.NewHashEmbedder(0)
	semStore := semantic.NewInMemoryStore(emb.Dimension())

	const query = "employee benefits enrollment"
	vecs, err := emb.Embed(ctx, []string{query})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	queryVec := vecs[0]

	tenantScope, err := semantic.NewTenantScope("acme")
	if err != nil {
		t.Fatalf("semantic.NewTenantScope: %v", err)
	}
	sharedDoc := semantic.Point{
		ID: semantic.PointID(tenantScope, semantic.KindDocument, "handbook"), TenantID: "acme", UserID: "",
		Kind: semantic.KindDocument, Content: "TENANT-SHARED-HANDBOOK-MARKER", Vector: queryVec,
	}
	if err := semStore.Upsert(ctx, tenantScope, []semantic.Point{sharedDoc}); err != nil {
		t.Fatalf("Upsert (tenant-shared): %v", err)
	}

	// A DIFFERENT user (bob) of the same tenant, who never ingested anything
	// of his own, still gets the tenant-shared document injected.
	factsScope, err := facts.NewScope("acme", "bob")
	if err != nil {
		t.Fatalf("facts.NewScope: %v", err)
	}
	key := memory.Key{Tenant: "acme", User: "bob", Session: "s1"}
	prompt, err := hydrateSystemPrompt(ctx, nil, nil, semStore, emb, nil, key, factsScope, "p1", query)
	if err != nil {
		t.Fatalf("hydrateSystemPrompt: %v", err)
	}
	if !strings.Contains(prompt, "TENANT-SHARED-HANDBOOK-MARKER") {
		t.Fatalf("expected the tenant-shared document in bob's hydrated prompt, got: %s", prompt)
	}

	// A user of a DIFFERENT tenant must never see it.
	otherFactsScope, err := facts.NewScope("globex", "carol")
	if err != nil {
		t.Fatalf("facts.NewScope (globex): %v", err)
	}
	otherKey := memory.Key{Tenant: "globex", User: "carol", Session: "s1"}
	otherPrompt, err := hydrateSystemPrompt(ctx, nil, nil, semStore, emb, nil, otherKey, otherFactsScope, "p1", query)
	if err != nil {
		t.Fatalf("hydrateSystemPrompt (globex): %v", err)
	}
	if strings.Contains(otherPrompt, "TENANT-SHARED-HANDBOOK-MARKER") {
		t.Fatalf("cross-tenant leak: globex's hydrated prompt contains acme's tenant-shared document: %s", otherPrompt)
	}
}

// TestHydrateSystemPrompt_TenantSharedExcludesFactAndTurn is the ADR-0019
// "Mandatory" guardrail exercised at the hydration call site: even though
// hydrateSystemPrompt's tenant-shared search always passes KindDocument as
// its only kind filter, this test proves the effect end-to-end — a Fact- or
// Turn-kind point sitting in the tenant-shared bucket never reaches the
// hydrated prompt.
func TestHydrateSystemPrompt_TenantSharedExcludesFactAndTurn(t *testing.T) {
	ctx := context.Background()
	emb := semantic.NewHashEmbedder(0)
	semStore := semantic.NewInMemoryStore(emb.Dimension())

	const query = "quarterly roadmap"
	vecs, err := emb.Embed(ctx, []string{query})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	queryVec := vecs[0]

	tenantScope, err := semantic.NewTenantScope("acme")
	if err != nil {
		t.Fatalf("semantic.NewTenantScope: %v", err)
	}
	docPt := semantic.Point{
		ID: semantic.PointID(tenantScope, semantic.KindDocument, "roadmap-doc"), TenantID: "acme", UserID: "",
		Kind: semantic.KindDocument, Content: "TENANT-SHARED-DOC-MARKER", Vector: queryVec,
	}
	factPt := semantic.Point{
		ID: semantic.PointID(tenantScope, semantic.KindFact, "roadmap-fact"), TenantID: "acme", UserID: "",
		Kind: semantic.KindFact, Content: "TENANT-SHARED-FACT-MARKER-SHOULD-NOT-APPEAR", Vector: queryVec,
	}
	turnPt := semantic.Point{
		ID: semantic.PointID(tenantScope, semantic.KindTurn, "roadmap-turn"), TenantID: "acme", UserID: "",
		Kind: semantic.KindTurn, Content: "TENANT-SHARED-TURN-MARKER-SHOULD-NOT-APPEAR", Vector: queryVec,
	}
	if err := semStore.Upsert(ctx, tenantScope, []semantic.Point{docPt, factPt, turnPt}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	factsScope, err := facts.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("facts.NewScope: %v", err)
	}
	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
	prompt, err := hydrateSystemPrompt(ctx, nil, nil, semStore, emb, nil, key, factsScope, "p1", query)
	if err != nil {
		t.Fatalf("hydrateSystemPrompt: %v", err)
	}
	if !strings.Contains(prompt, "TENANT-SHARED-DOC-MARKER") {
		t.Fatalf("expected the tenant-shared Document in the hydrated prompt, got: %s", prompt)
	}
	if strings.Contains(prompt, "TENANT-SHARED-FACT-MARKER-SHOULD-NOT-APPEAR") {
		t.Fatalf("tenant-shared Fact-kind point leaked into hydration: %s", prompt)
	}
	if strings.Contains(prompt, "TENANT-SHARED-TURN-MARKER-SHOULD-NOT-APPEAR") {
		t.Fatalf("tenant-shared Turn-kind point leaked into hydration: %s", prompt)
	}
}
