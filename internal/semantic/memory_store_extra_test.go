package semantic

import (
	"context"
	"testing"
	"time"
)

// TestInMemoryStoreSearchNonPositiveLimit verifies the limit<=0 guard returns
// (nil, nil) without scanning — a valid-dimension query so the guard, not the
// dimension check, is what short-circuits.
func TestInMemoryStoreSearchNonPositiveLimit(t *testing.T) {
	st := NewInMemoryStore(3)
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()
	if err := st.Upsert(ctx, sc, []Point{{ID: "p1", TenantID: "acme", UserID: "alice", Vector: []float32{1, 0, 0}}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := st.Search(ctx, sc, []float32{1, 0, 0}, 0)
	if err != nil {
		t.Fatalf("Search(limit=0): %v", err)
	}
	if got != nil {
		t.Fatalf("Search(limit=0) = %#v, want nil", got)
	}
	if got, err := st.Search(ctx, sc, []float32{1, 0, 0}, -5); err != nil || got != nil {
		t.Fatalf("Search(limit<0) = (%#v, %v), want (nil, nil)", got, err)
	}
}

// TestInMemoryStoreSearchTruncatesToLimit verifies a result set larger than
// limit is trimmed to exactly limit, keeping the highest-similarity prefix.
func TestInMemoryStoreSearchTruncatesToLimit(t *testing.T) {
	st := NewInMemoryStore(3)
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()

	pts := []Point{
		{ID: "a", TenantID: "acme", UserID: "alice", Vector: []float32{1, 0, 0}},
		{ID: "b", TenantID: "acme", UserID: "alice", Vector: []float32{0.9, 0.1, 0}},
		{ID: "c", TenantID: "acme", UserID: "alice", Vector: []float32{0, 1, 0}},
	}
	if err := st.Upsert(ctx, sc, pts); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := st.Search(ctx, sc, []float32{1, 0, 0}, 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(results) = %d, want 2 (truncated to limit)", len(got))
	}
	// The two nearest to [1,0,0] are "a" then "b"; "c" (orthogonal) is dropped.
	if got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("truncated top-2 = [%s %s], want [a b]", got[0].ID, got[1].ID)
	}
}

// TestInMemoryStorePruneTurnsCountTieBreaksByID verifies the count-cap eviction
// path's deterministic tie-break: when two KindTurn points share the exact same
// CreatedAt and the cap forces one out, the survivor is chosen by ascending ID
// (not by randomized map order), so the same input always evicts the same point.
func TestInMemoryStorePruneTurnsCountTieBreaksByID(t *testing.T) {
	st := NewInMemoryStore(3)
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()
	ts := time.UnixMilli(1_700_000_000_000).UTC()

	// Two turns, identical CreatedAt. maxCount=1 keeps exactly one.
	pts := []Point{
		{ID: "turn-b", TenantID: "acme", UserID: "alice", Kind: KindTurn, Vector: []float32{1, 0, 0}, CreatedAt: ts},
		{ID: "turn-a", TenantID: "acme", UserID: "alice", Kind: KindTurn, Vector: []float32{1, 0, 0}, CreatedAt: ts},
	}
	if err := st.Upsert(ctx, sc, pts); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	n, err := st.PruneTurns(ctx, sc, 0, 1)
	if err != nil {
		t.Fatalf("PruneTurns: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned %d, want 1 (cap of 1 over 2 equal-time turns)", n)
	}

	// The surviving turn must be "turn-a" (ascending-ID tie-break keeps the
	// lexicographically-smaller id as the "most recent" prefix).
	got, err := st.Search(ctx, sc, []float32{1, 0, 0}, 10, KindTurn)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("survivors = %d, want 1", len(got))
	}
	if got[0].ID != "turn-a" {
		t.Fatalf("survivor = %q, want turn-a (deterministic ID tie-break)", got[0].ID)
	}
}
