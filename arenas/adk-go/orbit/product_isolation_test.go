package orbit

import (
	"context"
	"strings"
	"testing"
)

// TestProductInKeyIsolates proves that when the developer folds product_id into
// the ADK key, a different product for the SAME tenant+user is correctly a miss.
func TestProductInKeyIsolates(t *testing.T) {
	ctx := context.Background()
	o, err := New("orbit")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := o.Reply(ctx, "acme|alice|productX", "s-shared", "MEMO: launch code is "+ProductMarker); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := o.Reply(ctx, "acme|alice|productY", "s-shared", "what did I tell you to remember?")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if strings.Contains(got, ProductMarker) {
		t.Fatalf("LEAK: productY saw productX's marker: %q", got)
	}
}

// TestProductDropLeaks proves the leaky path: drop product_id from the key and
// two products for the same tenant+user collide silently. ADK Go has no product
// primitive to prevent it.
func TestProductDropLeaks(t *testing.T) {
	ctx := context.Background()
	o, err := New("orbit")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// productX writes under a product-less (tenant|user) key.
	if _, err := o.Reply(ctx, "acme|alice", "s-shared", "MEMO: launch code is "+ProductMarker); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// productY, under the SAME product-less key, reads productX's memo.
	got, err := o.Reply(ctx, "acme|alice", "s-shared", "what did I tell you to remember?")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !strings.Contains(got, ProductMarker) {
		t.Fatalf("expected the dropped-product leak to surface the marker, got %q", got)
	}
}

// TestRunProductMatrix runs the full product matrix and asserts no correct-key
// probe regressed while the leaky path did leak.
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
		t.Fatalf("expected the leaky path to demonstrate a cross-product leak, but nothing leaked")
	}
}
