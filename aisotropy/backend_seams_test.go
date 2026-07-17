package aisotropy

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/plugintest"
)

// TestWithVectorBackend_UsedByIngestKnowledge proves the Tier-2 sealed-driver
// seam is APPLIED, not merely stored: an App built with a consumer VectorBackend
// routes IngestKnowledge through the sealed decorator into that backend (never
// the default in-memory store). The reference backend's RawVectorInspector shows
// exactly one opaque namespace populated after ingest — the sealed decorator
// derived it from the (tenant,user) scope, the community backend only ever saw
// the hash.
func TestWithVectorBackend_UsedByIngestKnowledge(t *testing.T) {
	backend := plugintest.NewRefVectorBackend()
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9), WithVectorBackend(backend))
	defer closeApp(t, app)()

	if app.ext.vectorBackend == nil {
		t.Fatal("WithVectorBackend did not store the backend on the App")
	}

	n, err := app.IngestKnowledge(context.Background(), "acme", "alice",
		Document{ID: "handbook", Content: "the onboarding handbook covers the memory tiers and quotas"})
	if err != nil {
		t.Fatalf("IngestKnowledge: %v", err)
	}
	if n == 0 {
		t.Fatal("IngestKnowledge wrote 0 chunks")
	}

	ns := backend.Namespaces()
	if len(ns) != 1 {
		t.Fatalf("consumer vector backend has %d namespaces, want 1 (ingest must route through the sealed decorator into the backend)", len(ns))
	}
	// The namespace is the opaque sealed hash, never the raw tenant/user.
	if ns[0] == "acme" || ns[0] == "acme\x00alice" {
		t.Errorf("backend saw a raw identity namespace %q; the seal must hand it an opaque hash", ns[0])
	}
}

// countingDocBackend wraps the reference facts backend and counts Search calls,
// so a test can prove the consumer facts backend is on the live hydration path.
type countingDocBackend struct {
	*plugintest.RefDocBackend
	searches atomic.Int32
}

func (c *countingDocBackend) Search(ctx context.Context, ns, query string, k int) ([]facts.BackendDocHit, error) {
	c.searches.Add(1)
	return c.RefDocBackend.Search(ctx, ns, query, k)
}

// TestWithFactsBackend_UsedByTurnHydration proves WithFactsBackend is APPLIED:
// the runtime seals the consumer DocBackend into the facts tier and the live
// system-prompt hydration searches it on every Turn. Asserting the backend's
// Search fired proves the seam is on the real request path, not merely stored on
// the App.
func TestWithFactsBackend_UsedByTurnHydration(t *testing.T) {
	backend := &countingDocBackend{RefDocBackend: plugintest.NewRefDocBackend()}
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9), WithFactsBackend(backend))
	defer closeApp(t, app)()

	if app.ext.factsBackend == nil {
		t.Fatal("WithFactsBackend did not store the backend on the App")
	}

	if _, err := app.Turn(context.Background(), ChatInput{
		TenantID: "acme", UserID: "alice", SessionID: "s1", Prompt: "what did we decide?",
	}); err != nil {
		t.Fatalf("Turn: %v", err)
	}

	if backend.searches.Load() == 0 {
		t.Fatal("consumer facts backend Search was never invoked; WithFactsBackend must be sealed into the live hydration path")
	}
}
