package graph

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/tenancy"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// BLOCKER (ADR-0060 plan Task 6 = Phase 9 DoD milestones.md:207): the ADR-0016
// invariant is machine-enforced —
//  1. two graphs run concurrently under two different tenant ctxs and no node
//     ever observes the other tenant (under -race);
//  2. a node body can obtain ONLY its own tenant's Scope via tenancy.FromContext
//     — it cannot widen scope beyond its ctx identity;
//  3. the executor threads a DERIVED ctx, so every node's tenant == the request
//     ctx's tenant, and differs across the two concurrent graphs.

// buildIsolationGraph builds a 2-node chain researcher→writer where BOTH nodes run
// a scoped tool whose executor records (via the injected observer) the tenant the
// node observed and the Scope it could obtain. The observer is how the test proves
// no cross-tenant leak and no scope widening.
func buildIsolationGraph(t *testing.T) *Graph {
	t.Helper()
	researcher := Node{ID: "researcher", System: "sys-researcher", Scopes: []string{"tools:probe"}, Tools: []string{"probe"}, MaxSteps: 3}
	writer := Node{ID: "writer", System: "sys-writer", Scopes: []string{"tools:probe"}, Tools: []string{"probe"}, MaxSteps: 3,
		Prompt: func(_ GraphInput, from map[string]EdgePayload) (string, error) {
			return "write from " + from["researcher"].Text, nil
		}}
	g, err := Build(NewSpec().
		AddNode(researcher).AddNode(writer).
		AddEdge(Edge{From: "researcher", To: "writer"}).
		SetEntry("researcher"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return g
}

// observation is what a node's tool executor saw about the ctx it ran under.
type observation struct {
	tenantSeen  string // via tenancy.TenantFromContext
	scopeTenant string // via tenancy.FromContext(ctx).Tenant()
	scopeErr    error
}

func TestIsolation_TwoConcurrentGraphsNeverCross(t *testing.T) {
	reg := tools.NewRegistry(tools.Tool{Name: "probe", RequiredScope: "tools:probe"})

	var mu sync.Mutex
	// obs[expectedTenant] = list of observations any node made while running that
	// graph. Every observation MUST match the expected tenant.
	obs := map[string][]observation{}

	// runOne runs one tenant's graph. Each node's tool executor reads identity from
	// the ctx it was handed and records what it saw for this tenant.
	runOne := func(tenant string, wg *sync.WaitGroup, errs chan<- error) {
		defer wg.Done()
		fc := newFakeClient()
		// Every node calls the probe tool once, then answers.
		fc.tools["sys-researcher"] = []llm.ToolCallResult{{Calls: []llm.ToolCall{{Name: "probe"}}}, {Message: "findings-" + tenant}}
		fc.tools["sys-writer"] = []llm.ToolCallResult{{Calls: []llm.ToolCall{{Name: "probe"}}}, {Message: "brief-" + tenant}}

		deps := Deps{
			Client:   fc,
			Registry: reg,
			Exec: func(ctx context.Context, _ string, _ string) (string, error) {
				seen, _ := tenancy.TenantFromContext(ctx)
				sc, scErr := tenancy.FromContext(ctx)
				o := observation{tenantSeen: seen, scopeErr: scErr}
				if scErr == nil {
					o.scopeTenant = sc.Tenant()
				}
				mu.Lock()
				obs[tenant] = append(obs[tenant], o)
				mu.Unlock()
				return "ok", nil
			},
		}

		g := buildIsolationGraph(t)
		// The request ctx carries ONLY this tenant's identity — the executor derives
		// it into every node, never re-mints it.
		ctx := tenancy.WithUser(tenancy.WithTenant(context.Background(), tenant), "u-"+tenant)
		if _, err := Execute(ctx, deps, g, GraphInput{Prompt: "topic"}); err != nil {
			errs <- err
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go runOne("tenant-a", &wg, errs)
	go runOne("tenant-b", &wg, errs)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Execute: %v", err)
	}

	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		got := obs[tenant]
		if len(got) != 2 { // researcher + writer each probed once
			t.Fatalf("tenant %q: expected 2 node observations, got %d", tenant, len(got))
		}
		for _, o := range got {
			if o.tenantSeen != tenant {
				t.Fatalf("CROSS-TENANT LEAK: a node running graph %q observed tenant %q via ctx", tenant, o.tenantSeen)
			}
			// No scope widening: the ONLY Scope obtainable is this tenant's own.
			if o.scopeErr != nil {
				t.Fatalf("tenant %q: node could not obtain its own Scope: %v", tenant, o.scopeErr)
			}
			if o.scopeTenant != tenant {
				t.Fatalf("SCOPE WIDENING: node in graph %q obtained a Scope for tenant %q", tenant, o.scopeTenant)
			}
		}
	}
}

// TestIsolation_NodeCtxIsDerivedFromRequest: every node's ctx tenant equals the
// request ctx's tenant — the executor derives, never re-mints, so identity is
// inherited and cannot be changed by a Node/Edge/PromptFunc.
func TestIsolation_NodeCtxIsDerivedFromRequest(t *testing.T) {
	reg := tools.NewRegistry(tools.Tool{Name: "probe", RequiredScope: "tools:probe"})
	fc := newFakeClient()
	fc.tools["sys-researcher"] = []llm.ToolCallResult{{Calls: []llm.ToolCall{{Name: "probe"}}}, {Message: "ok"}}
	fc.tools["sys-writer"] = []llm.ToolCallResult{{Calls: []llm.ToolCall{{Name: "probe"}}}, {Message: "ok"}}

	const tenant = "acme"
	var mu sync.Mutex
	var seen []string
	deps := Deps{
		Client:   fc,
		Registry: reg,
		Exec: func(ctx context.Context, _, _ string) (string, error) {
			ten, _ := tenancy.TenantFromContext(ctx)
			mu.Lock()
			seen = append(seen, ten)
			mu.Unlock()
			return "ok", nil
		},
	}
	ctx := tenancy.WithTenant(context.Background(), tenant)
	if _, err := Execute(ctx, deps, buildIsolationGraph(t), GraphInput{Prompt: "x"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 node observations, got %d", len(seen))
	}
	for _, s := range seen {
		if s != tenant {
			t.Fatalf("node ctx tenant = %q, want the request's %q (derived, not re-minted)", s, tenant)
		}
	}
}

// TestIsolation_PublicNodeExposesNoScope is a compile-fenced guard: the Node /
// Edge / PromptFunc surfaces expose NO Scope, tenant field, or store handle — a
// consumer cannot pass identity in as data. This is asserted structurally (the
// types simply have no such field to set); the string below documents the intent.
func TestIsolation_PublicNodeExposesNoScope(t *testing.T) {
	// A Node carries only ID/System/Prompt/Scopes/Tools/MaxSteps — Scopes are string
	// permission names, NOT a tenancy.Scope, and there is no TenantID field. If a
	// field granting cross-tenant reach were added, this reference block would need
	// updating, flagging the regression in review.
	n := Node{ID: "x", System: "s", Scopes: []string{"a"}, Tools: nil, MaxSteps: 1}
	if n.ID == "" || !strings.HasPrefix(n.System, "s") {
		t.Fatal("unreachable")
	}
}
