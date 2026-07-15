package gateway

import (
	"context"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// fakeClient is a minimal llm.Client for registry-resolution tests. It never
// streams anything real — Name() is enough to assert which client was chosen.
type fakeClient struct{ name string }

func (f fakeClient) StreamChat(context.Context, string, string) (<-chan llm.Token, error) {
	ch := make(chan llm.Token)
	close(ch)
	return ch, nil
}
func (f fakeClient) Name() string { return f.name }

// staticPolicy always returns a fixed Decision — lets us drive Route without
// depending on the reference policies.
type staticPolicy struct{ d Decision }

func (s staticPolicy) Select(context.Context, Request) Decision { return s.d }

func newTestGateway(t *testing.T, policy Policy) *Gateway {
	t.Helper()
	g := New(policy, nil)
	if err := g.Register("cheap", fakeClient{name: "cheap-client"}); err != nil {
		t.Fatalf("register cheap: %v", err)
	}
	if err := g.Register("premium", fakeClient{name: "premium-client"}); err != nil {
		t.Fatalf("register premium: %v", err)
	}
	if err := g.SetDefault("cheap"); err != nil {
		t.Fatalf("set default: %v", err)
	}
	return g
}

func TestRegisterValidation(t *testing.T) {
	g := New(nil, nil)
	if err := g.Register("", fakeClient{name: "x"}); err == nil {
		t.Error("expected error registering under empty name")
	}
	if err := g.Register("x", nil); err == nil {
		t.Error("expected error registering nil client")
	}
}

func TestSetDefaultUnregistered(t *testing.T) {
	g := New(nil, nil)
	if err := g.SetDefault("nope"); err == nil {
		t.Error("expected error setting default to unregistered target")
	}
}

func TestFirstRegisteredBecomesDefault(t *testing.T) {
	g := New(nil, nil)
	if err := g.Register("first", fakeClient{name: "first-client"}); err != nil {
		t.Fatal(err)
	}
	if err := g.Register("second", fakeClient{name: "second-client"}); err != nil {
		t.Fatal(err)
	}
	// An empty/unknown target must resolve to the first-registered default.
	c, exact := g.Client("")
	if exact {
		t.Error("empty target should not be an exact match")
	}
	if c == nil || c.Name() != "first-client" {
		t.Errorf("expected first-client as default, got %v", c)
	}
}

func TestClientResolution(t *testing.T) {
	g := newTestGateway(t, nil)

	tests := []struct {
		name      string
		target    string
		wantName  string
		wantExact bool
	}{
		{"known cheap", "cheap", "cheap-client", true},
		{"known premium", "premium", "premium-client", true},
		{"unknown falls back to default", "does-not-exist", "cheap-client", false},
		{"empty falls back to default", "", "cheap-client", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, exact := g.Client(tt.target)
			if c == nil {
				t.Fatalf("got nil client for %q", tt.target)
			}
			if c.Name() != tt.wantName {
				t.Errorf("target %q: got %q, want %q", tt.target, c.Name(), tt.wantName)
			}
			if exact != tt.wantExact {
				t.Errorf("target %q: exact=%v, want %v", tt.target, exact, tt.wantExact)
			}
		})
	}
}

func TestClientNoDefault(t *testing.T) {
	g := New(nil, nil)
	// No clients registered, no default: unresolvable.
	c, ok := g.Client("anything")
	if c != nil || ok {
		t.Errorf("expected (nil,false) with empty registry, got (%v,%v)", c, ok)
	}
}

func TestRouteHonorsPolicyAndResolves(t *testing.T) {
	g := newTestGateway(t, staticPolicy{d: Decision{Target: "premium", Reason: "test"}})
	c, d, err := g.Route(context.Background(), Request{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Name() != "premium-client" {
		t.Errorf("got %q, want premium-client", c.Name())
	}
	if d.Target != "premium" || d.Reason != "test" {
		t.Errorf("decision not preserved: %+v", d)
	}
}

func TestRouteUnknownTargetFallsBackToDefault(t *testing.T) {
	// Policy points at a target the gateway doesn't have -> fail-safe default,
	// but the original Decision (intent) is preserved for audit.
	g := newTestGateway(t, staticPolicy{d: Decision{Target: "ghost", Reason: "intent"}})
	c, d, err := g.Route(context.Background(), Request{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Name() != "cheap-client" {
		t.Errorf("expected fallback to cheap-client, got %q", c.Name())
	}
	if d.Target != "ghost" || d.Reason != "intent" {
		t.Errorf("expected original decision preserved, got %+v", d)
	}
}

func TestRouteNilPolicyUsesDefault(t *testing.T) {
	g := newTestGateway(t, nil)
	c, _, err := g.Route(context.Background(), Request{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Name() != "cheap-client" {
		t.Errorf("nil policy should use default, got %q", c.Name())
	}
}

func TestRouteNoDefaultErrors(t *testing.T) {
	g := New(staticPolicy{d: Decision{Target: "x"}}, nil)
	// No clients registered at all.
	c, _, err := g.Route(context.Background(), Request{})
	if err == nil {
		t.Error("expected error when nothing is registered")
	}
	if c != nil {
		t.Errorf("expected nil client on hard miss, got %v", c)
	}
}

func TestTargets(t *testing.T) {
	g := newTestGateway(t, nil)
	got := g.Targets()
	want := []string{"cheap", "premium"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Targets()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
