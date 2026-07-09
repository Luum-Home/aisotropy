package sleepcycle

import (
	"context"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/facts"
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
	sc := facts.Scope{TenantID: "acme", UserID: "alice"}
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
