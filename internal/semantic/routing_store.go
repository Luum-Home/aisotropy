package semantic

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrRoutingStoreClosed is returned by a RoutingStore operation issued after Close.
var ErrRoutingStoreClosed = errors.New("semantic: routing store is closed")

// resourceRouter is the seam onto internal/routing.Router that RoutingStore
// depends on. It resolves the physical resource (a Qdrant collection name,
// or an equivalent identifier for another backend) a tenant's vectors live
// in, given the logical base resource name. RoutingStore depends on this
// narrow interface — rather than importing *routing.Router directly — so it
// can be exercised with a lightweight fake in tests.
type resourceRouter interface {
	Resource(base, tenant string) string
}

// StoreFactory opens a Store bound to a single physical resource (e.g. a
// Qdrant collection name). It is called at most once per distinct resource
// name a RoutingStore encounters — once for the pooled Shared resource, and
// once per Dedicated tenant's own resource — and the result is cached for
// the RoutingStore's lifetime.
type StoreFactory func(resource string) (Store, error)

// provisioned lazily opens exactly one physical Store for a resource. The
// sync.Once serializes concurrent first-provisions of the SAME resource WITHOUT
// holding the RoutingStore's map lock, so a slow (networked) open for one
// Dedicated tenant's collection never blocks another tenant's dispatch — the
// map lock is held only for the O(1) holder lookup.
type provisioned struct {
	once  sync.Once
	store Store
	err   error
}

// RoutingStore implements Store by dispatching every scoped operation to the
// physical Store for that operation's tenant, per ADR-0007's hybrid/tiered
// multi-tenancy decision (docs/decisions/0007-multitenancy-fisico-db-unica-
// vs-por-tenant.md, "Recommended decision", Option C) applied to the
// semantic tier: Shared-tier tenants all share one pooled Store (the base
// resource, e.g. the shared Qdrant collection), while a Dedicated tenant
// gets its own Store (its own collection), provisioned lazily on first use
// and reused afterward.
//
// Provisioning is real, not a placeholder: constructing a Dedicated tenant's
// Store via StoreFactory actually creates its backing resource (for Qdrant,
// its collection), so a routed Dedicated tenant is fully usable the moment
// it is first addressed — there is no broken/stub path.
//
// Routing only decides physical placement. It does not replace the
// (tenant_id, user_id) filter every Store implementation applies during its
// own scan (see the package doc and .claude/rules/constitutional-gates.md):
// a Dedicated tenant's isolation is physical placement IN ADDITION TO that
// per-query filter, never instead of it — the filter remains
// defense-in-depth even though a Dedicated tenant's collection structurally
// holds only that tenant's vectors.
type RoutingStore struct {
	router  resourceRouter
	base    string
	factory StoreFactory

	mu     sync.Mutex
	stores map[string]*provisioned // keyed by physical resource name
	closed bool
}

// NewRoutingStore builds a RoutingStore and eagerly provisions the pooled
// Shared resource (base) plus one dedicated resource for each tenant in
// dedicated (typically the same tenant list backing the routing.Policy, so
// known Dedicated tenants are ready before their first request rather than
// paying a first-request provisioning cost). Empty tenant IDs in dedicated
// are ignored.
//
// If any provisioning step fails, every store already opened during this
// call is closed before the error is returned: NewRoutingStore never leaks
// partially-opened stores.
func NewRoutingStore(router resourceRouter, base string, dedicated []string, factory StoreFactory) (*RoutingStore, error) {
	rs := &RoutingStore{
		router:  router,
		base:    base,
		factory: factory,
		stores:  make(map[string]*provisioned),
	}

	if _, err := rs.storeForResource(base); err != nil {
		_ = rs.Close()
		return nil, fmt.Errorf("semantic: provisioning shared resource %q: %w", base, err)
	}
	for _, tenant := range dedicated {
		if tenant == "" {
			continue
		}
		resource := router.Resource(base, tenant)
		if _, err := rs.storeForResource(resource); err != nil {
			_ = rs.Close()
			return nil, fmt.Errorf("semantic: provisioning dedicated resource %q for tenant %q: %w", resource, tenant, err)
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

// storeForResource returns the cached Store for resource, opening it exactly
// once (outside the map lock, via the holder's sync.Once).
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

// storeFor resolves the physical Store the given tenant's operations must
// dispatch to, provisioning it lazily (and caching it) on first use. This
// covers a Dedicated tenant added to the routing Policy after
// NewRoutingStore ran, without requiring a restart.
func (rs *RoutingStore) storeFor(tenant string) (Store, error) {
	resource := rs.router.Resource(rs.base, tenant)
	st, err := rs.storeForResource(resource)
	if err != nil {
		if errors.Is(err, ErrRoutingStoreClosed) {
			return nil, err
		}
		return nil, fmt.Errorf("semantic: provisioning resource %q for tenant %q: %w", resource, tenant, err)
	}
	return st, nil
}

// Upsert dispatches to the physical Store for sc.TenantID. See Store.Upsert.
// The mutex is only held long enough to resolve/provision that Store inside
// storeFor; it is never held across the underlying Upsert call.
func (rs *RoutingStore) Upsert(ctx context.Context, sc Scope, points []Point) error {
	st, err := rs.storeFor(sc.TenantID)
	if err != nil {
		return err
	}
	return st.Upsert(ctx, sc, points)
}

// Search dispatches to the physical Store for sc.TenantID. See Store.Search.
// The mutex is only held long enough to resolve/provision that Store inside
// storeFor; it is never held across the underlying Search call.
func (rs *RoutingStore) Search(ctx context.Context, sc Scope, query []float32, limit int) ([]ScoredPoint, error) {
	st, err := rs.storeFor(sc.TenantID)
	if err != nil {
		return nil, err
	}
	return st.Search(ctx, sc, query, limit)
}

// Close closes every provisioned physical Store (the shared pool plus every
// Dedicated tenant's own store) and marks the store closed, so later
// dispatches fail with ErrRoutingStoreClosed instead of reaching a shut-down
// backend. It assumes no operation is concurrently in flight (the standard
// io.Closer contract). Returns the first close error encountered, if any,
// after attempting to close all of them.
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
