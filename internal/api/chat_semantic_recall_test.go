package api

// Route-parity regression tests for the chat routes' semantic recall.
//
// The product gap these lock shut: the non-streaming /chat route (and the
// in-process App.Turn that shares ChatCore) historically wired a nil semantic
// store into hydration, so a fact consolidated into a KindFact vector was
// paraphrase-recallable on /chat/stream but NOT on /chat — recall there could
// only hit the facts tier's FTS5 keyword search, which cannot bridge a
// paraphrase. These tests drive the REAL pipeline end to end (HTTP route ->
// transactional outbox -> consolidation.Sink -> engram dual-index -> semantic
// hydration) with the in-process semantic store and a stub embedder, and prove:
//
//  1. a fact stated under (tenant, user) in session A is semantically
//     recallable via a PARAPHRASE (zero content-word overlap, so FTS5 cannot
//     be the retriever) in session B on BOTH routes; and
//  2. that recall never crosses tenant (or user) scope: the same paraphrase
//     under another tenant/user retrieves nothing.

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

	"github.com/Luum-Home/aisotropy/internal/consolidation"
	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// factStatement is the session-A user turn. The HeuristicExtractor's "remember
// that" rule distills it into a fact (importance 0.9) whose content is
// factContent, and the engram facade dual-indexes that content as a KindFact
// vector during consolidation.
const (
	factStatement = "Remember that my badge code is kept inside the green ceramic jar"
	factContent   = "my badge code is kept inside the green ceramic jar"

	// paraphraseQuery shares ZERO content tokens with factContent ("door",
	// "pass", "location" vs "badge", "code", "green", "ceramic", "jar", ...),
	// so the facts tier's FTS5 keyword search cannot retrieve the fact — only
	// a vector match through the semantic tier can. That is exactly what makes
	// this a paraphrase-recall test rather than a keyword-recall test.
	paraphraseQuery = "door pass location?"
)

// conceptPair maps a keyword to the unit vector its containing text embeds to.
type conceptPair struct {
	keyword string
	vector  []float32
}

// conceptEmbedder is a deterministic stub semantic.Embedder that emulates
// what a real embedding model does for this fixture: it maps two different
// surface forms of the same concept ("green ceramic jar" in the stored fact,
// "door pass" in the recall query) to the SAME vector, and everything else to
// a distant default. Deterministic, offline, no network — and deliberately NOT
// a RoleEmbedder, so EmbedQuery/EmbedPassage degrade to plain Embed.
type conceptEmbedder struct {
	concepts []conceptPair
}

func (e conceptEmbedder) Dimension() int { return 4 }

func (e conceptEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, txt := range texts {
		lc := strings.ToLower(txt)
		vec := []float32{0, 0, 0, 1} // default: orthogonal to every concept
		for _, c := range e.concepts {
			if strings.Contains(lc, c.keyword) {
				vec = c.vector
				break
			}
		}
		out[i] = vec
	}
	return out, nil
}

// newSemanticRecallFixture builds the full two-route router (Register +
// RegisterStream) over ONE shared working memory, facts store, semantic store
// and stub embedder — the same parity wiring aisotropy/app.go now performs —
// plus the consolidation sink that drains the transactional outbox into the
// facts + semantic tiers. It returns the router, the capturing client (to
// assert what actually reached the model), the working memory (to drain the
// outbox) and the sink.
func newSemanticRecallFixture(t *testing.T) (http.Handler, *capturingClient, *memory.WorkingMemory, consolidation.Sink) {
	t.Helper()

	wm := newTestWorkingMemory(t)

	factsStore, err := facts.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("facts.OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := factsStore.Close(); err != nil {
			t.Errorf("facts store Close: %v", err)
		}
	})

	emb := conceptEmbedder{concepts: []conceptPair{
		{keyword: "green ceramic jar", vector: []float32{1, 0, 0, 0}},
		{keyword: "door pass", vector: []float32{1, 0, 0, 0}},
	}}
	semStore := semantic.NewInMemoryStore(emb.Dimension())
	t.Cleanup(func() { _ = semStore.Close() })

	client := &capturingClient{reply: `{"reply":"ok"}`}

	router := chi.NewRouter()
	humaAPI := humachi.New(router, huma.DefaultConfig("semantic-recall-test", "0.1.0"))
	// Parity by construction: both routes receive the SAME semStore + embedder.
	Register(humaAPI, client, wm, factsStore, nil, nil, nil, nil, semStore, emb, nil, nil, nil)
	RegisterStream(humaAPI, client, wm, factsStore, nil, nil, nil, nil, semStore, emb, nil, nil, 0, 0, nil, nil, nil)

	sink := consolidation.Sink{
		Facts:     factsStore,
		Extractor: facts.HeuristicExtractor{},
		Semantic:  semStore,
		Embedder:  emb,
	}
	return router, client, wm, sink
}

// postChat drives the non-streaming route for (tenant, user, session) and
// fails the test on a non-200.
func postChat(t *testing.T, handler http.Handler, tenant, user, session, prompt string) {
	t.Helper()
	body, err := json.Marshal(ChatRequest{Prompt: prompt, SessionID: session})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	path := "/v1/tenant/" + tenant + "/user/" + user + "/chat"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST %s: status = %d, body = %s", path, rec.Code, rec.Body.String())
	}
}

// postStream drives the streaming route for (tenant, user, session) and fails
// the test on a non-200 or an SSE error event.
func postStream(t *testing.T, handler http.Handler, tenant, user, session, prompt string) {
	t.Helper()
	events := postChatStreamSession(t, handler, tenant, user, prompt, session)
	for _, ev := range events {
		if ev.Event == "error" {
			t.Fatalf("unexpected SSE error event: %s", ev.Data)
		}
	}
}

// postChatStreamSession is postChatStream with an explicit session id.
func postChatStreamSession(t *testing.T, handler http.Handler, tenant, user, prompt, session string) []sseEvent {
	t.Helper()
	reqBody, err := json.Marshal(ChatRequest{Prompt: prompt, SessionID: session})
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

// seedConsolidatedFact states factStatement under (tenant, user) on the
// NON-streaming route in session "seed" and drains the outbox through the
// consolidation sink — the real pipeline that distills the statement into a
// facts-tier row dual-indexed as a KindFact vector.
func seedConsolidatedFact(t *testing.T, handler http.Handler, wm *memory.WorkingMemory, sink consolidation.Sink, tenant, user string) {
	t.Helper()
	postChat(t, handler, tenant, user, "seed", factStatement)
	n, err := wm.RecoverOutbox(context.Background(), sink)
	if err != nil {
		t.Fatalf("RecoverOutbox: %v", err)
	}
	if n != 1 {
		t.Fatalf("RecoverOutbox consolidated %d events, want 1", n)
	}
}

// TestChatRoutes_SemanticRecall_ParaphraseAcrossSessions is the route-parity
// acceptance test: a fact stated in session A is recallable via a paraphrase
// (no keyword overlap) in a FRESH session on BOTH the non-streaming and the
// streaming route, through the same consolidation -> KindFact -> semantic
// hydration pipeline.
func TestChatRoutes_SemanticRecall_ParaphraseAcrossSessions(t *testing.T) {
	handler, client, wm, sink := newSemanticRecallFixture(t)
	seedConsolidatedFact(t, handler, wm, sink, "acme", "alice")

	// Session B (fresh session, empty working memory for it): the paraphrase
	// must surface the consolidated fact via the SEMANTIC tier on the
	// NON-streaming route — the route that historically could not recall it.
	postChat(t, handler, "acme", "alice", "s-recall-chat", paraphraseQuery)
	system := client.lastSystem()
	if !strings.Contains(system, factContent) {
		t.Fatalf("non-streaming /chat: consolidated fact not recalled via paraphrase; system prompt:\n%s", system)
	}
	if !strings.Contains(system, "semantic memory") {
		t.Fatalf("non-streaming /chat: recalled context did not come through the semantic-memory section; system prompt:\n%s", system)
	}

	// Same paraphrase, another fresh session, STREAMING route: identical recall.
	postStream(t, handler, "acme", "alice", "s-recall-stream", paraphraseQuery)
	system = client.lastSystem()
	if !strings.Contains(system, factContent) {
		t.Fatalf("streaming /chat/stream: consolidated fact not recalled via paraphrase; system prompt:\n%s", system)
	}
	if !strings.Contains(system, "semantic memory") {
		t.Fatalf("streaming /chat/stream: recalled context did not come through the semantic-memory section; system prompt:\n%s", system)
	}
}

// TestChatRoutes_SemanticRecall_NeverCrossesTenantOrUser is the isolation half
// of the acceptance criteria: the SAME paraphrase that recalls the fact for
// (acme, alice) must retrieve NOTHING for another tenant — or another user of
// the same tenant — on either route. The semantic store search is scoped
// (tenant, user) in-scan, so cross-scope recall is not merely ranked lower; it
// is unreachable.
func TestChatRoutes_SemanticRecall_NeverCrossesTenantOrUser(t *testing.T) {
	handler, client, wm, sink := newSemanticRecallFixture(t)
	seedConsolidatedFact(t, handler, wm, sink, "acme", "alice")

	cases := []struct {
		name         string
		tenant, user string
	}{
		{name: "cross-tenant", tenant: "globex", user: "alice"},
		{name: "cross-user-same-tenant", tenant: "acme", user: "bob"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			postChat(t, handler, tc.tenant, tc.user, "s-iso-chat", paraphraseQuery)
			if system := client.lastSystem(); strings.Contains(system, factContent) {
				t.Fatalf("non-streaming /chat leaked (%s, %s)'s fact to (%s, %s); system prompt:\n%s",
					"acme", "alice", tc.tenant, tc.user, system)
			}

			postStream(t, handler, tc.tenant, tc.user, "s-iso-stream", paraphraseQuery)
			if system := client.lastSystem(); strings.Contains(system, factContent) {
				t.Fatalf("streaming /chat/stream leaked (%s, %s)'s fact to (%s, %s); system prompt:\n%s",
					"acme", "alice", tc.tenant, tc.user, system)
			}
		})
	}
}
