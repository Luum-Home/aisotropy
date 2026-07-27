package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// The tier latencies below stand in for the NETWORKED deployment the parallel
// hydration exists for (rqlite over HTTP for facts, Qdrant over gRPC for
// vectors) — the bare-binary in-process defaults are sub-millisecond and would
// make any timing assertion pure noise. searchSemanticContext issues TWO scoped
// scans (the user's own plus the tenant-shared Document bucket), so the semantic
// TIER costs 2*latentSemanticSearch; picking that to equal latentFactsSearch
// makes the two slow tiers deliberately balanced:
//
//	sequential = facts + semantic          = 120ms + 120ms = 240ms
//	parallel   = max(facts, semantic)      =         120ms
//
// The assertion threshold sits between them with ~60ms of headroom on both
// sides, so the test discriminates the two implementations without being a
// flaky wall-clock benchmark. It FAILS on the pre-ADR-0076 sequential code.
const (
	latentFactsSearch      = 120 * time.Millisecond
	latentSemanticSearch   = 60 * time.Millisecond
	hydrationParallelLimit = 180 * time.Millisecond
)

// latentFactsStore is a facts.Store whose Search sleeps for latentFactsSearch,
// standing in for a networked rqlite round trip. It returns one fact so the
// assembled prompt proves the tier actually contributed.
type latentFactsStore struct{}

func (latentFactsStore) Save(context.Context, facts.Scope, facts.Fact) error { return nil }
func (latentFactsStore) Search(ctx context.Context, _ facts.Scope, _ string, _ int) ([]facts.ScoredFact, error) {
	select {
	case <-time.After(latentFactsSearch):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []facts.ScoredFact{{Fact: facts.Fact{ID: "f1", Content: "the user flies seat 14C"}, Score: 1}}, nil
}
func (latentFactsStore) Supersede(context.Context, facts.Scope, string, facts.Fact) error { return nil }
func (latentFactsStore) ExportAll(context.Context) ([]facts.Fact, error)                  { return nil, nil }
func (latentFactsStore) Delete(context.Context, facts.Scope, []string) error              { return nil }
func (latentFactsStore) Close() error                                                     { return nil }

// latentSemanticStore is a semantic.Store whose Search sleeps for
// latentSemanticSearch, standing in for a networked Qdrant round trip.
type latentSemanticStore struct{}

func (latentSemanticStore) Upsert(context.Context, semantic.Scope, []semantic.Point) error {
	return nil
}
func (latentSemanticStore) Search(ctx context.Context, _ semantic.Scope, _ []float32, _ int, _ ...semantic.Kind) ([]semantic.ScoredPoint, error) {
	select {
	case <-time.After(latentSemanticSearch):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return nil, nil
}
func (latentSemanticStore) Delete(context.Context, semantic.Scope, []string) error { return nil }
func (latentSemanticStore) DeleteBySource(context.Context, semantic.Scope, semantic.Kind, string) (int, error) {
	return 0, nil
}
func (latentSemanticStore) ListBySource(context.Context, semantic.Scope, semantic.Kind, string) ([]semantic.Point, error) {
	return nil, nil
}
func (latentSemanticStore) PruneTurns(context.Context, semantic.Scope, time.Duration, int) (int, error) {
	return 0, nil
}
func (latentSemanticStore) Close() error { return nil }

// TestHydrateSystemPrompt_TiersRunConcurrently is the ADR-0076 regression guard:
// the independent memory tiers must be read CONCURRENTLY, so hydration costs
// max(tier) rather than sum(tier). It fails on the sequential implementation
// (~240ms against a 180ms budget) and passes on the parallel one (~120ms).
//
// It also asserts the tiers' output still lands in the prompt — a "fast" run
// that silently dropped a tier would be a regression, not a win.
func TestHydrateSystemPrompt_TiersRunConcurrently(t *testing.T) {
	scope, err := facts.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
	wm := newTestWorkingMemory(t)

	start := time.Now()
	sys, err := hydrateSystemPrompt(context.Background(), wm, latentFactsStore{}, latentSemanticStore{},
		semantic.NewHashEmbedder(64), nil, nil, key, scope, "p1", "which seat am I in?")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("hydrateSystemPrompt: %v", err)
	}

	if elapsed > hydrationParallelLimit {
		t.Errorf("hydration took %v, want < %v — the memory tiers are being read sequentially "+
			"(sequential cost is facts %v + semantic 2x%v); ADR-0076 requires them concurrent",
			elapsed, hydrationParallelLimit, latentFactsSearch, latentSemanticSearch)
	}

	if !strings.Contains(sys, "seat 14C") {
		t.Errorf("the facts tier's content is missing from the hydrated prompt; got:\n%s", sys)
	}
}

// BenchmarkHydrateSystemPrompt_InProcess measures the OTHER deployment shape —
// the bare binary's in-process defaults (embedded BadgerDB, in-process cosine
// semantic store) — where every tier is sub-millisecond. It exists to keep the
// ADR-0076 claim honest: run it against the sequential and the parallel
// implementation and the difference is goroutine scheduling noise, NOT a win.
// The win is real only when the tiers are networked (rqlite/Qdrant), which is
// what TestHydrateSystemPrompt_TiersRunConcurrently models.
func BenchmarkHydrateSystemPrompt_InProcess(b *testing.B) {
	scope, err := facts.NewScope("acme", "alice")
	if err != nil {
		b.Fatalf("NewScope: %v", err)
	}
	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}

	wm, err := memory.OpenInMemory(0, memory.NoopSummarizer{})
	if err != nil {
		b.Fatalf("OpenInMemory: %v", err)
	}
	b.Cleanup(func() { _ = wm.Close() })
	sem := semantic.NewInMemoryStore(64)
	b.Cleanup(func() { _ = sem.Close() })

	ctx := context.Background()
	b.ResetTimer()
	for range b.N {
		if _, err := hydrateSystemPrompt(ctx, wm, nil, sem, semantic.NewHashEmbedder(64), nil, nil,
			key, scope, "p1", "which seat am I in?"); err != nil {
			b.Fatalf("hydrateSystemPrompt: %v", err)
		}
	}
}

// TestHydrateSystemPrompt_WorkingMemoryErrorStaysFatal pins the half of the
// degradation contract the parallel join must not soften: a working-memory
// failure is FATAL (unlike facts/semantic, which degrade), and the error keeps
// its exact wrapped prefix so callers' 500 mapping is unchanged. A closed store
// is the cheapest real failure — every read against it errors.
func TestHydrateSystemPrompt_WorkingMemoryErrorStaysFatal(t *testing.T) {
	scope, err := facts.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}

	wm, err := memory.OpenInMemory(0, memory.NoopSummarizer{})
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	if cerr := wm.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}

	// A simultaneously-failing facts store proves precedence: the fatal
	// working-memory error must win over the tier that only degrades.
	_, err = hydrateSystemPrompt(context.Background(), wm, errFactsStore{}, nil, nil, nil, nil,
		key, scope, "p1", "some prompt")
	if err == nil {
		t.Fatal("a working-memory failure must stay FATAL, got nil error")
	}
	if !strings.HasPrefix(err.Error(), "read working-memory summary: ") {
		t.Errorf("error lost its wrapped prefix: %q", err.Error())
	}
}
