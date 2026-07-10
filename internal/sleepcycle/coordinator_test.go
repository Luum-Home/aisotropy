package sleepcycle

import (
	"context"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/engram"
	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

type fakeRunner struct {
	summary Summary
	err     error
}

func (f fakeRunner) Run(_ context.Context, _, _ string) (Summary, error) {
	return f.summary, f.err
}

func TestCoordinatorAppliesCleanup(t *testing.T) {
	fs, err := facts.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	sc, err := facts.NewScope("acme", "alice")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Two duplicates ("keep" wins on importance, "drop" is redundant) + one distinct.
	for _, f := range []facts.Fact{
		{ID: "keep", TenantID: "acme", UserID: "alice", Content: "prefers dark mode", Importance: 0.9},
		{ID: "drop", TenantID: "acme", UserID: "alice", Content: "prefers dark mode", Importance: 0.5},
		{ID: "other", TenantID: "acme", UserID: "alice", Content: "region is eu-west-1", Importance: 0.8},
	} {
		if err := fs.Save(ctx, sc, f); err != nil {
			t.Fatal(err)
		}
	}

	// The worker reports "drop" as the non-canonical duplicate.
	c := Coordinator{
		Store:  fs,
		Engram: engram.Store{Facts: fs},
		Runner: fakeRunner{summary: Summary{RowsIn: 3, RowsOut: 2, Dropped: 1, DroppedIDs: []string{"drop"}}},
	}
	got, err := c.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got.Dropped != 1 {
		t.Fatalf("summary dropped = %d, want 1", got.Dropped)
	}

	all, err := fs.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("facts after cleanup = %d, want 2", len(all))
	}
	for _, f := range all {
		if f.ID == "drop" {
			t.Fatal("the dropped duplicate was not deleted from the live store")
		}
	}
}

func TestCoordinatorEmptyStoreIsNoOp(t *testing.T) {
	fs, err := facts.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	c := Coordinator{Store: fs, Runner: fakeRunner{}}
	got, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce on empty store: %v", err)
	}
	if got.RowsIn != 0 || len(got.DroppedIDs) != 0 {
		t.Fatalf("expected empty summary, got %+v", got)
	}
}

// TestCoordinatorPrunesTurnsPerActiveScope is the ADR-0017 phase-3 wiring
// test: RunOnce calls Semantic.PruneTurns once per (tenant,user) scope
// discovered from the fact export, passing through the configured
// TurnMaxAge/TurnMaxCount policy, and never touches a scope that has no
// current facts.
func TestCoordinatorPrunesTurnsPerActiveScope(t *testing.T) {
	fs, err := facts.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	ctx := context.Background()

	acmeScope, err := facts.NewScope("acme", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Save(ctx, acmeScope, facts.Fact{ID: "f1", TenantID: "acme", UserID: "alice", Content: "prefers dark mode", Importance: 0.5}); err != nil {
		t.Fatal(err)
	}

	sem := &fakeSemanticPruner{}
	c := Coordinator{
		Store:        fs,
		Engram:       engram.Store{Facts: fs},
		Runner:       fakeRunner{summary: Summary{RowsIn: 1, RowsOut: 1}},
		Semantic:     sem,
		TurnMaxAge:   7 * 24 * time.Hour,
		TurnMaxCount: 500,
	}
	if _, err := c.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if len(sem.calls) != 1 {
		t.Fatalf("PruneTurns calls = %d, want 1 (one active tenant/user scope)", len(sem.calls))
	}
	call := sem.calls[0]
	if call.tenant != "acme" || call.user != "alice" {
		t.Fatalf("PruneTurns scope = %s/%s, want acme/alice", call.tenant, call.user)
	}
	if call.maxAge != 7*24*time.Hour || call.maxCount != 500 {
		t.Fatalf("PruneTurns policy = (%v, %d), want (%v, 500)", call.maxAge, call.maxCount, 7*24*time.Hour)
	}
}

// TestCoordinatorSkipsTurnPruningWhenDisabled confirms a nil Semantic store
// or a fully-disabled policy never calls PruneTurns.
func TestCoordinatorSkipsTurnPruningWhenDisabled(t *testing.T) {
	fs, err := facts.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	ctx := context.Background()

	sc, err := facts.NewScope("acme", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Save(ctx, sc, facts.Fact{ID: "f1", TenantID: "acme", UserID: "alice", Content: "prefers dark mode", Importance: 0.5}); err != nil {
		t.Fatal(err)
	}

	sem := &fakeSemanticPruner{}
	c := Coordinator{
		Store:  fs,
		Engram: engram.Store{Facts: fs},
		Runner: fakeRunner{summary: Summary{RowsIn: 1, RowsOut: 1}},
		// Semantic left nil: turn pruning must not run at all.
	}
	if _, err := c.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(sem.calls) != 0 {
		t.Fatalf("PruneTurns calls = %d, want 0 (nil Semantic disables pruning)", len(sem.calls))
	}

	// Semantic configured but both policy knobs disabled: still a no-op.
	c.Semantic = sem
	if _, err := c.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(sem.calls) != 0 {
		t.Fatalf("PruneTurns calls = %d, want 0 (TurnMaxAge<=0 and TurnMaxCount<=0)", len(sem.calls))
	}
}

// fakeSemanticPruner is a semantic.Store test double that only implements
// what this test needs (PruneTurns); every other method panics if called,
// since the coordinator's Turn-pruning wiring must call PruneTurns only.
type fakeSemanticPruner struct {
	calls []prunecall
}

type prunecall struct {
	tenant, user string
	maxAge       time.Duration
	maxCount     int
}

func (f *fakeSemanticPruner) Upsert(context.Context, semantic.Scope, []semantic.Point) error {
	panic("Upsert not expected in this test")
}

func (f *fakeSemanticPruner) Search(context.Context, semantic.Scope, []float32, int, ...semantic.Kind) ([]semantic.ScoredPoint, error) {
	panic("Search not expected in this test")
}

func (f *fakeSemanticPruner) Delete(context.Context, semantic.Scope, []string) error {
	panic("Delete not expected in this test")
}

func (f *fakeSemanticPruner) PruneTurns(_ context.Context, sc semantic.Scope, maxAge time.Duration, maxCount int) (int, error) {
	f.calls = append(f.calls, prunecall{tenant: sc.Tenant(), user: sc.User(), maxAge: maxAge, maxCount: maxCount})
	return 0, nil
}

func (f *fakeSemanticPruner) Close() error { return nil }
