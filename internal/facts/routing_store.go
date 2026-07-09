package facts

import (
	"context"
	"errors"
	"sort"
	"sync"
)

// ErrRoutingStoreClosed is returned by a RoutingStore operation issued after Close.
var ErrRoutingStoreClosed = errors.New("facts: routing store is closed")

// resourceRouter is the seam internal/routing.Router satisfies. It is declared
// locally (rather than importing internal/routing) so the facts package keeps
// its dependency direction: facts depends on routing only through this narrow,
// structurally-typed interface.
type resourceRouter interface {
	// Resource returns the physical resource identifier for tenant's data under
	// the logical base (see internal/routing.Router.Resource).
	Resource(base, tenant string) string
}

// StoreFactory opens a Store bound to a physical resource name (an rqlite
// table, a SQLite file/DSN, ...). RoutingStore calls it at most once per
// distinct resource name.
type StoreFactory func(resource string) (Store, error)

// provisioned lazily opens exactly one physical Store for a resource. The
// sync.Once serializes concurrent first-provisions of the SAME resource WITHOUT
// holding the RoutingStore's map lock, so a slow (networked) open for one
// tenant never blocks another tenant's dispatch — the map lock is held only for
// the O(1) holder lookup.
type provisioned struct {
	once  sync.Once
	store Store
	err   error
}

// RoutingStore implements Store by dispatching every scoped operation to the
// physical Store for that tenant's tier (ADR-0007, "hybrid/tiered"):
//
//   - Shared tenants (the default) are pooled onto one Store, keyed by the
//     unchanged base resource name.
//   - A Dedicated tenant gets its own Store, keyed by router.Resource(base,
//     tenant) (e.g. "base_tenant"), opened lazily on first use and reused.
//
// Provisioning is real: the physical resource (a file, a table) is opened and
// its schema created via factory the first time it is needed. RoutingStore only
// decides WHICH physical Store an operation lands on; it does not replace the
// (tenant_id, user_id) filter every Store applies internally. Routing is
// physical placement; the per-statement scope filter is isolation. A Dedicated
// tenant's isolation is physical IN ADDITION TO, not instead of, that filter.
type RoutingStore struct {
	router  resourceRouter
	base    string
	factory StoreFactory

	mu     sync.Mutex
	stores map[string]*provisioned
	closed bool
}

// NewRoutingStore builds a RoutingStore over router and base, provisioning the
// shared pool (base) plus every tenant in dedicated up front — so ExportAll (the
// cross-tenant sleep-cycle read) covers a Dedicated tenant's data even before
// that tenant's first request. Passing the COMPLETE dedicated set is what makes
// ExportAll complete; RoutingStore only sees the narrow Resource() seam and
// cannot enumerate dedicated tenants itself. On any provisioning failure every
// already-opened store is closed and the error is returned.
func NewRoutingStore(router resourceRouter, base string, dedicated []string, factory StoreFactory) (*RoutingStore, error) {
	rs := &RoutingStore{
		router:  router,
		base:    base,
		factory: factory,
		stores:  make(map[string]*provisioned),
	}
	toProvision := make([]string, 0, len(dedicated)+1)
	toProvision = append(toProvision, base)
	for _, tenant := range dedicated {
		toProvision = append(toProvision, router.Resource(base, tenant))
	}
	for _, resource := range toProvision {
		if _, err := rs.storeForResource(resource); err != nil {
			_ = rs.Close()
			return nil, err
		}
	}
	return rs, nil
}

// entryFor returns (creating if needed) the holder for resource. The map lock is
// held only for this O(1) lookup — never across the factory call. Returns
// ErrRoutingStoreClosed after Close.
func (rs *RoutingStore) entryFor(resource string) (*provisioned, error) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.closed {
		return nil, ErrRoutingStoreClosed
	}
	p, ok := rs.stores[resource]
	if !ok {
		p = &provisioned{}
		rs.stores[resource] = p
	}
	return p, nil
}

// storeForResource returns the Store for resource, opening it exactly once
// (outside the map lock, via the holder's sync.Once).
func (rs *RoutingStore) storeForResource(resource string) (Store, error) {
	p, err := rs.entryFor(resource)
	if err != nil {
		return nil, err
	}
	p.once.Do(func() { p.store, p.err = rs.factory(resource) })
	if p.err != nil {
		return nil, p.err
	}
	return p.store, nil
}

// storeFor resolves tenant's physical resource and returns its Store.
func (rs *RoutingStore) storeFor(tenant string) (Store, error) {
	return rs.storeForResource(rs.router.Resource(rs.base, tenant))
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

// ExportAll unions ExportAll across every physical Store provisioned so far (the
// shared pool plus every Dedicated tenant), then restores Store.ExportAll's
// "ordered by id" contract across the union. It snapshots the resource names
// under the lock and re-fetches each Store via storeForResource (whose sync.Once
// gives the happens-before that makes reading the opened store race-free), so a
// concurrent Save/Search is never blocked by this sleep-cycle read.
func (rs *RoutingStore) ExportAll(ctx context.Context) ([]Fact, error) {
	rs.mu.Lock()
	if rs.closed {
		rs.mu.Unlock()
		return nil, ErrRoutingStoreClosed
	}
	resources := make([]string, 0, len(rs.stores))
	for resource := range rs.stores {
		resources = append(resources, resource)
	}
	rs.mu.Unlock()

	var all []Fact
	for _, resource := range resources {
		s, err := rs.storeForResource(resource)
		if err != nil {
			return nil, err
		}
		fs, err := s.ExportAll(ctx)
		if err != nil {
			return nil, err
		}
		all = append(all, fs...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	return all, nil
}

// Close closes every physical Store provisioned so far and marks the store
// closed, so later dispatches fail with ErrRoutingStoreClosed instead of
// reaching a shut-down backend. It assumes no operation is concurrently in
// flight (the standard io.Closer contract). Returns the first close error.
func (rs *RoutingStore) Close() error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	rs.closed = true
	var first error
	for resource, p := range rs.stores {
		if p.store != nil {
			if err := p.store.Close(); err != nil && first == nil {
				first = err
			}
		}
		delete(rs.stores, resource)
	}
	return first
}
