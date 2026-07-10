package semantic

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
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

// provisioned lazily opens exactly one physical Store for a resource. It
// serializes concurrent first-opens of the SAME resource on its OWN mutex —
// never the RoutingStore map lock — so a slow (networked) open for one
// Dedicated tenant's collection never blocks another tenant's dispatch (the map
// lock is held only for the O(1) holder lookup), and it synchronizes that open
// against Close so a concurrent shutdown can neither race the store field nor
// leak a collection opened after Close.
type provisioned struct {
	mu     sync.Mutex
	done   bool // factory has run (store/err are set)
	closed bool // shut() ran; open() must fail closed
	store  Store
	err    error
}

// open returns the holder's Store, running factory exactly once. Concurrent
// callers for the same resource block on p.mu (not the map lock); the loser
// observes done and returns the cached result. Returns ErrRoutingStoreClosed if
// the holder was already shut, so a provision racing Close never opens a store
// that Close will not close.
func (p *provisioned) open(resource string, factory StoreFactory) (Store, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, ErrRoutingStoreClosed
	}
	if !p.done {
		p.store, p.err = factory(resource)
		p.done = true
	}
	return p.store, p.err
}

// shut marks the holder closed and closes its Store if one was opened. It is
// serialized against open on p.mu: whichever of open/shut acquires p.mu first
// wins deterministically — if open ran first, shut closes the opened store; if
// shut ran first, open returns ErrRoutingStoreClosed and never opens. Idempotent.
func (p *provisioned) shut() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.done && p.store != nil {
		s := p.store
		p.store = nil // prevent a double Close on a repeated shut
		return s.Close()
	}
	return nil
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
// once (outside the map lock, via the holder).
func (rs *RoutingStore) storeForResource(resource string) (Store, error) {
	p, err := rs.entryFor(resource)
	if err != nil {
		return nil, err
	}
	return p.open(resource, rs.factory)
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

// Upsert dispatches to the physical Store for sc's tenant. See Store.Upsert.
// The mutex is only held long enough to resolve/provision that Store inside
// storeFor; it is never held across the underlying Upsert call.
func (rs *RoutingStore) Upsert(ctx context.Context, sc Scope, points []Point) error {
	st, err := rs.storeFor(sc.Tenant())
	if err != nil {
		return err
	}
	return st.Upsert(ctx, sc, points)
}

// Search dispatches to the physical Store for sc's tenant. See Store.Search.
// The mutex is only held long enough to resolve/provision that Store inside
// storeFor; it is never held across the underlying Search call.
func (rs *RoutingStore) Search(ctx context.Context, sc Scope, query []float32, limit int, kinds ...Kind) ([]ScoredPoint, error) {
	st, err := rs.storeFor(sc.Tenant())
	if err != nil {
		return nil, err
	}
	return st.Search(ctx, sc, query, limit, kinds...)
}

// Delete dispatches to the physical Store for sc's tenant. See Store.Delete.
// The mutex is only held long enough to resolve/provision that Store inside
// storeFor; it is never held across the underlying Delete call.
func (rs *RoutingStore) Delete(ctx context.Context, sc Scope, ids []string) error {
	st, err := rs.storeFor(sc.Tenant())
	if err != nil {
		return err
	}
	return st.Delete(ctx, sc, ids)
}

// DeleteBySource dispatches to the physical Store for sc's tenant. See
// Store.DeleteBySource. The mutex is only held long enough to
// resolve/provision that Store inside storeFor; it is never held across the
// underlying DeleteBySource call.
func (rs *RoutingStore) DeleteBySource(ctx context.Context, sc Scope, kind Kind, source string) (int, error) {
	st, err := rs.storeFor(sc.Tenant())
	if err != nil {
		return 0, err
	}
	return st.DeleteBySource(ctx, sc, kind, source)
}

// PruneTurns dispatches to the physical Store for sc's tenant. See
// Store.PruneTurns. The mutex is only held long enough to resolve/provision
// that Store inside storeFor; it is never held across the underlying
// PruneTurns call.
func (rs *RoutingStore) PruneTurns(ctx context.Context, sc Scope, maxAge time.Duration, maxCount int) (int, error) {
	st, err := rs.storeFor(sc.Tenant())
	if err != nil {
		return 0, err
	}
	return st.PruneTurns(ctx, sc, maxAge, maxCount)
}

// Close closes every provisioned physical Store (the shared pool plus every
// Dedicated tenant's own store) and marks the store closed, so later dispatches
// fail with ErrRoutingStoreClosed instead of reaching a shut-down backend. It
// is safe to call concurrently with in-flight dispatches: each holder's shut()
// is serialized against its open() on the holder mutex, so a provision racing
// Close either completes and is then closed, or observes the closed holder and
// never opens. The map lock is released before closing the physical stores, so
// a slow backend Close never blocks dispatch resolution. Returns the first
// close error encountered, if any, after attempting to close all of them.
func (rs *RoutingStore) Close() error {
	rs.mu.Lock()
	rs.closed = true
	holders := make([]*provisioned, 0, len(rs.stores))
	for resource, p := range rs.stores {
		holders = append(holders, p)
		delete(rs.stores, resource)
	}
	rs.mu.Unlock()

	var first error
	for _, p := range holders {
		if err := p.shut(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
