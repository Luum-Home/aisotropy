package aisotropy

import (
	"context"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/gateway"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/routing"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// tagClient is a distinguishable llm.Client whose Name identifies which gateway
// target Route resolved to.
type tagClient struct{ name string }

func (t tagClient) Name() string { return t.name }

func (t tagClient) StreamChat(context.Context, string, string) (<-chan llm.Token, error) {
	ch := make(chan llm.Token)
	close(ch)
	return ch, nil
}

// toCheapPolicy is a consumer gateway.Policy that always routes to the "cheap"
// target, so a test can prove consumer policies are chained FIRST and win.
type toCheapPolicy struct{}

func (toCheapPolicy) Select(context.Context, gateway.Request) gateway.Decision {
	return gateway.Decision{Target: gwCheap, Reason: "test: force cheap"}
}

// TestBuildModelGateway_DefaultRoutesToPrimary verifies the inert baseline: with
// no cheap client, no budget, no vision target, and no consumer policy, every
// request falls through the built-in chain to the primary default.
func TestBuildModelGateway_DefaultRoutesToPrimary(t *testing.T) {
	primary := tagClient{name: "primary-client"}
	gw := buildModelGateway(primary, nil, 0, "", nil)

	got, _, err := gw.Route(context.Background(), gateway.Request{Tenant: "acme"})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if got.Name() != "primary-client" {
		t.Errorf("default route = %q, want primary-client", got.Name())
	}
}

// TestBuildModelGateway_ConsumerPolicyWinsToCheap verifies a consumer policy is
// chained ahead of the built-ins: it names the cheap target and that pick wins.
// This also exercises the cost-budget branch (cheap != nil && budget > 0 adds
// the CostAwarePolicy) and the vision-routing log branch (visionTarget != primary).
func TestBuildModelGateway_ConsumerPolicyWinsToCheap(t *testing.T) {
	primary := tagClient{name: "primary-client"}
	cheap := tagClient{name: "cheap-client"}
	gw := buildModelGateway(primary, cheap, 5.0, gwCheap, []gateway.Policy{toCheapPolicy{}})

	got, _, err := gw.Route(context.Background(), gateway.Request{Tenant: "acme"})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if got.Name() != "cheap-client" {
		t.Errorf("consumer-policy route = %q, want cheap-client (consumer policy chained first must win)", got.Name())
	}
}

// TestBuildModelGateway_RegistrationFailuresAreLoggedNotFatal covers the
// defensive Register/SetDefault error branches: a nil primary client makes its
// own Register call fail (Register rejects a nil client) and, since primary
// never became the registered default, the trailing SetDefault(gwPrimary) also
// fails — buildModelGateway must still return a non-nil, usable Gateway rather
// than propagating either error (there is no error return to propagate to).
func TestBuildModelGateway_RegistrationFailuresAreLoggedNotFatal(t *testing.T) {
	cheap := tagClient{name: "cheap-client"}
	gw := buildModelGateway(nil, cheap, 5.0, "vision-target", nil)
	if gw == nil {
		t.Fatal("buildModelGateway must return a non-nil Gateway even when primary registration fails")
	}
	// cheap registered fine and, since primary never registered, became the
	// gateway's fallback default (the first successful Register wins the
	// default) — a request with no routing signal still resolves to a client.
	got, _, err := gw.Route(context.Background(), gateway.Request{Tenant: "acme"})
	if err != nil {
		t.Fatalf("Route after a primary registration failure: %v", err)
	}
	if got == nil {
		t.Fatal("Route returned a nil client")
	}
}

// TestBuildResponseCache covers both layers: a nil embedder yields an
// exact-only cache; a real embedder adds the semantic (nearest-neighbor) layer.
func TestBuildResponseCache(t *testing.T) {
	if c := buildResponseCache(nil); c == nil {
		t.Fatal("buildResponseCache(nil) must return a non-nil exact-only cache")
	}
	if c := buildResponseCache(semantic.NewHashEmbedder(64)); c == nil {
		t.Fatal("buildResponseCache(embedder) must return a non-nil cache with the semantic layer")
	}
}

// TestNewWorkingMemory covers the in-memory default and the on-disk
// (AISOTROPY_DATA_DIR) branch, plus the nil-summarizer fallback to the built-in
// LLMSummarizer.
func TestNewWorkingMemory(t *testing.T) {
	t.Run("in-memory default", func(t *testing.T) {
		t.Setenv("AISOTROPY_DATA_DIR", "")
		wm, err := newWorkingMemory(llm.NewStubClient(), nil)
		if err != nil {
			t.Fatalf("newWorkingMemory: %v", err)
		}
		if err := wm.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	t.Run("on-disk under data dir", func(t *testing.T) {
		t.Setenv("AISOTROPY_DATA_DIR", t.TempDir())
		wm, err := newWorkingMemory(llm.NewStubClient(), nil)
		if err != nil {
			t.Fatalf("newWorkingMemory (data dir): %v", err)
		}
		if err := wm.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
}

// TestOpenBitrateStore covers both the in-memory default and the on-disk branch.
func TestOpenBitrateStore(t *testing.T) {
	t.Run("in-memory default", func(t *testing.T) {
		t.Setenv("AISOTROPY_DATA_DIR", "")
		s, err := openBitrateStore()
		if err != nil {
			t.Fatalf("openBitrateStore: %v", err)
		}
		_ = s.Close()
	})
	t.Run("on-disk under data dir", func(t *testing.T) {
		t.Setenv("AISOTROPY_DATA_DIR", t.TempDir())
		s, err := openBitrateStore()
		if err != nil {
			t.Fatalf("openBitrateStore (data dir): %v", err)
		}
		_ = s.Close()
	})
}

// TestOpenFactsStore covers the default (no dedicated tenants) and the routing
// (dedicated tenants -> facts.NewRoutingStore) branches on the in-process
// SQLite backend. RQLITE_URL is left unset so no networked service is needed.
func TestOpenFactsStore(t *testing.T) {
	t.Setenv("RQLITE_URL", "")
	t.Setenv("AISOTROPY_DATA_DIR", "")

	t.Run("shared pool (no dedicated tenants)", func(t *testing.T) {
		router := routing.NewRouter(routing.NewStaticPolicy())
		s, err := openFactsStore(router, nil)
		if err != nil {
			t.Fatalf("openFactsStore: %v", err)
		}
		_ = s.Close()
	})
	t.Run("routing store with dedicated tenants", func(t *testing.T) {
		dedicated := []string{"acme"}
		router := routing.NewRouter(routing.NewStaticPolicy(dedicated...))
		s, err := openFactsStore(router, dedicated)
		if err != nil {
			t.Fatalf("openFactsStore (dedicated): %v", err)
		}
		_ = s.Close()
	})
}

// TestOpenSemanticStore covers the default in-memory store and the dedicated
// routing store. QDRANT_HOST is left unset so the in-process cosine store backs
// both branches (no networked Qdrant needed).
func TestOpenSemanticStore(t *testing.T) {
	t.Setenv("QDRANT_HOST", "")
	ctx := context.Background()
	const dim = 64

	t.Run("in-memory default", func(t *testing.T) {
		router := routing.NewRouter(routing.NewStaticPolicy())
		s, err := openSemanticStore(ctx, router, nil, dim)
		if err != nil {
			t.Fatalf("openSemanticStore: %v", err)
		}
		_ = s.Close()
	})
	t.Run("routing store with dedicated tenants", func(t *testing.T) {
		dedicated := []string{"globex"}
		router := routing.NewRouter(routing.NewStaticPolicy(dedicated...))
		s, err := openSemanticStore(ctx, router, dedicated, dim)
		if err != nil {
			t.Fatalf("openSemanticStore (dedicated): %v", err)
		}
		_ = s.Close()
	})
}
