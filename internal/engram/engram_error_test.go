package engram

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// fakeFacts is a facts.Store double with per-op error injection. Unset error
// fields behave as a silent success (the engram facade never inspects the
// returned data on those paths).
type fakeFacts struct {
	saveErr      error
	supersedeErr error
	deleteErr    error
}

func (f fakeFacts) Save(context.Context, facts.Scope, facts.Fact) error { return f.saveErr }
func (f fakeFacts) Search(context.Context, facts.Scope, string, int) ([]facts.ScoredFact, error) {
	return nil, nil
}
func (f fakeFacts) Supersede(context.Context, facts.Scope, string, facts.Fact) error {
	return f.supersedeErr
}
func (f fakeFacts) ExportAll(context.Context) ([]facts.Fact, error) { return nil, nil }
func (f fakeFacts) Delete(context.Context, facts.Scope, []string) error {
	return f.deleteErr
}
func (f fakeFacts) Close() error { return nil }

// fakeSemantic is a semantic.Store double with error injection on the two
// methods the engram facade calls (Delete, Upsert).
type fakeSemantic struct {
	deleteErr    error
	upsertErr    error
	deleteCalled bool
	upsertCalled bool
}

func (f *fakeSemantic) Upsert(context.Context, semantic.Scope, []semantic.Point) error {
	f.upsertCalled = true
	return f.upsertErr
}
func (f *fakeSemantic) Search(context.Context, semantic.Scope, []float32, int, ...semantic.Kind) ([]semantic.ScoredPoint, error) {
	return nil, nil
}
func (f *fakeSemantic) Delete(context.Context, semantic.Scope, []string) error {
	f.deleteCalled = true
	return f.deleteErr
}
func (f *fakeSemantic) DeleteBySource(context.Context, semantic.Scope, semantic.Kind, string) (int, error) {
	return 0, nil
}

func (f *fakeSemantic) ListBySource(context.Context, semantic.Scope, semantic.Kind, string) ([]semantic.Point, error) {
	return nil, nil
}
func (f *fakeSemantic) PruneTurns(context.Context, semantic.Scope, time.Duration, int) (int, error) {
	return 0, nil
}
func (f *fakeSemantic) Close() error { return nil }

// errEmbedder / emptyEmbedder exercise indexFact's embed error and
// zero-vector short-circuit.
type errEmbedder struct{ err error }

func (e errEmbedder) Embed(context.Context, []string) ([][]float32, error) { return nil, e.err }
func (e errEmbedder) Dimension() int                                       { return 8 }

type emptyEmbedder struct{}

func (emptyEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return [][]float32{}, nil
}
func (emptyEmbedder) Dimension() int { return 8 }

func errScope(t *testing.T) facts.Scope {
	t.Helper()
	sc, err := facts.NewScope("acme", "alice")
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func aFact() facts.Fact {
	return facts.Fact{ID: "f1", TenantID: "acme", UserID: "alice", Content: "prefers dark mode", Importance: 0.9}
}

func TestSaveFactStoreError(t *testing.T) {
	want := errors.New("disk full")
	s := Store{Facts: fakeFacts{saveErr: want}}
	if err := s.Save(context.Background(), errScope(t), aFact()); err == nil || !errors.Is(err, want) {
		t.Fatalf("Save error = %v, want wrap of %v", err, want)
	}
}

func TestSupersedeFactStoreError(t *testing.T) {
	want := errors.New("not found")
	s := Store{Facts: fakeFacts{supersedeErr: want}}
	if err := s.Supersede(context.Background(), errScope(t), "old", aFact()); err == nil || !errors.Is(err, want) {
		t.Fatalf("Supersede error = %v, want wrap of %v", err, want)
	}
}

func TestSupersedeSemanticDeleteError(t *testing.T) {
	want := errors.New("vector store down")
	sem := &fakeSemantic{deleteErr: want}
	s := Store{Facts: fakeFacts{}, Semantic: sem, Embedder: semantic.NewHashEmbedder(0)}
	if err := s.Supersede(context.Background(), errScope(t), "old", aFact()); err == nil || !errors.Is(err, want) {
		t.Fatalf("Supersede error = %v, want wrap of %v", err, want)
	}
	if !sem.deleteCalled {
		t.Fatal("Supersede did not attempt to delete the old vector")
	}
}

func TestDeleteFactStoreError(t *testing.T) {
	want := errors.New("delete failed")
	s := Store{Facts: fakeFacts{deleteErr: want}}
	if err := s.Delete(context.Background(), errScope(t), []string{"f1"}); err == nil || !errors.Is(err, want) {
		t.Fatalf("Delete error = %v, want wrap of %v", err, want)
	}
}

func TestDeleteSemanticError(t *testing.T) {
	want := errors.New("vector delete failed")
	sem := &fakeSemantic{deleteErr: want}
	s := Store{Facts: fakeFacts{}, Semantic: sem, Embedder: semantic.NewHashEmbedder(0)}
	if err := s.Delete(context.Background(), errScope(t), []string{"f1"}); err == nil || !errors.Is(err, want) {
		t.Fatalf("Delete error = %v, want wrap of %v", err, want)
	}
}

// TestDeleteEmptyIDsSkipsSemantic covers the len(ids)==0 short-circuit: with a
// Semantic store configured, an empty id slice must not touch the vector tier.
func TestDeleteEmptyIDsSkipsSemantic(t *testing.T) {
	sem := &fakeSemantic{}
	s := Store{Facts: fakeFacts{}, Semantic: sem, Embedder: semantic.NewHashEmbedder(0)}
	if err := s.Delete(context.Background(), errScope(t), nil); err != nil {
		t.Fatalf("Delete(nil ids) = %v, want nil", err)
	}
	if sem.deleteCalled {
		t.Fatal("Delete with empty ids must not call the semantic tier")
	}
}

func TestIndexFactEmbedError(t *testing.T) {
	want := errors.New("embed model unreachable")
	sem := &fakeSemantic{}
	s := Store{Facts: fakeFacts{}, Semantic: sem, Embedder: errEmbedder{err: want}}
	if err := s.Save(context.Background(), errScope(t), aFact()); err == nil || !errors.Is(err, want) {
		t.Fatalf("Save error = %v, want embed wrap of %v", err, want)
	}
	if sem.upsertCalled {
		t.Fatal("upsert must not run when embedding fails")
	}
}

// TestIndexFactEmptyVectorsIsNoOp covers the len(vecs)==0 short-circuit: no
// upsert, no error.
func TestIndexFactEmptyVectorsIsNoOp(t *testing.T) {
	sem := &fakeSemantic{}
	s := Store{Facts: fakeFacts{}, Semantic: sem, Embedder: emptyEmbedder{}}
	if err := s.Save(context.Background(), errScope(t), aFact()); err != nil {
		t.Fatalf("Save = %v, want nil", err)
	}
	if sem.upsertCalled {
		t.Fatal("upsert must not run when the embedder returns no vectors")
	}
}

func TestIndexFactUpsertError(t *testing.T) {
	want := errors.New("upsert rejected")
	sem := &fakeSemantic{upsertErr: want}
	s := Store{Facts: fakeFacts{}, Semantic: sem, Embedder: semantic.NewHashEmbedder(0)}
	if err := s.Save(context.Background(), errScope(t), aFact()); err == nil || !errors.Is(err, want) {
		t.Fatalf("Save error = %v, want upsert wrap of %v", err, want)
	}
	if !sem.upsertCalled {
		t.Fatal("expected an upsert attempt")
	}
}
