package orbit

import (
	"context"
	"strings"
	"testing"
)

// TestProductAppNameIsolatesByConstruction proves the STRONGEST ADK config for
// the product axis: one runner per product with appName=product. Because ADK
// binds appName at runner construction (not per request), a second product's
// runner reading the SAME userID + sessionID is a storage miss by construction —
// the product axis cannot be "dropped" per request.
func TestProductAppNameIsolatesByConstruction(t *testing.T) {
	ctx := context.Background()
	runnerX, err := New("productX")
	if err != nil {
		t.Fatalf("New(productX): %v", err)
	}
	runnerY, err := New("productY")
	if err != nil {
		t.Fatalf("New(productY): %v", err)
	}
	if _, err := runnerX.Reply(ctx, "acme|alice", "s1", "MEMO: launch code is "+ProductMarker); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := runnerY.Reply(ctx, "acme|alice", "s1", "what did I tell you to remember?")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if strings.Contains(got, ProductMarker) {
		t.Fatalf("LEAK: productY runner saw productX's marker despite distinct appName: %q", got)
	}
}

// TestResidualAxisDropLeaks proves the honest residual: with appName spent on
// product, tenant has no native slot and rides in userID. Drop it and two
// tenants collide on the same session — a genuine same-session read. This is a
// chosen-weak-config collision, not an ADK inability to isolate.
func TestResidualAxisDropLeaks(t *testing.T) {
	ctx := context.Background()
	o, err := New("productX")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// acme's alice writes under a bare (tenant-dropped) userID.
	if _, err := o.Reply(ctx, "alice", "s-shared", "MEMO: launch code is "+ProductMarker); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// globex's alice, under the SAME bare userID, reads acme's memo.
	got, err := o.Reply(ctx, "alice", "s-shared", "what did I tell you to remember?")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !strings.Contains(got, ProductMarker) {
		t.Fatalf("expected the dropped residual-axis leak to surface the marker, got %q", got)
	}
}

// TestRunProductMatrix runs the full product matrix and asserts no correct-key
// probe regressed while the residual leaky path did leak.
func TestRunProductMatrix(t *testing.T) {
	rep, err := RunProductMatrix(context.Background())
	if err != nil {
		t.Fatalf("RunProductMatrix: %v", err)
	}
	if rep.Failed {
		for _, r := range rep.Results {
			if r.Fatal || (r.Leaked && r.MustIsolate) {
				t.Errorf("regression: %s -> %s", r.Label, r.Detail)
			}
		}
	}
	if !rep.Leaked {
		t.Fatalf("expected the residual leaky path to demonstrate a leak, but nothing leaked")
	}
}
