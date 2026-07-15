package orbit

import (
	"context"
	"strings"
	"testing"
)

// TestOrbitEndToEnd proves the real ADK Go runner + fake model produce a reply
// and that same-session memory recall works.
func TestOrbitEndToEnd(t *testing.T) {
	ctx := context.Background()
	o, err := New("orbit")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := o.Reply(ctx, "acme|alice", "s1", "MEMO: launch code is "+Marker); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := o.Reply(ctx, "acme|alice", "s1", "what did I tell you to remember?")
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !strings.Contains(got, Marker) {
		t.Fatalf("expected recalled marker in reply, got %q", got)
	}
}

// TestCompositeKeyIsolates proves ADK Go's session.Service isolates when the
// developer folds tenant into the key (the correct path).
func TestCompositeKeyIsolates(t *testing.T) {
	ctx := context.Background()
	o, err := New("orbit")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := o.Reply(ctx, "acme|alice", "s-shared", "MEMO: launch code is "+Marker); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, id := range []string{"globex|bob", "acme|bob", "globex|alice"} {
		got, err := o.Reply(ctx, id, "s-shared", "what did I tell you to remember?")
		if err != nil {
			t.Fatalf("probe %s: %v", id, err)
		}
		if strings.Contains(got, Marker) {
			t.Fatalf("LEAK: %s saw acme's marker: %q", id, got)
		}
	}
}

// TestTenantDropLeaks proves the leaky path: drop the tenant component from the
// developer-passed key and two tenants collide silently. ADK Go has no tenant
// binding to prevent this — it is the whole point of the arena.
func TestTenantDropLeaks(t *testing.T) {
	ctx := context.Background()
	o, err := New("orbit")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// acme's alice writes under a buggy bare-username key.
	if _, err := o.Reply(ctx, "alice", "s-shared", "MEMO: launch code is "+Marker); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// globex's alice, under the SAME buggy key, reads acme's memo.
	got, err := o.Reply(ctx, "alice", "s-shared", "what did I tell you to remember?")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !strings.Contains(got, Marker) {
		t.Fatalf("expected the dropped-tenant leak to surface the marker, got %q", got)
	}
}

// TestRunMatrix runs the full adversarial matrix and asserts no correct-key
// probe regressed (Failed == false) while the leaky-path demos did leak.
func TestRunMatrix(t *testing.T) {
	rep, err := RunMatrix(context.Background())
	if err != nil {
		t.Fatalf("RunMatrix: %v", err)
	}
	if rep.Failed {
		for _, r := range rep.Results {
			if r.Fatal || (r.Leaked && r.MustIsolate) {
				t.Errorf("regression: %s -> %s", r.Label, r.Detail)
			}
		}
	}
	if !rep.Leaked {
		t.Fatalf("expected the leaky path to demonstrate a leak, but nothing leaked")
	}
}
