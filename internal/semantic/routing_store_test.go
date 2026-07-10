package semantic

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeRouter is a minimal resourceRouter test double: tenants in the
// dedicated set get their own "base_tenant" resource; every other tenant
// (including empty) shares base — mirroring routing.Router's real Resource
// contract without importing internal/routing.
type fakeRouter struct {
	dedicated map[string]struct{}
}

func newFakeRouter(dedicatedTenants ...string) fakeRouter {
	set := make(map[string]struct{}, len(dedicatedTenants))
	for _, t := range dedicatedTenants {
		set[t] = struct{}{}
	}
	return fakeRouter{dedicated: set}
}

func (r fakeRouter) Resource(base, tenant string) string {
	if tenant == "" {
		return base
	}
	if _, ok := r.dedicated[tenant]; ok {
		return base + "_" + tenant
	}
	return base
}

// inMemoryFactory builds a StoreFactory where every distinct resource name
// gets its own, separate InMemoryStore index of the given dimension — the
// property the isolation tests below depend on.
func inMemoryFactory(dim int) StoreFactory {
	return func(_ string) (Store, error) {
		return NewInMemoryStore(dim), nil
	}
}

func TestRoutingStore_DedicatedTenantIsolatedFromSharedPool(t *testing.T) {
	router := newFakeRouter("tenant-dedicated")
	rs, err := NewRoutingStore(router, "knowledge", []string{"tenant-dedicated"}, inMemoryFactory(4))
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}
	defer rs.Close()

	ctx := context.Background()
	dedicatedScope := Scope{TenantID: "tenant-dedicated", UserID: "u1"}
	sharedScope := Scope{TenantID: "tenant-shared", UserID: "u2"}

	dedicatedPoint := Point{ID: "d1", TenantID: "tenant-dedicated", UserID: "u1", Content: "dedicated", Vector: []float32{1, 0, 0, 0}}
	sharedPoint := Point{ID: "s1", TenantID: "tenant-shared", UserID: "u2", Content: "shared", Vector: []float32{1, 0, 0, 0}}

	if err := rs.Upsert(ctx, dedicatedScope, []Point{dedicatedPoint}); err != nil {
		t.Fatalf("Upsert dedicated: %v", err)
	}
	if err := rs.Upsert(ctx, sharedScope, []Point{sharedPoint}); err != nil {
		t.Fatalf("Upsert shared: %v", err)
	}

	// A shared-tenant search must never see the dedicated tenant's point --
	// it lives in an entirely separate index, not merely filtered out.
	sharedResults, err := rs.Search(ctx, sharedScope, []float32{1, 0, 0, 0}, 10)
	if err != nil {
		t.Fatalf("Search shared: %v", err)
	}
	if len(sharedResults) != 1 || sharedResults[0].ID != sharedPoint.ID {
		t.Fatalf("expected shared search to return only %q, got %+v", sharedPoint.ID, sharedResults)
	}

	// And vice versa: the dedicated tenant's search never sees the shared
	// pool's point.
	dedicatedResults, err := rs.Search(ctx, dedicatedScope, []float32{1, 0, 0, 0}, 10)
	if err != nil {
		t.Fatalf("Search dedicated: %v", err)
	}
	if len(dedicatedResults) != 1 || dedicatedResults[0].ID != dedicatedPoint.ID {
		t.Fatalf("expected dedicated search to return only %q, got %+v", dedicatedPoint.ID, dedicatedResults)
	}
}

func TestRoutingStore_LazyProvisionsUnlistedDedicatedTenant(t *testing.T) {
	// "tenant-late" is Dedicated per the router, but was NOT passed to
	// NewRoutingStore's pre-provisioned dedicated list -- it must still get
	// its own isolated store the first time it is addressed.
	router := newFakeRouter("tenant-late")
	rs, err := NewRoutingStore(router, "knowledge", nil, inMemoryFactory(4))
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}
	defer rs.Close()

	ctx := context.Background()
	lateScope := Scope{TenantID: "tenant-late", UserID: "u1"}
	sharedScope := Scope{TenantID: "tenant-shared", UserID: "u2"}

	latePoint := Point{ID: "late1", TenantID: "tenant-late", UserID: "u1", Content: "late", Vector: []float32{0, 1, 0, 0}}
	sharedPoint := Point{ID: "shared1", TenantID: "tenant-shared", UserID: "u2", Content: "shared", Vector: []float32{0, 1, 0, 0}}

	if err := rs.Upsert(ctx, lateScope, []Point{latePoint}); err != nil {
		t.Fatalf("Upsert late-provisioned dedicated tenant: %v", err)
	}
	if err := rs.Upsert(ctx, sharedScope, []Point{sharedPoint}); err != nil {
		t.Fatalf("Upsert shared: %v", err)
	}

	sharedResults, err := rs.Search(ctx, sharedScope, []float32{0, 1, 0, 0}, 10)
	if err != nil {
		t.Fatalf("Search shared: %v", err)
	}
	if len(sharedResults) != 1 || sharedResults[0].ID != sharedPoint.ID {
		t.Fatalf("expected shared search to return only %q, got %+v", sharedPoint.ID, sharedResults)
	}

	lateResults, err := rs.Search(ctx, lateScope, []float32{0, 1, 0, 0}, 10)
	if err != nil {
		t.Fatalf("Search late tenant: %v", err)
	}
	if len(lateResults) != 1 || lateResults[0].ID != latePoint.ID {
		t.Fatalf("expected late tenant search to return only %q, got %+v", latePoint.ID, lateResults)
	}

	// A second resolution of the same tenant must reuse the cached store
	// (same index), not silently create a second, disconnected one.
	again, err := rs.storeFor("tenant-late")
	if err != nil {
		t.Fatalf("storeFor (second call): %v", err)
	}
	reResults, err := again.Search(ctx, lateScope, []float32{0, 1, 0, 0}, 10)
	if err != nil {
		t.Fatalf("Search via cached store: %v", err)
	}
	if len(reResults) != 1 || reResults[0].ID != latePoint.ID {
		t.Fatalf("expected cached store to still contain %q, got %+v", latePoint.ID, reResults)
	}
}

func TestRoutingStore_ConcurrentUpsertAcrossTenants(t *testing.T) {
	router := newFakeRouter("tenant-dedicated-a", "tenant-dedicated-b")
	// Only pre-provision one of the two dedicated tenants; the other is left
	// to be lazily provisioned under concurrent load.
	rs, err := NewRoutingStore(router, "knowledge", []string{"tenant-dedicated-a"}, inMemoryFactory(4))
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}
	defer rs.Close()

	ctx := context.Background()
	tenants := []string{"tenant-shared-1", "tenant-shared-2", "tenant-dedicated-a", "tenant-dedicated-b"}
	const perTenant = 20

	var wg sync.WaitGroup
	for ti, tenant := range tenants {
		for i := range perTenant {
			wg.Add(1)
			go func(tenant string, ti, i int) {
				defer wg.Done()
				sc := Scope{TenantID: tenant, UserID: "u"}
				p := Point{
					ID:       tenant + "-" + string(rune('a'+i)),
					TenantID: tenant,
					UserID:   "u",
					Vector:   []float32{float32(ti), float32(i), 0, 0},
				}
				if err := rs.Upsert(ctx, sc, []Point{p}); err != nil {
					t.Errorf("Upsert(%s): %v", tenant, err)
				}
			}(tenant, ti, i)
		}
	}
	wg.Wait()

	for _, tenant := range tenants {
		sc := Scope{TenantID: tenant, UserID: "u"}
		results, err := rs.Search(ctx, sc, []float32{0, 0, 0, 0}, perTenant*2)
		if err != nil {
			t.Fatalf("Search(%s): %v", tenant, err)
		}
		if len(results) != perTenant {
			t.Fatalf("tenant %s: expected %d points, got %d", tenant, perTenant, len(results))
		}
	}
}

// countingStore wraps a Store to count Close calls, so a test can assert every
// physical store opened was also closed.
type countingStore struct {
	Store
	closed *int32
}

func (c countingStore) Close() error {
	atomic.AddInt32(c.closed, 1)
	return c.Store.Close()
}

// TestRoutingStore_ConcurrentCloseNoLeak forces the Close-vs-provision race the
// holder mutex resolves: many goroutines each provision a NEW dedicated
// collection (a fresh factory call) while Close runs concurrently. Must be clean
// under -race, and opened == closed: no collection opened after Close leaks.
func TestRoutingStore_ConcurrentCloseNoLeak(t *testing.T) {
	const n = 64
	tenants := make([]string, n)
	for i := range n {
		tenants[i] = "t" + strconv.Itoa(i)
	}
	router := newFakeRouter(tenants...)

	var opened, closed int32
	factory := func(_ string) (Store, error) {
		atomic.AddInt32(&opened, 1)
		return countingStore{Store: NewInMemoryStore(4), closed: &closed}, nil
	}

	// No pre-provisioned dedicated tenants: every Upsert below is a first open.
	rs, err := NewRoutingStore(router, "knowledge", nil, factory)
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}

	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sc := Scope{TenantID: "t" + strconv.Itoa(i), UserID: "u"}
			p := Point{ID: "p" + strconv.Itoa(i), TenantID: sc.TenantID, UserID: sc.UserID, Vector: []float32{1, 0, 0, 0}}
			_ = rs.Upsert(ctx, sc, []Point{p})
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := rs.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	wg.Wait()

	if err := rs.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got, want := atomic.LoadInt32(&closed), atomic.LoadInt32(&opened); got != want {
		t.Fatalf("store leak: opened %d, closed %d", want, got)
	}
}

// TestRoutingStore_UseAfterClose proves post-Close dispatch fails closed.
func TestRoutingStore_UseAfterClose(t *testing.T) {
	router := newFakeRouter("dedicated-a")
	rs, err := NewRoutingStore(router, "knowledge", []string{"dedicated-a"}, inMemoryFactory(4))
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}
	if err := rs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ctx := context.Background()
	sc := Scope{TenantID: "tenant-shared", UserID: "u"}
	p := Point{ID: "p1", TenantID: sc.TenantID, UserID: sc.UserID, Vector: []float32{1, 0, 0, 0}}
	if err := rs.Upsert(ctx, sc, []Point{p}); !errors.Is(err, ErrRoutingStoreClosed) {
		t.Fatalf("Upsert after Close = %v, want ErrRoutingStoreClosed", err)
	}
	if _, err := rs.Search(ctx, sc, []float32{1, 0, 0, 0}, 5); !errors.Is(err, ErrRoutingStoreClosed) {
		t.Fatalf("Search after Close = %v, want ErrRoutingStoreClosed", err)
	}
}
