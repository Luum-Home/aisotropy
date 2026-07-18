package main

// Test doubles shared by every _test.go file in this package. They exist so
// the harness's error/edge branches (a failing embedder, a failing store, a
// failing judge client) can be exercised deterministically with zero real
// network, subprocess, or model calls — the "inject fake embed/vector/LLM
// seams" seam this package's tests rely on.

import (
	"context"
	"errors"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// fakeEmbedder is a plain (non-role-aware) semantic.Embedder whose Embed
// behavior is fully controlled by the embed func, so a test can make it
// error, return a mismatched vector count, or return vectors of the wrong
// dimension — all the failure modes buildStore/EvalRetrieval must handle.
type fakeEmbedder struct {
	dim   int
	embed func(ctx context.Context, texts []string) ([][]float32, error)
}

func (f *fakeEmbedder) Dimension() int { return f.dim }

func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return f.embed(ctx, texts)
}

// constVector returns a func for fakeEmbedder.embed that hands back the same
// dim-wide vector for every input text — deterministic, nonzero, no I/O.
func constVector(dim int, fill float32) func(context.Context, []string) ([][]float32, error) {
	return func(_ context.Context, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i := range texts {
			v := make([]float32, dim)
			for j := range v {
				v[j] = fill
			}
			out[i] = v
		}
		return out, nil
	}
}

// failOnText returns a func for fakeEmbedder.embed that errors only when
// called with the single sentinel text (the shape a per-query EmbedQuery call
// takes), and otherwise delegates to a real deterministic HashEmbedder — so a
// test can fail exactly one query embed (EvalRetrieval's per-case call)
// without disturbing the earlier batched corpus embed (buildStore's call).
func failOnText(dim int, sentinel string, err error) func(context.Context, []string) ([][]float32, error) {
	real := semantic.NewHashEmbedder(dim)
	return func(ctx context.Context, texts []string) ([][]float32, error) {
		if len(texts) == 1 && texts[0] == sentinel {
			return nil, err
		}
		return real.Embed(ctx, texts)
	}
}

// roleFakeEmbedder is a RoleEmbedder whose query-role and passage-role
// vectors are deliberately different constants, so classifyEmbedder's
// query-vs-passage probe observes divergence and reports roleAware=true —
// exercising the branch a plain (non-role-aware) Embedder can never reach.
// It intentionally matches none of classifyEmbedder's named-type switch
// cases, so it also exercises the "default" backend-naming branch.
type roleFakeEmbedder struct {
	dim int
}

func (f *roleFakeEmbedder) Dimension() int { return f.dim }

func (f *roleFakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return f.EmbedForRole(ctx, texts, semantic.RoleUnspecified)
}

func (f *roleFakeEmbedder) EmbedForRole(_ context.Context, texts []string, role semantic.EmbedRole) ([][]float32, error) {
	fill := float32(0.25)
	if role == semantic.RoleQuery {
		fill = 0.75
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, f.dim)
		for j := range v {
			v[j] = fill
		}
		out[i] = v
	}
	return out, nil
}

// searchErrStore wraps a real *semantic.InMemoryStore and overrides Search to
// return a fixed error, so a test can force EvalRetrieval / topPassage down
// their search-failure path without a fake Store reimplementing the whole
// interface. Every other method (Upsert, Delete, ...) is the real, correct
// in-memory behavior via embedding.
type searchErrStore struct {
	*semantic.InMemoryStore
	err error
}

func (s *searchErrStore) Search(ctx context.Context, sc semantic.Scope, query []float32, limit int, kinds ...semantic.Kind) ([]semantic.ScoredPoint, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.InMemoryStore.Search(ctx, sc, query, limit, kinds...)
}

// errClient is an llm.Client whose StreamChat always fails immediately — no
// network, no goroutine, no delay — so a judge Score() call deterministically
// exhausts the reflection loop and RunSuite returns an error. It exercises
// runJudgePass's "run suite" error branch with zero real infra.
type errClient struct{}

func (errClient) Name() string { return "fake-error-client" }

func (errClient) StreamChat(context.Context, string, string) (<-chan llm.Token, error) {
	return nil, errors.New("fakeclient: deliberate stream failure for test coverage")
}

var _ semantic.Embedder = (*fakeEmbedder)(nil)
var _ semantic.RoleEmbedder = (*roleFakeEmbedder)(nil)
var _ semantic.Store = (*searchErrStore)(nil)
var _ llm.Client = errClient{}
