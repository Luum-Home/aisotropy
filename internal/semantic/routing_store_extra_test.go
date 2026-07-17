package semantic

import (
	"context"
	"errors"
	"testing"
)

// closeErrStore wraps InMemoryStore so Close reports a chosen error, exercising
// RoutingStore.Close's first-error-wins aggregation over its provisioned stores.
type closeErrStore struct {
	*InMemoryStore
	closeErr error
}

func (s closeErrStore) Close() error { return s.closeErr }

// TestRoutingStoreProvisionFactoryErrorIsWrapped verifies that a factory error
// while lazily provisioning a not-yet-open resource (here, a dedicated tenant
// addressed after construction) is surfaced as a wrapped error from the
// dispatched operation, not ErrRoutingStoreClosed.
func TestRoutingStoreProvisionFactoryErrorIsWrapped(t *testing.T) {
	sentinel := errors.New("factory boom")
	router := newFakeRouter("dedicated-later")

	// The base resource opens fine; only the dedicated resource fails, and it is
	// provisioned lazily on first dispatch (not eagerly, since it is not in the
	// dedicated pre-provision list).
	factory := func(resource string) (Store, error) {
		if resource == "knowledge_dedicated-later" {
			return nil, sentinel
		}
		return NewInMemoryStore(4), nil
	}
	rs, err := NewRoutingStore(router, "knowledge", nil, factory)
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}
	defer func() { _ = rs.Close() }()

	sc := Scope{tenant: "dedicated-later", user: "u"}
	p := Point{ID: "p1", TenantID: "dedicated-later", UserID: "u", Vector: []float32{1, 0, 0, 0}}
	err = rs.Upsert(context.Background(), sc, []Point{p})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Upsert err = %v, want a wrap of the factory error", err)
	}
	if errors.Is(err, ErrRoutingStoreClosed) {
		t.Fatal("factory error must not masquerade as ErrRoutingStoreClosed")
	}
}

// TestRoutingStoreCloseReturnsFirstBackendCloseError verifies Close attempts to
// close every provisioned store and returns the first close error rather than
// swallowing it.
func TestRoutingStoreCloseReturnsFirstBackendCloseError(t *testing.T) {
	sentinel := errors.New("backend close boom")
	router := newFakeRouter()

	factory := func(_ string) (Store, error) {
		return closeErrStore{InMemoryStore: NewInMemoryStore(4), closeErr: sentinel}, nil
	}
	rs, err := NewRoutingStore(router, "knowledge", nil, factory)
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}

	if err := rs.Close(); !errors.Is(err, sentinel) {
		t.Fatalf("Close err = %v, want the backend close error", err)
	}
}
