package consolidation

import (
	"context"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

func newSink(t *testing.T) (Sink, facts.Store, semantic.Store, semantic.Embedder) {
	t.Helper()
	fs, err := facts.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open facts: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	emb := semantic.NewHashEmbedder(0)
	ss := semantic.NewInMemoryStore(emb.Dimension())
	return Sink{Facts: fs, Extractor: facts.HeuristicExtractor{}, Semantic: ss, Embedder: emb}, fs, ss, emb
}

func TestSinkConsolidatesToBothTiers(t *testing.T) {
	sink, fs, ss, emb := newSink(t)
	ctx := context.Background()
	e := memory.OutboxEvent{ID: "m1", Tenant: "acme", User: "alice",
		Content: "remember that the billing database is postgres"}

	if err := sink.Consolidate(ctx, e); err != nil {
		t.Fatalf("Consolidate: %v", err)
	}

	// Facts tier: the extractor produced a fact, searchable by FTS5.
	fres, err := fs.Search(ctx, facts.Scope{TenantID: "acme", UserID: "alice"}, "postgres database", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(fres) == 0 {
		t.Fatal("fact not consolidated into the facts tier")
	}

	// Semantic tier: a vector for the turn is retrievable and scoped.
	sscope := semantic.Scope{TenantID: "acme", UserID: "alice"}
	q, _ := emb.Embed(ctx, []string{"postgres billing database"})
	sres, err := ss.Search(ctx, sscope, q[0], 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(sres) != 1 {
		t.Fatalf("vector count = %d, want 1", len(sres))
	}

	// Idempotency: re-consolidating the same event (at-least-once redelivery)
	// must not duplicate the vector (deterministic UUIDv5 id).
	if err := sink.Consolidate(ctx, e); err != nil {
		t.Fatal(err)
	}
	sres2, _ := ss.Search(ctx, sscope, q[0], 5)
	if len(sres2) != 1 {
		t.Fatalf("vector count after reprocess = %d, want 1 (idempotent)", len(sres2))
	}
}

func TestSinkTiersAreOptional(t *testing.T) {
	ctx := context.Background()
	e := memory.OutboxEvent{ID: "m1", Tenant: "acme", User: "alice", Content: "I prefer dark mode"}

	// Facts-only sink: no semantic tier configured, must not error.
	fs, err := facts.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	factsOnly := Sink{Facts: fs, Extractor: facts.HeuristicExtractor{}}
	if err := factsOnly.Consolidate(ctx, e); err != nil {
		t.Fatalf("facts-only Consolidate: %v", err)
	}
	got, _ := fs.Search(ctx, facts.Scope{TenantID: "acme", UserID: "alice"}, "dark mode", 5)
	if len(got) == 0 {
		t.Fatal("facts-only sink did not persist the fact")
	}

	// Empty sink: nothing configured, still a no-op success.
	if err := (Sink{}).Consolidate(ctx, e); err != nil {
		t.Fatalf("empty Consolidate: %v", err)
	}
}
