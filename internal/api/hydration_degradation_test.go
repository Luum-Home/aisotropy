package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// errEmbedder fails every Embed, exercising searchSemanticContext's embed-error
// degradation (returns no semantic context rather than failing the request).
type errEmbedder struct{ dim int }

func (e errEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("embed backend down")
}
func (e errEmbedder) Dimension() int { return e.dim }

// errSemStore fails every Search, exercising searchSemanticContext's
// search-error degradation on both the user and tenant-shared scans.
type errSemStore struct{}

func (errSemStore) Upsert(context.Context, semantic.Scope, []semantic.Point) error { return nil }
func (errSemStore) Search(context.Context, semantic.Scope, []float32, int, ...semantic.Kind) ([]semantic.ScoredPoint, error) {
	return nil, errors.New("vector store unavailable")
}
func (errSemStore) Delete(context.Context, semantic.Scope, []string) error { return nil }
func (errSemStore) DeleteBySource(context.Context, semantic.Scope, semantic.Kind, string) (int, error) {
	return 0, nil
}

func (errSemStore) ListBySource(context.Context, semantic.Scope, semantic.Kind, string) ([]semantic.Point, error) {
	return nil, nil
}
func (errSemStore) PruneTurns(context.Context, semantic.Scope, time.Duration, int) (int, error) {
	return 0, nil
}
func (errSemStore) Close() error { return nil }

// errFactsStore fails every Search, exercising hydrateSystemPrompt's non-fatal
// facts degradation (enrichment, not a hard dependency).
type errFactsStore struct{}

func (errFactsStore) Save(context.Context, facts.Scope, facts.Fact) error { return nil }
func (errFactsStore) Search(context.Context, facts.Scope, string, int) ([]facts.ScoredFact, error) {
	return nil, errors.New("facts store unavailable")
}
func (errFactsStore) Supersede(context.Context, facts.Scope, string, facts.Fact) error { return nil }
func (errFactsStore) ExportAll(context.Context) ([]facts.Fact, error)                  { return nil, nil }
func (errFactsStore) Delete(context.Context, facts.Scope, []string) error              { return nil }
func (errFactsStore) Close() error                                                     { return nil }

// TestSearchSemanticContext_EmbedErrorDegrades: an embed failure yields no
// semantic context (nil), never an error — semantic RAG is enrichment.
func TestSearchSemanticContext_EmbedErrorDegrades(t *testing.T) {
	scope, err := facts.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	sem := semantic.NewInMemoryStore(64)
	defer func() { _ = sem.Close() }()

	got := searchSemanticContext(context.Background(), sem, errEmbedder{dim: 64}, nil, scope, "query")
	if got != nil {
		t.Errorf("embed error must degrade to nil semantic context, got %v", got)
	}
}

// TestSearchSemanticContext_SearchErrorDegrades: when both scoped searches fail,
// the function returns no context (nil) rather than failing — each scan degrades
// independently.
func TestSearchSemanticContext_SearchErrorDegrades(t *testing.T) {
	scope, err := facts.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	got := searchSemanticContext(context.Background(), errSemStore{}, semantic.NewHashEmbedder(64), nil, scope, "query")
	if len(got) != 0 {
		t.Errorf("search failure must degrade to empty semantic context, got %v", got)
	}
}

// TestHydrateSystemPrompt_FactsErrorDegrades: a facts-search failure logs and
// proceeds with no facts (the request still succeeds on the base persona).
func TestHydrateSystemPrompt_FactsErrorDegrades(t *testing.T) {
	scope, err := facts.NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	key := memory.Key{Tenant: "acme", User: "alice", Session: "s1"}
	sys, err := hydrateSystemPrompt(context.Background(), nil, errFactsStore{}, nil, nil, nil, nil,
		key, scope, "p1", "some prompt")
	if err != nil {
		t.Fatalf("a facts-search failure must NOT fail hydration: %v", err)
	}
	if sys == "" {
		t.Error("expected the base persona prompt despite the facts failure")
	}
}
