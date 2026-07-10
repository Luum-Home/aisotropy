package facts

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/tenancy"
)

// fakeRouter routes a fixed set of tenants to their own dedicated resource
// (base + "_" + tenant); every other tenant shares base, mirroring
// internal/routing.Router's Shared-tier behavior without importing it.
type fakeRouter struct {
	dedicated map[string]bool
}

func (r fakeRouter) Resource(base, tenant string) string {
	if base == "" || tenant == "" {
		return base
	}
	if r.dedicated[tenant] {
		return base + "_" + tenant
	}
	return base
}

// memoryFactory opens a fresh, independent in-memory SQLite store per call —
// exactly what RoutingStore needs to prove physical isolation between
// resources in a test.
func memoryFactory(resource string) (Store, error) {
	return OpenSQLite(":memory:")
}

func mustScope(t *testing.T, tenant, user string) Scope {
	t.Helper()
	sc, err := NewScope(tenant, user)
	if err != nil {
		t.Fatalf("NewScope(%q, %q): %v", tenant, user, err)
	}
	return sc
}

func fact(sc Scope, id, content string) Fact {
	now := time.Now()
	return Fact{
		ID:        id,
		TenantID:  sc.TenantID,
		UserID:    sc.UserID,
		Content:   content,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func TestRoutingStore_SharedTenantVisibleInSharedStore(t *testing.T) {
	router := fakeRouter{dedicated: map[string]bool{"dedicated-a": true}}
	rs, err := NewRoutingStore(router, "facts", []string{"dedicated-a"}, memoryFactory)
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}
	defer rs.Close()

	ctx := context.Background()
	sharedSc := mustScope(t, "shared-a", "user-1")
	if err := rs.Save(ctx, sharedSc, fact(sharedSc, "f1", "prefers dark mode")); err != nil {
		t.Fatalf("Save shared: %v", err)
	}

	results, err := rs.Search(ctx, sharedSc, "dark", 10)
	if err != nil {
		t.Fatalf("Search shared: %v", err)
	}
	if len(results) != 1 || results[0].ID != "f1" {
		t.Fatalf("expected shared fact f1 via Search, got %+v", results)
	}
}

func TestRoutingStore_DedicatedTenantPhysicallyIsolated(t *testing.T) {
	router := fakeRouter{dedicated: map[string]bool{"dedicated-a": true}}
	rs, err := NewRoutingStore(router, "facts", []string{"dedicated-a"}, memoryFactory)
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}
	defer rs.Close()

	ctx := context.Background()
	sharedSc := mustScope(t, "shared-a", "user-1")
	dedicatedSc := mustScope(t, "dedicated-a", "user-2")

	if err := rs.Save(ctx, sharedSc, fact(sharedSc, "f-shared", "shared content")); err != nil {
		t.Fatalf("Save shared: %v", err)
	}
	if err := rs.Save(ctx, dedicatedSc, fact(dedicatedSc, "f-dedicated", "dedicated content")); err != nil {
		t.Fatalf("Save dedicated: %v", err)
	}

	// The dedicated tenant's fact must not be visible in the shared pool: a
	// second RoutingStore instance sharing nothing with rs, but querying the
	// SAME shared resource name via a factory that always returns a NEW
	// store, cannot see it either way — so instead we verify isolation by
	// inspecting ExportAll's partitioning directly against the underlying
	// stores that rs itself provisioned.
	rs.mu.Lock()
	sharedHolder, ok := rs.stores["facts"]
	rs.mu.Unlock()
	if !ok || sharedHolder.store == nil {
		t.Fatalf("expected shared store %q to be provisioned", "facts")
	}
	sharedExport, err := sharedHolder.store.ExportAll(ctx)
	if err != nil {
		t.Fatalf("ExportAll shared store: %v", err)
	}
	for _, f := range sharedExport {
		if f.ID == "f-dedicated" {
			t.Fatalf("dedicated tenant's fact leaked into the shared store: %+v", f)
		}
	}

	rs.mu.Lock()
	dedicatedHolder, ok := rs.stores["facts_dedicated-a"]
	rs.mu.Unlock()
	if !ok || dedicatedHolder.store == nil {
		t.Fatalf("expected dedicated store %q to be provisioned", "facts_dedicated-a")
	}
	dedicatedExport, err := dedicatedHolder.store.ExportAll(ctx)
	if err != nil {
		t.Fatalf("ExportAll dedicated store: %v", err)
	}
	for _, f := range dedicatedExport {
		if f.ID == "f-shared" {
			t.Fatalf("shared tenant's fact leaked into the dedicated store: %+v", f)
		}
	}
	if len(dedicatedExport) != 1 || dedicatedExport[0].ID != "f-dedicated" {
		t.Fatalf("expected only f-dedicated in the dedicated store, got %+v", dedicatedExport)
	}

	// Also verify via Search on the routing store itself: searching under the
	// dedicated tenant's scope finds its own fact, and searching under the
	// shared tenant's scope does not.
	dedicatedResults, err := rs.Search(ctx, dedicatedSc, "dedicated", 10)
	if err != nil {
		t.Fatalf("Search dedicated: %v", err)
	}
	if len(dedicatedResults) != 1 || dedicatedResults[0].ID != "f-dedicated" {
		t.Fatalf("expected dedicated fact via Search, got %+v", dedicatedResults)
	}

	sharedResults, err := rs.Search(ctx, sharedSc, "dedicated", 10)
	if err != nil {
		t.Fatalf("Search shared for dedicated content: %v", err)
	}
	if len(sharedResults) != 0 {
		t.Fatalf("expected no hits for dedicated content under the shared tenant, got %+v", sharedResults)
	}
}

func TestRoutingStore_ExportAllUnionsAcrossTiers(t *testing.T) {
	router := fakeRouter{dedicated: map[string]bool{"dedicated-a": true}}
	rs, err := NewRoutingStore(router, "facts", []string{"dedicated-a"}, memoryFactory)
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}
	defer rs.Close()

	ctx := context.Background()
	sharedSc := mustScope(t, "shared-a", "user-1")
	dedicatedSc := mustScope(t, "dedicated-a", "user-2")

	if err := rs.Save(ctx, sharedSc, fact(sharedSc, "f-shared", "shared content")); err != nil {
		t.Fatalf("Save shared: %v", err)
	}
	if err := rs.Save(ctx, dedicatedSc, fact(dedicatedSc, "f-dedicated", "dedicated content")); err != nil {
		t.Fatalf("Save dedicated: %v", err)
	}

	all, err := rs.ExportAll(ctx)
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	ids := make([]string, 0, len(all))
	for _, f := range all {
		ids = append(ids, f.ID)
	}
	sort.Strings(ids)
	if len(ids) != 2 || ids[0] != "f-dedicated" || ids[1] != "f-shared" {
		t.Fatalf("expected [f-dedicated f-shared], got %v", ids)
	}
}

func TestRoutingStore_DeleteScopedToDedicatedStore(t *testing.T) {
	router := fakeRouter{dedicated: map[string]bool{"dedicated-a": true}}
	rs, err := NewRoutingStore(router, "facts", []string{"dedicated-a"}, memoryFactory)
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}
	defer rs.Close()

	ctx := context.Background()
	sharedSc := mustScope(t, "shared-a", "user-1")
	dedicatedSc := mustScope(t, "dedicated-a", "user-2")

	if err := rs.Save(ctx, sharedSc, fact(sharedSc, "f-shared", "shared content")); err != nil {
		t.Fatalf("Save shared: %v", err)
	}
	if err := rs.Save(ctx, dedicatedSc, fact(dedicatedSc, "f-dedicated", "dedicated content")); err != nil {
		t.Fatalf("Save dedicated: %v", err)
	}

	if err := rs.Delete(ctx, dedicatedSc, []string{"f-dedicated"}); err != nil {
		t.Fatalf("Delete dedicated: %v", err)
	}

	all, err := rs.ExportAll(ctx)
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	if len(all) != 1 || all[0].ID != "f-shared" {
		t.Fatalf("expected only f-shared to remain, got %+v", all)
	}
}

func TestRoutingStore_LazyProvisioningForUnlistedDedicatedTenant(t *testing.T) {
	router := fakeRouter{dedicated: map[string]bool{"dedicated-a": true, "dedicated-late": true}}
	// dedicated-late is NOT in the pre-provisioned list.
	rs, err := NewRoutingStore(router, "facts", []string{"dedicated-a"}, memoryFactory)
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}
	defer rs.Close()

	ctx := context.Background()
	lateSc := mustScope(t, "dedicated-late", "user-9")

	if err := rs.Save(ctx, lateSc, fact(lateSc, "f-late", "late content")); err != nil {
		t.Fatalf("Save lazily-provisioned dedicated tenant: %v", err)
	}

	results, err := rs.Search(ctx, lateSc, "late", 10)
	if err != nil {
		t.Fatalf("Search lazily-provisioned dedicated tenant: %v", err)
	}
	if len(results) != 1 || results[0].ID != "f-late" {
		t.Fatalf("expected f-late via Search, got %+v", results)
	}

	rs.mu.Lock()
	_, ok := rs.stores["facts_dedicated-late"]
	rs.mu.Unlock()
	if !ok {
		t.Fatalf("expected facts_dedicated-late to be lazily provisioned")
	}
}

func TestRoutingStore_ConcurrentSaveNoRace(t *testing.T) {
	router := fakeRouter{dedicated: map[string]bool{"dedicated-a": true, "dedicated-b": true}}
	rs, err := NewRoutingStore(router, "facts", nil, memoryFactory)
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}
	defer rs.Close()

	ctx := context.Background()
	tenants := []string{"shared-a", "shared-b", "dedicated-a", "dedicated-b"}

	var wg sync.WaitGroup
	const perTenant = 20
	for _, tenant := range tenants {
		for i := range perTenant {
			wg.Add(1)
			go func(tenant string, i int) {
				defer wg.Done()
				sc := mustScopeNoFatal(ctx, tenant, "user-x")
				id := tenant + "-" + strconv.Itoa(i)
				if err := rs.Save(ctx, sc, fact(sc, id, "concurrent content")); err != nil {
					t.Errorf("concurrent Save(%s, %d): %v", tenant, i, err)
				}
			}(tenant, i)
		}
	}
	wg.Wait()

	all, err := rs.ExportAll(ctx)
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	if len(all) != len(tenants)*perTenant {
		t.Fatalf("expected %d facts, got %d", len(tenants)*perTenant, len(all))
	}
}

// mustScopeNoFatal builds a Scope without requiring *testing.T, so it can be
// called from goroutines spawned inside a test (t.Fatalf is not
// goroutine-safe to call from anywhere but the test's own goroutine). ctx is
// threaded through (rather than NewScope's internal context.Background()) so
// the validation honors the caller's cancellation.
func mustScopeNoFatal(ctx context.Context, tenant, user string) Scope {
	sc, err := ScopeFromContext(tenancy.WithUser(tenancy.WithTenant(ctx, tenant), user))
	if err != nil {
		panic(err)
	}
	return sc
}

// TestRoutingStore_ExportAllGloballySorted proves ExportAll restores the
// "ordered by id" contract ACROSS the union of physical stores, not just
// within each one: the shared pool and a dedicated tenant each return their
// own facts id-sorted, and a naive concatenation would interleave incorrectly.
func TestRoutingStore_ExportAllGloballySorted(t *testing.T) {
	router := fakeRouter{dedicated: map[string]bool{"dedicated-a": true}}
	rs, err := NewRoutingStore(router, "facts", []string{"dedicated-a"}, memoryFactory)
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}
	defer rs.Close()

	ctx := context.Background()
	// IDs chosen so the globally-sorted order (a,b,c,d) interleaves between the
	// two physical stores: shared holds b,d and dedicated holds a,c.
	shared := mustScope(t, "shared-a", "user-1")
	dedicated := mustScope(t, "dedicated-a", "user-1")
	if err := rs.Save(ctx, shared, fact(shared, "b", "shared b")); err != nil {
		t.Fatalf("Save b: %v", err)
	}
	if err := rs.Save(ctx, shared, fact(shared, "d", "shared d")); err != nil {
		t.Fatalf("Save d: %v", err)
	}
	if err := rs.Save(ctx, dedicated, fact(dedicated, "a", "dedicated a")); err != nil {
		t.Fatalf("Save a: %v", err)
	}
	if err := rs.Save(ctx, dedicated, fact(dedicated, "c", "dedicated c")); err != nil {
		t.Fatalf("Save c: %v", err)
	}

	all, err := rs.ExportAll(ctx)
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	got := make([]string, len(all))
	for i, f := range all {
		got[i] = f.ID
	}
	want := []string{"a", "b", "c", "d"}
	if !sort.SliceIsSorted(all, func(i, j int) bool { return all[i].ID < all[j].ID }) {
		t.Fatalf("ExportAll not globally sorted by id: %v", got)
	}
	if len(got) != len(want) {
		t.Fatalf("ExportAll returned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ExportAll order = %v, want %v", got, want)
		}
	}
}

// countingStore wraps a Store to count how many times Close is called, so a
// test can assert every physical store that was opened was also closed.
type countingStore struct {
	Store
	closed *int32
}

func (c countingStore) Close() error {
	atomic.AddInt32(c.closed, 1)
	return c.Store.Close()
}

// TestRoutingStore_ConcurrentCloseNoLeak forces the Close-vs-provision race the
// holder mutex is designed to resolve: many goroutines each provision a NEW
// dedicated resource (a fresh factory call, hence a fresh open) while Close runs
// concurrently. It must run clean under -race, and the invariant that closes the
// hazard is opened == closed: no physical store is ever opened after Close in a
// way that leaks it unclosed.
func TestRoutingStore_ConcurrentCloseNoLeak(t *testing.T) {
	const n = 64
	dedicated := make(map[string]bool, n)
	for i := range n {
		dedicated["t"+strconv.Itoa(i)] = true
	}
	router := fakeRouter{dedicated: dedicated}

	var opened, closed int32
	factory := func(resource string) (Store, error) {
		atomic.AddInt32(&opened, 1)
		s, err := OpenSQLite(":memory:")
		if err != nil {
			return nil, err
		}
		return countingStore{Store: s, closed: &closed}, nil
	}

	// No pre-provisioned dedicated tenants: every Save below is a first open.
	rs, err := NewRoutingStore(router, "facts", nil, factory)
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}

	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sc := mustScopeNoFatal(ctx, "t"+strconv.Itoa(i), "user-x")
			// Either succeeds (opened before Close) or fails closed (after Close);
			// both are correct. We only require no race and no leak.
			_ = rs.Save(ctx, sc, fact(sc, "f"+strconv.Itoa(i), "concurrent open"))
		}(i)
	}
	// Close concurrently with the in-flight provisions.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := rs.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	wg.Wait()

	// A second Close must be a no-op that closes nothing new.
	if err := rs.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	if got, want := atomic.LoadInt32(&closed), atomic.LoadInt32(&opened); got != want {
		t.Fatalf("store leak: opened %d, closed %d (every opened store must be closed)", want, got)
	}
}

// TestRoutingStore_UseAfterClose proves that after Close, every dispatch fails
// closed with ErrRoutingStoreClosed instead of reaching a shut-down backend.
func TestRoutingStore_UseAfterClose(t *testing.T) {
	router := fakeRouter{dedicated: map[string]bool{"dedicated-a": true}}
	rs, err := NewRoutingStore(router, "facts", []string{"dedicated-a"}, memoryFactory)
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}
	if err := rs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ctx := context.Background()
	sc := mustScope(t, "shared-a", "user-1")
	if err := rs.Save(ctx, sc, fact(sc, "x", "after close")); !errors.Is(err, ErrRoutingStoreClosed) {
		t.Fatalf("Save after Close = %v, want ErrRoutingStoreClosed", err)
	}
	if _, err := rs.Search(ctx, sc, "after", 5); !errors.Is(err, ErrRoutingStoreClosed) {
		t.Fatalf("Search after Close = %v, want ErrRoutingStoreClosed", err)
	}
	if _, err := rs.ExportAll(ctx); !errors.Is(err, ErrRoutingStoreClosed) {
		t.Fatalf("ExportAll after Close = %v, want ErrRoutingStoreClosed", err)
	}
}
