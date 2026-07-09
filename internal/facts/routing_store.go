package facts

import (
	"context"
	"sync"
)

// resourceRouter is the seam internal/routing.Router satisfies. It is
// declared locally (rather than importing internal/routing) so the facts
// package keeps its existing dependency direction: internal/routing depends
// on nothing tier-specific, and facts depends on routing only through this
// narrow, structurally-typed interface.
type resourceRouter interface {
	// Resource returns the physical resource identifier for tenant's data
	// under the logical resource base (see internal/routing.Router.Resource).
	Resource(base, tenant string) string
}

// StoreFactory opens a Store bound to a physical resource name (an rqlite
// table, a SQLite file/DSN, ...). RoutingStore calls it at most once per
// distinct resource name.
type StoreFactory func(resource string) (Store, error)

// RoutingStore implements Store by dispatching every scoped operation to the
// physical Store for that tenant's tier (ADR-0007, "hybrid/tiered"):
//
//   - Shared tenants (the default) are pooled onto one Store, keyed by the
//     unchanged base resource name.
//   - A Dedicated tenant gets its own Store, keyed by
//     router.Resource(base, tenant) (e.g. "base_tenant"), opened lazily on
//     first use and reused afterward.
//
// Provisioning is real: the physical resource (a file, a table) is opened
// and its schema created via factory the first time RoutingStore needs it —
// there is no placeholder or stub path. RoutingStore only decides *which*
// physical Store a tenant's operation lands on; it does not replace the
// (tenant_id, user_id) filter every constitutional Store implementation
// applies internally. Routing is physical placement; the per-statement scope
// filter is isolation. A Dedicated tenant's isolation is physical *in
// addition to*, not instead of, that filter.
type RoutingStore struct {
	router  resourceRouter
	base    string
	factory StoreFactory

	mu     sync.Mutex
	stores map[string]Store
}

// NewRoutingStore builds a RoutingStore over router and base, provisioning
// the shared pool (base) plus every tenant in dedicated up front — so
// ExportAll (the cross-tenant sleep-cycle read) covers a Dedicated tenant's
// data even before that tenant's first request. If any provisioning step
// fails, every store already opened in this call is closed and the error is
// returned; no partially-constructed RoutingStore is handed back.
func NewRoutingStore(router resourceRouter, base string, dedicated []string, factory StoreFactory) (*RoutingStore, error) {
	rs := &RoutingStore{
		router:  router,
		base:    base,
		factory: factory,
		stores:  make(map[string]Store),
	}

	rs.mu.Lock()
	defer rs.mu.Unlock()

	if _, err := rs.provision(base); err != nil {
		rs.closeAllLocked()
		return nil, err
	}
	for _, tenant := range dedicated {
		resource := router.Resource(base, tenant)
		if _, err := rs.provision(resource); err != nil {
			rs.closeAllLocked()
			return nil, err
		}
	}

	return rs, nil
}

// provision returns the cached Store for resource, opening it via rs.factory
// and caching it if this is the first request for that resource. Callers
// MUST hold rs.mu; provision never locks or unlocks it itself.
func (rs *RoutingStore) provision(resource string) (Store, error) {
	if s, ok := rs.stores[resource]; ok {
		return s, nil
	}
	s, err := rs.factory(resource)
	if err != nil {
		return nil, err
	}
	rs.stores[resource] = s
	return s, nil
}

// closeAllLocked closes every store provisioned so far, ignoring individual
// close errors (best-effort unwind on a failed NewRoutingStore). Callers MUST
// hold rs.mu.
func (rs *RoutingStore) closeAllLocked() {
	for resource, s := range rs.stores {
		_ = s.Close()
		delete(rs.stores, resource)
	}
}

// storeFor resolves tenant's physical resource and returns its Store,
// provisioning it lazily (and caching it) if this is the tenant's first
// operation. The mutex is held only for the resolve-and-provision step, never
// across the store operation the caller performs afterward.
func (rs *RoutingStore) storeFor(tenant string) (Store, error) {
	resource := rs.router.Resource(rs.base, tenant)

	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.provision(resource)
}

// Save dispatches to the Store for sc.TenantID's tier.
func (rs *RoutingStore) Save(ctx context.Context, sc Scope, f Fact) error {
	s, err := rs.storeFor(sc.TenantID)
	if err != nil {
		return err
	}
	return s.Save(ctx, sc, f)
}

// Search dispatches to the Store for sc.TenantID's tier.
func (rs *RoutingStore) Search(ctx context.Context, sc Scope, query string, limit int) ([]ScoredFact, error) {
	s, err := rs.storeFor(sc.TenantID)
	if err != nil {
		return nil, err
	}
	return s.Search(ctx, sc, query, limit)
}

// Supersede dispatches to the Store for sc.TenantID's tier.
func (rs *RoutingStore) Supersede(ctx context.Context, sc Scope, oldID string, newFact Fact) error {
	s, err := rs.storeFor(sc.TenantID)
	if err != nil {
		return err
	}
	return s.Supersede(ctx, sc, oldID, newFact)
}

// Delete dispatches to the Store for sc.TenantID's tier.
func (rs *RoutingStore) Delete(ctx context.Context, sc Scope, ids []string) error {
	s, err := rs.storeFor(sc.TenantID)
	if err != nil {
		return err
	}
	return s.Delete(ctx, sc, ids)
}

// ExportAll unions ExportAll across every physical Store provisioned so far
// (the shared pool plus every Dedicated tenant seen). It snapshots the set of
// provisioned stores under rs.mu, then runs each ExportAll unlocked, so a
// concurrent Save/Search is never blocked by this cross-tenant sleep-cycle
// read. The first error encountered is returned; results already collected
// are discarded, matching Store.ExportAll's all-or-nothing contract.
func (rs *RoutingStore) ExportAll(ctx context.Context) ([]Fact, error) {
	rs.mu.Lock()
	snapshot := make([]Store, 0, len(rs.stores))
	for _, s := range rs.stores {
		snapshot = append(snapshot, s)
	}
	rs.mu.Unlock()

	var all []Fact
	for _, s := range snapshot {
		facts, err := s.ExportAll(ctx)
		if err != nil {
			return nil, err
		}
		all = append(all, facts...)
	}
	return all, nil
}

// Close closes every physical Store provisioned so far, returning the first
// error encountered (after attempting to close the rest).
func (rs *RoutingStore) Close() error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	var first error
	for _, s := range rs.stores {
		if err := s.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
