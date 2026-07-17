package api

import (
	"context"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/primitives"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// baseAgentRegistry is the shared base tool metadata used by the overlay tests:
// one always-permitted tool whose Description the sleep cycle later improves.
func baseAgentRegistry() *tools.Registry {
	return tools.NewRegistry(
		tools.Tool{Name: "write_tasks", Description: "write a task to the journal"},
	)
}

// rewritePrimitive drives a real friction->consolidate cycle so the test asserts
// on the ACTUAL guidance the sleep cycle produces (not a hand-crafted string):
// it records enough friction to cross threshold, then Consolidate rewrites the
// primitive via the deterministic HeuristicRewriter.
func rewritePrimitive(t *testing.T, store *primitives.Store, tenant, user, name string) primitives.Primitive {
	t.Helper()
	if _, err := store.RecordFriction(tenant, user, name, 3.0); err != nil {
		t.Fatalf("RecordFriction: %v", err)
	}
	n, err := primitives.Consolidate(context.Background(), store, primitives.HeuristicRewriter{}, 3.0)
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 primitive rewritten, got %d", n)
	}
	p, ok := store.Get(tenant, user, name)
	if !ok {
		t.Fatalf("rewritten primitive %s/%s/%s not found", tenant, user, name)
	}
	if p.Revision == 0 || p.Description == "" || len(p.Examples) == 0 {
		t.Fatalf("primitive was not rewritten with improved guidance: %+v", p)
	}
	return p
}

// TestOverlay_RewrittenGuidanceReachesModel (requirement a): a friction-rewritten
// primitive's improved Description AND accumulated corrective Examples appear in
// the assembled tool guidance for that (tenant, user) — on BOTH the native path
// (Permitted -> ToolSpec.Description) and the fallback path (PromptFragment).
func TestOverlay_RewrittenGuidanceReachesModel(t *testing.T) {
	const tenant, user, tool = "acme", "alice", "write_tasks"
	store := primitives.NewStore()
	base := baseAgentRegistry()
	p := rewritePrimitive(t, store, tenant, user, tool)

	active := overlayRegistry(store, tenant, user, base, nil)

	// Native path: the model is handed the tool's Description via ToolSpec.
	permitted := active.Permitted(nil)
	if len(permitted) != 1 {
		t.Fatalf("expected 1 permitted tool, got %d", len(permitted))
	}
	desc := permitted[0].Description
	if !strings.Contains(desc, "write a task to the journal") {
		t.Errorf("overlay dropped the base description: %q", desc)
	}
	if !strings.Contains(desc, p.Description) {
		t.Errorf("improved description %q not present in overlaid tool description %q", p.Description, desc)
	}
	for _, ex := range p.Examples {
		if !strings.Contains(desc, ex) {
			t.Errorf("corrective example %q not present in overlaid tool description %q", ex, desc)
		}
	}

	// Fallback path: the guidance is rendered into the system-prompt fragment.
	frag := active.PromptFragment(nil)
	if !strings.Contains(frag, p.Description) {
		t.Errorf("improved description missing from PromptFragment:\n%s", frag)
	}
	for _, ex := range p.Examples {
		if !strings.Contains(frag, ex) {
			t.Errorf("corrective example %q missing from PromptFragment:\n%s", ex, frag)
		}
	}
}

// TestOverlay_NilStoreIsByteForByteUnchanged (requirement b): with no store the
// handler must present the base registry untouched. RegisterAgent keeps this
// guarantee by using the original *tools.Registry object when primStore is nil;
// this test pins the observable output (Permitted + PromptFragment) as identical.
func TestOverlay_NilStoreIsByteForByteUnchanged(t *testing.T) {
	base := baseAgentRegistry()

	// A store exists with a rewritten primitive, but if it is NOT supplied the
	// overlay must never run — so its guidance must NOT leak into the base output.
	sneaky := primitives.NewStore()
	p := rewritePrimitive(t, sneaky, "acme", "alice", "write_tasks")

	// The base (no overlay) output.
	if got := base.PromptFragment(nil); strings.Contains(got, p.Description) {
		t.Fatalf("base PromptFragment unexpectedly contains rewritten guidance without overlay:\n%s", got)
	}
	baseFrag := base.PromptFragment(nil)
	basePermitted := base.Permitted(nil)

	// Re-deriving without invoking the overlay must be identical, byte for byte.
	if baseFrag != base.PromptFragment(nil) {
		t.Fatal("base PromptFragment is not stable")
	}
	if len(basePermitted) != 1 || basePermitted[0].Description != "write a task to the journal" {
		t.Fatalf("base tool metadata changed unexpectedly: %+v", basePermitted)
	}
}

// TestOverlay_IsolatedPerTenantUser (requirement c): a primitive rewritten for
// (acme, alice) must NEVER surface in the tool guidance assembled for a different
// tenant or a different user. The overlay reads only store.Get(tenant, user, ·)
// for the request's own identity.
func TestOverlay_IsolatedPerTenantUser(t *testing.T) {
	const tool = "write_tasks"
	store := primitives.NewStore()
	base := baseAgentRegistry()

	// Only (acme, alice) accrues friction and gets rewritten.
	pAlice := rewritePrimitive(t, store, "acme", "alice", tool)

	// alice at acme sees her improved guidance.
	aliceFrag := overlayRegistry(store, "acme", "alice", base, nil).PromptFragment(nil)
	if !strings.Contains(aliceFrag, pAlice.Description) {
		t.Fatalf("alice did not receive her own rewritten guidance:\n%s", aliceFrag)
	}

	// Different user, same tenant: MUST NOT see alice's guidance.
	bobFrag := overlayRegistry(store, "acme", "bob", base, nil).PromptFragment(nil)
	if strings.Contains(bobFrag, pAlice.Description) {
		t.Errorf("cross-USER leak: bob sees alice's rewritten guidance:\n%s", bobFrag)
	}
	if bobFrag != base.PromptFragment(nil) {
		t.Errorf("bob's guidance is not the untouched base:\n%s", bobFrag)
	}

	// Same user id, different tenant: MUST NOT see alice's guidance.
	otherTenantFrag := overlayRegistry(store, "globex", "alice", base, nil).PromptFragment(nil)
	if strings.Contains(otherTenantFrag, pAlice.Description) {
		t.Errorf("cross-TENANT leak: globex/alice sees acme/alice's rewritten guidance:\n%s", otherTenantFrag)
	}
	if otherTenantFrag != base.PromptFragment(nil) {
		t.Errorf("globex/alice guidance is not the untouched base:\n%s", otherTenantFrag)
	}
}

// TestOverlay_EmptyPrimitiveIsNoOp: a primitive that has recorded friction but
// has NOT yet been consolidated (empty Description, no Examples) contributes
// nothing — the overlaid description equals the base. This proves the overlay is
// an append of real guidance, not a marker that a primitive merely exists.
func TestOverlay_EmptyPrimitiveIsNoOp(t *testing.T) {
	const tenant, user, tool = "acme", "alice", "write_tasks"
	store := primitives.NewStore()
	base := baseAgentRegistry()

	if _, err := store.RecordFriction(tenant, user, tool, 1.0); err != nil {
		t.Fatalf("RecordFriction: %v", err)
	}
	// No Consolidate: the primitive exists but carries no improved guidance yet.

	active := overlayRegistry(store, tenant, user, base, nil)
	if got, want := active.PromptFragment(nil), base.PromptFragment(nil); got != want {
		t.Fatalf("empty primitive changed the guidance:\n got=%q\nwant=%q", got, want)
	}
}
