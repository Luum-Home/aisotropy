package api

import (
	"context"
	"errors"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/primitives"
)

type failingExecutor struct{}

func (failingExecutor) Execute(context.Context, string, map[string]string) (string, error) {
	return "", errors.New("tool boom")
}

// TestFrictionExecutorRecordsOnFailure: a failed tool invocation must accrue
// friction for that user's primitive so the sleep cycle can rewrite it.
func TestFrictionExecutorRecordsOnFailure(t *testing.T) {
	store := primitives.NewStore()
	fe := frictionExecutor{
		inner:    failingExecutor{},
		detector: primitives.NewFrictionDetector(store),
		tenant:   "acme",
		user:     "alice",
	}
	if _, err := fe.Execute(context.Background(), "write_tasks", nil); err == nil {
		t.Fatal("expected the inner tool error to propagate")
	}
	p, ok := store.Get("acme", "alice", "write_tasks")
	if !ok || p.FrictionScore <= 0 {
		t.Fatalf("friction not recorded on failure: primitive=%+v ok=%v", p, ok)
	}
}

// TestFrictionExecutorNoFrictionOnSuccess: a successful invocation records no
// friction (the common case).
func TestFrictionExecutorNoFrictionOnSuccess(t *testing.T) {
	store := primitives.NewStore()
	fe := frictionExecutor{
		inner:    DemoExecutor{},
		detector: primitives.NewFrictionDetector(store),
		tenant:   "acme",
		user:     "alice",
	}
	res, err := fe.Execute(context.Background(), "search_docs", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res == "" {
		t.Fatal("expected an echo result")
	}
	if _, ok := store.Get("acme", "alice", "search_docs"); ok {
		t.Fatal("friction should not be recorded on a successful invocation")
	}
}
