package semantic

import (
	"context"
	"testing"
	"time"
)

// TestVectorBackendStorePruneTurnsNoEvictionReturnsZero verifies the
// scopedVectorStore prune path's "nothing to delete" branch: a count cap larger
// than the turn population and an age window that spares every turn yields
// (0, nil) without issuing a backend Delete.
func TestVectorBackendStorePruneTurnsNoEvictionReturnsZero(t *testing.T) {
	b := newFakeVectorBackend()
	s := NewVectorBackendStore(b, 3)
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()

	now := time.Now()
	pts := []Point{
		{ID: "t1", TenantID: "acme", UserID: "alice", Kind: KindTurn, Vector: vec3(1, 0, 0), CreatedAt: now},
		{ID: "t2", TenantID: "acme", UserID: "alice", Kind: KindTurn, Vector: vec3(1, 0, 0), CreatedAt: now},
	}
	if err := s.Upsert(ctx, sc, pts); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// maxCount above the population and a generous age window: nothing evicted.
	n, err := s.PruneTurns(ctx, sc, time.Hour, 10)
	if err != nil {
		t.Fatalf("PruneTurns: %v", err)
	}
	if n != 0 {
		t.Fatalf("pruned %d, want 0 (no turn is old enough or over the cap)", n)
	}
}

// TestVectorBackendStorePruneTurnsCountTieBreaksByID verifies the count-cap
// eviction over the sealed backend decorator uses the same deterministic
// ascending-ID tie-break as InMemoryStore when two turns share a CreatedAt.
func TestVectorBackendStorePruneTurnsCountTieBreaksByID(t *testing.T) {
	b := newFakeVectorBackend()
	s := NewVectorBackendStore(b, 3)
	sc := Scope{tenant: "acme", user: "alice"}
	ctx := context.Background()

	ts := time.UnixMilli(1_700_000_000_000)
	pts := []Point{
		{ID: "turn-b", TenantID: "acme", UserID: "alice", Kind: KindTurn, Vector: vec3(1, 0, 0), CreatedAt: ts},
		{ID: "turn-a", TenantID: "acme", UserID: "alice", Kind: KindTurn, Vector: vec3(1, 0, 0), CreatedAt: ts},
	}
	if err := s.Upsert(ctx, sc, pts); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	n, err := s.PruneTurns(ctx, sc, 0, 1)
	if err != nil {
		t.Fatalf("PruneTurns: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned %d, want 1 (cap of 1 over 2 equal-time turns)", n)
	}

	got, err := s.Search(ctx, sc, vec3(1, 0, 0), 10, KindTurn)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].ID != "turn-a" {
		t.Fatalf("survivors = %#v, want exactly [turn-a] (ascending-ID tie-break)", got)
	}
}
