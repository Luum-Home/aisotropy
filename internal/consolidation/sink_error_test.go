package consolidation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// --- error-injecting doubles ---------------------------------------------

type errEmbedder struct {
	err   error
	empty bool
}

func (e errEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	if e.err != nil {
		return nil, e.err
	}
	if e.empty {
		return [][]float32{}, nil
	}
	return [][]float32{make([]float32, 8)}, nil
}
func (e errEmbedder) Dimension() int { return 8 }

type errSemantic struct {
	upsertErr    error
	upsertCalled bool
}

func (e *errSemantic) Upsert(context.Context, semantic.Scope, []semantic.Point) error {
	e.upsertCalled = true
	return e.upsertErr
}
func (e *errSemantic) Search(context.Context, semantic.Scope, []float32, int, ...semantic.Kind) ([]semantic.ScoredPoint, error) {
	return nil, nil
}
func (e *errSemantic) Delete(context.Context, semantic.Scope, []string) error { return nil }
func (e *errSemantic) DeleteBySource(context.Context, semantic.Scope, semantic.Kind, string) (int, error) {
	return 0, nil
}
func (e *errSemantic) PruneTurns(context.Context, semantic.Scope, time.Duration, int) (int, error) {
	return 0, nil
}
func (e *errSemantic) Close() error { return nil }

// errFacts errors on Save; every other method is an inert success so the
// engram facade reaches Save.
type errFacts struct{ saveErr error }

func (f errFacts) Save(context.Context, facts.Scope, facts.Fact) error { return f.saveErr }
func (f errFacts) Search(context.Context, facts.Scope, string, int) ([]facts.ScoredFact, error) {
	return nil, nil
}
func (f errFacts) Supersede(context.Context, facts.Scope, string, facts.Fact) error { return nil }
func (f errFacts) ExportAll(context.Context) ([]facts.Fact, error)                  { return nil, nil }
func (f errFacts) Delete(context.Context, facts.Scope, []string) error              { return nil }
func (f errFacts) Close() error                                                     { return nil }

// staticExtractor always yields one fact, so consolidateFacts reaches Save.
type staticExtractor struct{}

func (staticExtractor) Extract(sc facts.Scope, _ facts.Turn) []facts.Fact {
	return []facts.Fact{{ID: "x1", TenantID: sc.Tenant(), UserID: sc.User(), Content: "a distilled fact", Importance: 0.9}}
}

// --- tests ----------------------------------------------------------------

func TestConsolidateSemanticEmbedError(t *testing.T) {
	want := errors.New("embed model down")
	// Facts/Extractor nil so only the semantic path runs.
	sink := Sink{Semantic: &errSemantic{}, Embedder: errEmbedder{err: want}}
	e := memory.OutboxEvent{ID: "m1", Tenant: "acme", User: "alice", Content: "hi"}
	if err := sink.Consolidate(context.Background(), e); err == nil || !errors.Is(err, want) {
		t.Fatalf("Consolidate error = %v, want embed wrap of %v", err, want)
	}
}

func TestConsolidateSemanticEmptyVectorsIsNoOp(t *testing.T) {
	sem := &errSemantic{}
	sink := Sink{Semantic: sem, Embedder: errEmbedder{empty: true}}
	e := memory.OutboxEvent{ID: "m1", Tenant: "acme", User: "alice", Content: "hi"}
	if err := sink.Consolidate(context.Background(), e); err != nil {
		t.Fatalf("Consolidate = %v, want nil (empty vectors short-circuit)", err)
	}
	if sem.upsertCalled {
		t.Fatal("upsert ran despite the embedder returning no vectors")
	}
}

func TestConsolidateSemanticUpsertError(t *testing.T) {
	want := errors.New("vector store rejected upsert")
	sem := &errSemantic{upsertErr: want}
	sink := Sink{Semantic: sem, Embedder: errEmbedder{}}
	e := memory.OutboxEvent{ID: "m1", Tenant: "acme", User: "alice", Content: "hi"}
	if err := sink.Consolidate(context.Background(), e); err == nil || !errors.Is(err, want) {
		t.Fatalf("Consolidate error = %v, want upsert wrap of %v", err, want)
	}
}

func TestConsolidateSemanticInvalidScope(t *testing.T) {
	// A reserved character in the tenant makes semantic.ScopeFromContext fail,
	// after embedding succeeds. Facts path disabled so the semantic error is
	// the one surfaced.
	sink := Sink{Semantic: &errSemantic{}, Embedder: errEmbedder{}}
	e := memory.OutboxEvent{ID: "m1", Tenant: "bad:tenant", User: "alice", Content: "hi"}
	if err := sink.Consolidate(context.Background(), e); err == nil {
		t.Fatal("expected invalid-scope error for a reserved-character tenant")
	}
}

func TestConsolidateFactsInvalidScope(t *testing.T) {
	// Facts path only. Reserved-character tenant fails facts.ScopeFromContext.
	sink := Sink{Facts: errFacts{}, Extractor: staticExtractor{}}
	e := memory.OutboxEvent{ID: "m1", Tenant: "bad:tenant", User: "alice", Content: "hi"}
	if err := sink.Consolidate(context.Background(), e); err == nil {
		t.Fatal("expected invalid facts-scope error for a reserved-character tenant")
	}
}

func TestConsolidateFactsSaveError(t *testing.T) {
	want := errors.New("facts write failed")
	sink := Sink{Facts: errFacts{saveErr: want}, Extractor: staticExtractor{}}
	e := memory.OutboxEvent{ID: "m1", Tenant: "acme", User: "alice", Content: "hi"}
	if err := sink.Consolidate(context.Background(), e); err == nil || !errors.Is(err, want) {
		t.Fatalf("Consolidate error = %v, want save wrap of %v", err, want)
	}
}
