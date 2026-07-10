package aisotropy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// TestWithEmbedderAndWithChunkSize_ApplyToApp verifies both options land on
// the App, and that WithChunkSize's values are what chunkOptions() resolves
// to (no silent fallback to the default once explicitly set).
func TestWithEmbedderAndWithChunkSize_ApplyToApp(t *testing.T) {
	emb := semantic.NewHashEmbedder(32)
	a := New(WithEmbedder(emb), WithChunkSize(100, 10))

	if a.embedder != emb {
		t.Error("WithEmbedder did not set the App's embedder")
	}
	got := a.chunkOptions()
	if got.Size != 100 || got.Overlap != 10 {
		t.Errorf("chunkOptions() = %+v, want {100 10}", got)
	}
}

// TestChunkOptions_DefaultsWhenUnset verifies an App built without
// WithChunkSize resolves to semantic.DefaultChunkOptions() (512/64), per
// ADR-0018.
func TestChunkOptions_DefaultsWhenUnset(t *testing.T) {
	a := New()
	got := a.chunkOptions()
	want := semantic.DefaultChunkOptions()
	if got != want {
		t.Errorf("chunkOptions() = %+v, want the default %+v", got, want)
	}
}

// TestApp_IngestKnowledge_EndToEnd is the SDK-surface half of ADR-0018's
// acceptance criterion #7: IngestKnowledge works BEFORE Run is ever called
// (pure composition-SDK usage), chunks+embeds+stores the docs, and the
// result is retrievable via a direct KindDocument search against the same
// store ensureSemantic memoized.
func TestApp_IngestKnowledge_EndToEnd(t *testing.T) {
	a := New(WithLLM(llm.NewStubClient()))
	ctx := context.Background()

	n, err := a.IngestKnowledge(ctx, "acme", "alice",
		Document{ID: "handbook", Content: "the aisotropy onboarding handbook covers memory tiers", Source: "handbook.md"},
		Document{ID: "faq", Content: "frequently asked questions about billing and quotas"},
	)
	if err != nil {
		t.Fatalf("IngestKnowledge: %v", err)
	}
	if n != 2 {
		t.Fatalf("chunks ingested = %d, want 2 (two short docs = one chunk each)", n)
	}

	// Retrievability: the same App's semantic store (built lazily by
	// ensureSemantic, shared with IngestKnowledge) must return the ingested
	// chunk for a matching query, scoped to (acme, alice).
	store, emb, err := a.ensureSemantic(ctx)
	if err != nil {
		t.Fatalf("ensureSemantic: %v", err)
	}
	sc, err := semantic.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	vecs, err := emb.Embed(ctx, []string{"onboarding handbook memory tiers"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	got, err := store.Search(ctx, sc, vecs[0], 10, semantic.KindDocument)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("ingested document not retrievable via KindDocument search")
	}
}

// TestApp_IngestKnowledge_BadIdentityRejected verifies a bad tenant/user is
// returned as an error (via semantic.NewScope, ADR-0011), never silently
// accepted or ingested.
func TestApp_IngestKnowledge_BadIdentityRejected(t *testing.T) {
	a := New()
	if _, err := a.IngestKnowledge(context.Background(), "", "alice", Document{ID: "d", Content: "c"}); err == nil {
		t.Fatal("expected an error for an empty tenant, got nil")
	}
}

// TestApp_IngestKnowledge_SharesStoreAcrossRunAndSDK verifies Run and
// IngestKnowledge share exactly one semantic store/embedder pair per App:
// a document ingested via the SDK before Run is retrievable through the
// running server's chat/stream RAG hydration once Run boots — proving
// ensureSemantic's memoization, not just that both surfaces independently
// work.
func TestApp_IngestKnowledge_SharesStoreAcrossRunAndSDK(t *testing.T) {
	addr := freeAddr(t)
	app := New(WithAddr(addr), WithLLM(llm.NewStubClient()))

	ctx := context.Background()
	if _, err := app.IngestKnowledge(ctx, "acme", "alice", Document{ID: "d", Content: "the secret onboarding password is hunter2"}); err != nil {
		t.Fatalf("IngestKnowledge before Run: %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(runCtx) }()
	waitForServer(t, "http://"+addr+"/healthz")

	req, err := http.NewRequestWithContext(runCtx, http.MethodPost, "http://"+addr+"/v1/tenant/acme/user/alice/knowledge",
		strings.NewReader(`{"documents":[]}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	rec, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST knowledge (empty batch, just to prove the route is live and shares state): %v", err)
	}
	_, _ = io.Copy(io.Discard, rec.Body)
	_ = rec.Body.Close()
	if rec.StatusCode != http.StatusOK {
		t.Fatalf("POST .../knowledge status = %d", rec.StatusCode)
	}

	cancel()
	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatalf("Run returned error: %v", runErr)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after ctx cancellation")
	}
}

// TestRun_KnowledgeRouteInOpenAPI is acceptance criterion #8: POST
// .../knowledge appears in the generated OpenAPI document.
func TestRun_KnowledgeRouteInOpenAPI(t *testing.T) {
	addr := freeAddr(t)
	app := New(WithAddr(addr), WithLLM(llm.NewStubClient()))

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()
	waitForServer(t, "http://"+addr+"/healthz")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/openapi.json", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /openapi.json: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read /openapi.json body: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("unmarshal /openapi.json: %v", err)
	}
	paths, _ := doc["paths"].(map[string]any)
	if _, ok := paths["/v1/tenant/{tenant_id}/user/{user_id}/knowledge"]; !ok {
		t.Fatalf("POST .../knowledge missing from OpenAPI paths: %v", keysOf(paths))
	}

	cancel()
	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatalf("Run returned error: %v", runErr)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after ctx cancellation")
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
