package semantic

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestRoutingStore_MaintenanceDispatch covers the Delete / DeleteBySource /
// PruneTurns dispatch paths, which the existing routing tests (focused on
// Upsert/Search isolation) leave uncovered.
func TestRoutingStore_MaintenanceDispatch(t *testing.T) {
	router := newFakeRouter("tenant-dedicated")
	rs, err := NewRoutingStore(router, "knowledge", []string{"tenant-dedicated"}, inMemoryFactory(3))
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}
	defer func() { _ = rs.Close() }()

	ctx := context.Background()
	sc := Scope{tenant: "tenant-dedicated", user: "u1"}

	pts := []Point{
		{ID: "d1", TenantID: "tenant-dedicated", UserID: "u1", Kind: KindDocument, Source: "doc-1", Vector: vec3(1, 0, 0)},
		{ID: "d2", TenantID: "tenant-dedicated", UserID: "u1", Kind: KindDocument, Source: "doc-1", Vector: vec3(0, 1, 0)},
		{ID: "t1", TenantID: "tenant-dedicated", UserID: "u1", Kind: KindTurn, Vector: vec3(0, 0, 1), CreatedAt: time.Now().Add(-72 * time.Hour)},
	}
	if err := rs.Upsert(ctx, sc, pts); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// DeleteBySource dispatch.
	n, err := rs.DeleteBySource(ctx, sc, KindDocument, "doc-1")
	if err != nil || n != 2 {
		t.Fatalf("DeleteBySource: got %d (%v)", n, err)
	}

	// PruneTurns dispatch (age policy evicts the old turn).
	n, err = rs.PruneTurns(ctx, sc, 24*time.Hour, 0)
	if err != nil || n != 1 {
		t.Fatalf("PruneTurns: got %d (%v)", n, err)
	}

	// Delete dispatch on a fresh point.
	if err := rs.Upsert(ctx, sc, []Point{{ID: "x", TenantID: "tenant-dedicated", UserID: "u1", Kind: KindTurn, Vector: vec3(1, 0, 0)}}); err != nil {
		t.Fatalf("Upsert x: %v", err)
	}
	if err := rs.Delete(ctx, sc, []string{"x"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ := rs.Search(ctx, sc, vec3(1, 0, 0), 10)
	if len(got) != 0 {
		t.Fatalf("expected everything removed, got %d", len(got))
	}
}

// TestRoutingStore_MaintenanceAfterClose covers the error-return branches of the
// maintenance dispatchers when the store is already closed.
func TestRoutingStore_MaintenanceAfterClose(t *testing.T) {
	router := newFakeRouter()
	rs, err := NewRoutingStore(router, "knowledge", nil, inMemoryFactory(3))
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}
	if err := rs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ctx := context.Background()
	sc := Scope{tenant: "t", user: "u"}
	if err := rs.Delete(ctx, sc, []string{"x"}); !errors.Is(err, ErrRoutingStoreClosed) {
		t.Fatalf("Delete after close: got %v", err)
	}
	if _, err := rs.DeleteBySource(ctx, sc, KindDocument, "s"); !errors.Is(err, ErrRoutingStoreClosed) {
		t.Fatalf("DeleteBySource after close: got %v", err)
	}
	if _, err := rs.PruneTurns(ctx, sc, time.Hour, 0); !errors.Is(err, ErrRoutingStoreClosed) {
		t.Fatalf("PruneTurns after close: got %v", err)
	}
}

// TestRoutingStore_ProvisionFailureClosesOpened proves NewRoutingStore returns
// the provisioning error (and closes what it opened) when a dedicated resource
// fails to open.
func TestRoutingStore_ProvisionFailure(t *testing.T) {
	sentinel := errors.New("cannot open resource")
	failing := func(resource string) (Store, error) {
		if resource == "knowledge" {
			return NewInMemoryStore(3), nil // shared pool opens fine
		}
		return nil, sentinel // dedicated resource fails
	}
	router := newFakeRouter("tenant-dedicated")
	_, err := NewRoutingStore(router, "knowledge", []string{"tenant-dedicated"}, failing)
	if err == nil || !errors.Is(err, sentinel) {
		t.Fatalf("expected provisioning error, got %v", err)
	}
}

// TestRoutingStore_SharedProvisionFailure covers the shared-pool open failure
// branch of NewRoutingStore.
func TestRoutingStore_SharedProvisionFailure(t *testing.T) {
	sentinel := errors.New("shared open failed")
	failing := func(string) (Store, error) { return nil, sentinel }
	_, err := NewRoutingStore(newFakeRouter(), "knowledge", nil, failing)
	if err == nil || !errors.Is(err, sentinel) {
		t.Fatalf("expected shared provisioning error, got %v", err)
	}
}

// TestRoutingStore_EmptyDedicatedTenantIgnored covers the `if tenant == ""`
// skip branch in NewRoutingStore.
func TestRoutingStore_EmptyDedicatedTenantIgnored(t *testing.T) {
	rs, err := NewRoutingStore(newFakeRouter(), "knowledge", []string{""}, inMemoryFactory(3))
	if err != nil {
		t.Fatalf("empty dedicated tenant should be ignored, got %v", err)
	}
	_ = rs.Close()
}

// TestRoutingStore_EraseScopeDispatch covers EraseScope dispatching to the
// physical Store for the scope's tenant (InMemoryStore implements
// ScopeEraser), mirroring the Delete/DeleteBySource/PruneTurns dispatch test.
func TestRoutingStore_EraseScopeDispatch(t *testing.T) {
	router := newFakeRouter("tenant-dedicated")
	rs, err := NewRoutingStore(router, "knowledge", []string{"tenant-dedicated"}, inMemoryFactory(3))
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}
	defer func() { _ = rs.Close() }()

	ctx := context.Background()
	sc := Scope{tenant: "tenant-dedicated", user: "u1"}
	other := Scope{tenant: "tenant-dedicated", user: "u2"}
	if err := rs.Upsert(ctx, sc, []Point{{ID: "p1", TenantID: "tenant-dedicated", UserID: "u1", Kind: KindFact, Vector: vec3(1, 0, 0)}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := rs.Upsert(ctx, other, []Point{{ID: "p2", TenantID: "tenant-dedicated", UserID: "u2", Kind: KindFact, Vector: vec3(0, 1, 0)}}); err != nil {
		t.Fatalf("Upsert other: %v", err)
	}

	n, err := rs.EraseScope(ctx, sc)
	if err != nil || n != 1 {
		t.Fatalf("EraseScope: got %d (%v), want (1,nil)", n, err)
	}
	if got, _ := rs.Search(ctx, sc, vec3(1, 0, 0), 10); len(got) != 0 {
		t.Fatalf("erased scope still has %d points", len(got))
	}
	if got, _ := rs.Search(ctx, other, vec3(0, 1, 0), 10); len(got) != 1 {
		t.Fatalf("EraseScope leaked into another user, got %d", len(got))
	}
}

// TestRoutingStore_EraseScopeAfterClose covers EraseScope's error-return
// branch when the routing store is already closed.
func TestRoutingStore_EraseScopeAfterClose(t *testing.T) {
	rs, err := NewRoutingStore(newFakeRouter(), "knowledge", nil, inMemoryFactory(3))
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}
	if err := rs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := rs.EraseScope(context.Background(), Scope{tenant: "t", user: "u"}); !errors.Is(err, ErrRoutingStoreClosed) {
		t.Fatalf("EraseScope after close: got %v", err)
	}
}

// noEraseScopeStore wraps a Store WITHOUT re-exposing any extra method the
// underlying value implements (Go's interface embedding promotes only the
// embedded interface's own method set), so it deliberately does NOT satisfy
// ScopeEraser even when the wrapped concrete Store does. This exercises
// RoutingStore.EraseScope's "does not support subject erasure" error branch,
// which every built-in store bypasses.
type noEraseScopeStore struct{ Store }

// TestRoutingStore_EraseScopeUnsupportedStore covers the branch where the
// resolved physical Store does not implement ScopeEraser: EraseScope must
// fail loudly rather than silently skipping the tier.
func TestRoutingStore_EraseScopeUnsupportedStore(t *testing.T) {
	factory := func(string) (Store, error) { return noEraseScopeStore{Store: NewInMemoryStore(3)}, nil }
	rs, err := NewRoutingStore(newFakeRouter(), "knowledge", nil, factory)
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}
	defer func() { _ = rs.Close() }()

	if _, err := rs.EraseScope(context.Background(), Scope{tenant: "t", user: "u"}); err == nil || !strings.Contains(err.Error(), "does not support subject erasure") {
		t.Fatalf("expected an unsupported-erasure error, got %v", err)
	}
}

// TestProvisionedOpenAfterShut covers provisioned.open's closed guard when
// shut() has already run on the SAME holder — a direct unit test of the
// unexported type, since RoutingStore's own closed check (rs.closed) short-
// circuits before this branch is ever reached through the public API.
func TestProvisionedOpenAfterShut(t *testing.T) {
	p := &provisioned{}
	if err := p.shut(); err != nil {
		t.Fatalf("shut: %v", err)
	}
	if _, err := p.open("resource", inMemoryFactory(3)); !errors.Is(err, ErrRoutingStoreClosed) {
		t.Fatalf("open after shut: got %v", err)
	}
}
