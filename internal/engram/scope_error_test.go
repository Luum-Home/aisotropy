package engram

import (
	"context"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// A zero-value facts.Scope has an empty tenant/user. The facts tier double
// ignores the scope, so the operation reaches engram's semanticScope, where
// semantic.ScopeFromContext rejects the empty tenant — driving the
// invalid-semantic-scope error branch that each dual-indexing method
// propagates. This is a real isolation-boundary guard: a fact must never be
// dual-indexed under an unvalidated (potentially cross-tenant) semantic scope.

func TestSaveInvalidSemanticScope(t *testing.T) {
	sem := &fakeSemantic{}
	s := Store{Facts: fakeFacts{}, Semantic: sem, Embedder: semantic.NewHashEmbedder(0)}
	err := s.Save(context.Background(), facts.Scope{}, aFact())
	if err == nil || !strings.Contains(err.Error(), "invalid semantic scope") {
		t.Fatalf("Save with an empty scope = %v, want an invalid-semantic-scope error", err)
	}
	if sem.upsertCalled {
		t.Fatal("no vector may be upserted once the semantic scope is rejected")
	}
}

func TestSupersedeInvalidSemanticScope(t *testing.T) {
	sem := &fakeSemantic{}
	s := Store{Facts: fakeFacts{}, Semantic: sem, Embedder: semantic.NewHashEmbedder(0)}
	err := s.Supersede(context.Background(), facts.Scope{}, "old", aFact())
	if err == nil || !strings.Contains(err.Error(), "invalid semantic scope") {
		t.Fatalf("Supersede with an empty scope = %v, want an invalid-semantic-scope error", err)
	}
	// The scope is derived before the old-vector delete, so nothing is touched.
	if sem.deleteCalled {
		t.Fatal("no vector may be deleted once the semantic scope is rejected")
	}
}

func TestDeleteInvalidSemanticScope(t *testing.T) {
	sem := &fakeSemantic{}
	s := Store{Facts: fakeFacts{}, Semantic: sem, Embedder: semantic.NewHashEmbedder(0)}
	err := s.Delete(context.Background(), facts.Scope{}, []string{"f1"})
	if err == nil || !strings.Contains(err.Error(), "invalid semantic scope") {
		t.Fatalf("Delete with an empty scope = %v, want an invalid-semantic-scope error", err)
	}
	if sem.deleteCalled {
		t.Fatal("no vector may be deleted once the semantic scope is rejected")
	}
}
