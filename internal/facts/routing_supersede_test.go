package facts

import (
	"context"
	"testing"
)

// TestRoutingStore_SupersedeDispatches proves RoutingStore.Supersede reaches the
// physical store for the scope's tenant (the routing-dispatch path is otherwise
// uncovered by the existing routing tests).
func TestRoutingStore_SupersedeDispatches(t *testing.T) {
	router := fakeRouter{dedicated: map[string]bool{"dedicated-a": true}}
	rs, err := NewRoutingStore(router, "facts", []string{"dedicated-a"}, memoryFactory)
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}
	defer func() { _ = rs.Close() }()

	ctx := context.Background()
	sc := mustScope(t, "dedicated-a", "user-1")
	if err := rs.Save(ctx, sc, Fact{ID: "old", TenantID: "dedicated-a", UserID: "user-1",
		Content: "migrating billing to postgres", Importance: 0.5}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := rs.Supersede(ctx, sc, "old", Fact{ID: "new", TenantID: "dedicated-a", UserID: "user-1",
		Content: "finished migrating billing", Importance: 0.6}); err != nil {
		t.Fatalf("Supersede: %v", err)
	}
	got, err := rs.Search(ctx, sc, "migrating billing", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].ID != "new" {
		t.Fatalf("after supersede want only 'new', got %+v", got)
	}
}

// TestNewDocBackendStore_PublicConstructor exercises the blessed public seam
// (NewDocBackendStore) end to end, as a consumer would use it — distinct from the
// backend_test.go cases that construct scopedDocStore directly for clock control.
func TestNewDocBackendStore_PublicConstructor(t *testing.T) {
	store := NewDocBackendStore(newFakeDocBackend())
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	sc := Scope{tenant: "acme", user: "alice"}
	if err := store.Save(ctx, sc, Fact{ID: "f", TenantID: "acme", UserID: "alice", Content: "public seam keyword"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Search(ctx, sc, "keyword", 5)
	if err != nil || len(got) != 1 || got[0].ID != "f" {
		t.Fatalf("Search via public constructor: got %d (%v)", len(got), err)
	}
}
