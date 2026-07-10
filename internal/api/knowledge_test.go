package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// newKnowledgeRouter builds a router with only the knowledge-ingestion
// endpoint registered, backed by a fresh in-memory semantic store + the
// deterministic HashEmbedder — zero external dependency, mirroring
// newChatOnlyRouter's pattern. Returns the router and the store, so tests
// can also assert directly against the semantic tier.
func newKnowledgeRouter(t *testing.T, opts semantic.ChunkOptions) (http.Handler, semantic.Store, semantic.Embedder) {
	t.Helper()
	emb := semantic.NewHashEmbedder(0)
	store := semantic.NewInMemoryStore(emb.Dimension())
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("semantic store close: %v", err)
		}
	})
	router := chi.NewRouter()
	humaAPI := humachi.New(router, huma.DefaultConfig("Aisotropy-test", "0.1.0"))
	RegisterKnowledge(humaAPI, store, emb, opts)
	return router, store, emb
}

// postKnowledge issues a POST against /v1/tenant/{tenant}/user/{user}/knowledge
// with the given documents and returns the raw response.
func postKnowledge(t *testing.T, handler http.Handler, tenant, user string, docs []KnowledgeDocument) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(KnowledgeRequest{Documents: docs})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	path := "/v1/tenant/" + tenant + "/user/" + user + "/knowledge"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// TestRegisterKnowledge_IngestsAndReturnsChunkCount is the HTTP-surface half
// of ADR-0018's acceptance criterion #7: POST .../knowledge ingests and
// returns the chunk count.
func TestRegisterKnowledge_IngestsAndReturnsChunkCount(t *testing.T) {
	handler, _, _ := newKnowledgeRouter(t, semantic.DefaultChunkOptions())

	rec := postKnowledge(t, handler, "acme", "alice", []KnowledgeDocument{
		{ID: "handbook", Content: "the aisotropy onboarding handbook", Source: "handbook.md"},
		{ID: "faq", Content: "frequently asked questions about billing"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var out KnowledgeOutput
	if err := json.Unmarshal(rec.Body.Bytes(), &out.Body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.Body.ChunksIngested != 2 {
		t.Fatalf("chunks_ingested = %d, want 2 (two short docs = one chunk each)", out.Body.ChunksIngested)
	}
}

// TestRegisterKnowledge_IngestedChunkIsRetrievable proves an ingested
// document is retrievable via a direct KindDocument search against the same
// store the handler wrote to — the HTTP surface's contribution to
// acceptance criterion #7 ("ingested docs are retrievable via KindDocument
// search").
func TestRegisterKnowledge_IngestedChunkIsRetrievable(t *testing.T) {
	handler, store, emb := newKnowledgeRouter(t, semantic.DefaultChunkOptions())

	rec := postKnowledge(t, handler, "acme", "alice", []KnowledgeDocument{
		{ID: "runbook", Content: "incident response runbook for the payments service", Source: "runbook.md"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	sc, err := semantic.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	vecs, err := emb.Embed(context.Background(), []string{"incident response runbook payments"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	got, err := store.Search(context.Background(), sc, vecs[0], 10, semantic.KindDocument)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d KindDocument results, want 1", len(got))
	}
	if got[0].Source != "runbook.md" {
		t.Fatalf("Source = %q, want runbook.md", got[0].Source)
	}
}

// TestRegisterKnowledge_IsIdempotent verifies re-ingesting the same document
// id via HTTP replaces its chunks rather than duplicating them.
func TestRegisterKnowledge_IsIdempotent(t *testing.T) {
	handler, store, emb := newKnowledgeRouter(t, semantic.DefaultChunkOptions())

	for _, content := range []string{"first version of the policy", "second, revised version of the policy"} {
		rec := postKnowledge(t, handler, "acme", "alice", []KnowledgeDocument{{ID: "policy", Content: content}})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}

	sc, err := semantic.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	vecs, err := emb.Embed(context.Background(), []string{"version policy"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	got, err := store.Search(context.Background(), sc, vecs[0], 10, semantic.KindDocument)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d points after re-ingesting policy, want 1 (idempotent replace)", len(got))
	}
	if got[0].Content != "second, revised version of the policy" {
		t.Fatalf("content = %q, want the revised version", got[0].Content)
	}
}

// TestRegisterKnowledge_RejectsInvalidIdentity verifies the multi-tenancy
// gate: a tenant/user id outside the path's charset pattern is rejected
// before the handler (and any chunk/embed work) runs.
func TestRegisterKnowledge_RejectsInvalidIdentity(t *testing.T) {
	handler, _, _ := newKnowledgeRouter(t, semantic.DefaultChunkOptions())

	rec := postKnowledge(t, handler, "acme!not-allowed", "alice", []KnowledgeDocument{{ID: "d", Content: "content"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s, want 422 for an invalid tenant id", rec.Code, rec.Body.String())
	}
}
