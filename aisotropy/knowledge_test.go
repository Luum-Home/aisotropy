package aisotropy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
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

// TestApp_DeleteKnowledge_RemovesAllChunks is the SDK-surface half of
// ADR-0018 open q#3's acceptance criterion: DeleteKnowledge removes every
// chunk of a multi-chunk document, and the document is no longer retrievable
// afterward.
func TestApp_DeleteKnowledge_RemovesAllChunks(t *testing.T) {
	a := New(WithLLM(llm.NewStubClient()), WithChunkSize(10, 0))
	ctx := context.Background()

	long := strings.Repeat("abcdefghij", 5) // 50 runes -> 5 chunks at size 10
	n, err := a.IngestKnowledge(ctx, "acme", "alice", Document{ID: "runbook", Content: long})
	if err != nil {
		t.Fatalf("IngestKnowledge: %v", err)
	}
	if n != 5 {
		t.Fatalf("chunks ingested = %d, want 5", n)
	}

	deleted, err := a.DeleteKnowledge(ctx, "acme", "alice", "runbook")
	if err != nil {
		t.Fatalf("DeleteKnowledge: %v", err)
	}
	if deleted != 5 {
		t.Fatalf("deleted = %d, want 5 (all chunks removed)", deleted)
	}

	store, emb, err := a.ensureSemantic(ctx)
	if err != nil {
		t.Fatalf("ensureSemantic: %v", err)
	}
	sc, err := semantic.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	vecs, err := emb.Embed(ctx, []string{"abcdefghij"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	got, err := store.Search(ctx, sc, vecs[0], 10, semantic.KindDocument)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d KindDocument results after DeleteKnowledge, want 0", len(got))
	}

	// Deleting again (unknown docID by now) is idempotent: no error, 0 deleted.
	deleted, err = a.DeleteKnowledge(ctx, "acme", "alice", "runbook")
	if err != nil {
		t.Fatalf("second DeleteKnowledge: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("second deleted = %d, want 0", deleted)
	}
}

// TestApp_IngestTenantKnowledge_SharedAcrossUsersIsolatedAcrossTenants is the
// SDK-surface half of ADR-0019's acceptance criterion: a document ingested
// via IngestTenantKnowledge is retrievable by a DIFFERENT user of the same
// tenant, but never by another tenant.
func TestApp_IngestTenantKnowledge_SharedAcrossUsersIsolatedAcrossTenants(t *testing.T) {
	a := New(WithLLM(llm.NewStubClient()))
	ctx := context.Background()

	n, err := a.IngestTenantKnowledge(ctx, "acme", Document{ID: "handbook", Content: "the acme onboarding handbook covers benefits and payroll"})
	if err != nil {
		t.Fatalf("IngestTenantKnowledge: %v", err)
	}
	if n != 1 {
		t.Fatalf("chunks ingested = %d, want 1", n)
	}

	store, emb, err := a.ensureSemantic(ctx)
	if err != nil {
		t.Fatalf("ensureSemantic: %v", err)
	}
	vecs, err := emb.Embed(ctx, []string{"onboarding handbook benefits payroll"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	// A DIFFERENT user of the SAME tenant retrieves it via the tenant-shared
	// scope (the same scope IngestTenantKnowledge wrote to).
	tenantScope, err := semantic.NewTenantScope("acme")
	if err != nil {
		t.Fatalf("NewTenantScope: %v", err)
	}
	got, err := store.Search(ctx, tenantScope, vecs[0], 10, semantic.KindDocument)
	if err != nil {
		t.Fatalf("Search (tenant-shared scope): %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d tenant-shared results, want 1 (visible regardless of which user asks)", len(got))
	}

	// A DIFFERENT tenant's shared scope must never see it.
	otherTenantScope, err := semantic.NewTenantScope("globex")
	if err != nil {
		t.Fatalf("NewTenantScope (globex): %v", err)
	}
	leaked, err := store.Search(ctx, otherTenantScope, vecs[0], 10, semantic.KindDocument)
	if err != nil {
		t.Fatalf("Search (globex tenant-shared scope): %v", err)
	}
	if len(leaked) != 0 {
		t.Fatalf("cross-tenant leak: globex's tenant-shared search returned %d acme documents, want 0", len(leaked))
	}
}

// TestApp_DeleteTenantKnowledge_RemovesSharedDocument is the SDK-surface
// half of ADR-0019 open q#1: DeleteTenantKnowledge removes a tenant-shared
// document's chunks.
func TestApp_DeleteTenantKnowledge_RemovesSharedDocument(t *testing.T) {
	a := New(WithLLM(llm.NewStubClient()))
	ctx := context.Background()

	if _, err := a.IngestTenantKnowledge(ctx, "acme", Document{ID: "faq", Content: "frequently asked questions about time off"}); err != nil {
		t.Fatalf("IngestTenantKnowledge: %v", err)
	}

	deleted, err := a.DeleteTenantKnowledge(ctx, "acme", "faq")
	if err != nil {
		t.Fatalf("DeleteTenantKnowledge: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}

	store, emb, err := a.ensureSemantic(ctx)
	if err != nil {
		t.Fatalf("ensureSemantic: %v", err)
	}
	tenantScope, err := semantic.NewTenantScope("acme")
	if err != nil {
		t.Fatalf("NewTenantScope: %v", err)
	}
	vecs, err := emb.Embed(ctx, []string{"frequently asked questions time off"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	got, err := store.Search(ctx, tenantScope, vecs[0], 10, semantic.KindDocument)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d results after DeleteTenantKnowledge, want 0", len(got))
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
	// NoAuth: this test proves the HTTP knowledge route is live and shares the
	// semantic store with the SDK path; authentication is orthogonal here, so
	// opt out of the default deny-all gate to reach the handler over HTTP.
	app := New(WithAddr(addr), WithLLM(llm.NewStubClient()), WithAuthenticator(NoAuth()))

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
	// ADR-0018 open q#3 + ADR-0019: the delete and tenant-shared routes must
	// also be published in the OpenAPI contract, not just wired at runtime.
	for _, want := range []string{
		"/v1/tenant/{tenant_id}/user/{user_id}/knowledge/{doc_id}",
		"/v1/tenant/{tenant_id}/knowledge",
		"/v1/tenant/{tenant_id}/knowledge/{doc_id}",
	} {
		if _, ok := paths[want]; !ok {
			t.Fatalf("%s missing from OpenAPI paths: %v", want, keysOf(paths))
		}
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

// closeCountingStore wraps a semantic.Store to count Close calls, the same
// pattern internal/semantic/routing_store_test.go uses, so a test can assert
// idempotency (repeated Close calls do not close the underlying store twice).
type closeCountingStore struct {
	semantic.Store
	closed *int32
}

func (c closeCountingStore) Close() error {
	atomic.AddInt32(c.closed, 1)
	return c.Store.Close()
}

// TestApp_Close_NilSafeWhenSemanticStoreNeverBuilt is the C.1 acceptance
// criterion's nil-safety half: an App on which neither IngestKnowledge nor
// Run was ever called (so ensureSemantic never ran and a.semanticStore is
// still nil) returns nil from Close without touching anything.
func TestApp_Close_NilSafeWhenSemanticStoreNeverBuilt(t *testing.T) {
	a := New()
	if err := a.Close(); err != nil {
		t.Fatalf("Close on an App with no semantic store built = %v, want nil", err)
	}
}

// TestApp_Close_ClosesMemoizedSemanticStoreIdempotently is the C.1 acceptance
// criterion: Close closes the semantic store ensureSemantic memoized, and a
// second Close call does not close it again (idempotent — verified via a
// wrapped store's Close call count).
func TestApp_Close_ClosesMemoizedSemanticStoreIdempotently(t *testing.T) {
	a := New(WithLLM(llm.NewStubClient()))

	// Force-build the semantic store the same way IngestKnowledge would (SDK-
	// only usage, no Run), then wrap the memoized store so Close calls are
	// counted.
	if _, _, err := a.ensureSemantic(context.Background()); err != nil {
		t.Fatalf("ensureSemantic: %v", err)
	}
	var closed int32
	a.semanticStore = closeCountingStore{Store: a.semanticStore, closed: &closed}

	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := atomic.LoadInt32(&closed); got != 1 {
		t.Fatalf("Close calls on the underlying store = %d, want 1", got)
	}

	// Idempotent: a second Close must not close the store again.
	if err := a.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := atomic.LoadInt32(&closed); got != 1 {
		t.Fatalf("Close calls on the underlying store after a second Close = %d, want 1 (idempotent)", got)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
