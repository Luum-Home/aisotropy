package aisotropy

import (
	"context"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// seedSubject writes one item into each memory tier for (tenant,user), so a
// subsequent EraseSubject has something to remove in every tier. It uses the
// runtime's own stores and embedder, exactly what the chat pipeline reads/writes.
func seedSubject(t *testing.T, rt *appRuntime, tenant, user string) {
	t.Helper()
	ctx := context.Background()

	fsc, err := facts.NewScope(tenant, user)
	if err != nil {
		t.Fatalf("facts.NewScope(%q,%q): %v", tenant, user, err)
	}
	if err := rt.factsStore.Save(ctx, fsc, facts.Fact{
		ID: tenant + "/" + user + "/f1", TenantID: tenant, UserID: user,
		Content: "subject fact content", Importance: 0.9,
	}); err != nil {
		t.Fatalf("factsStore.Save: %v", err)
	}

	if err := rt.wm.Append(ctx, memory.Key{Tenant: tenant, User: user, Session: "s1"},
		memory.Message{Role: "user", Content: "subject chat content"}); err != nil {
		t.Fatalf("wm.Append: %v", err)
	}

	if rt.semanticStore != nil {
		ssc, err := semantic.NewScope(tenant, user)
		if err != nil {
			t.Fatalf("semantic.NewScope: %v", err)
		}
		vecs, err := rt.embedder.Embed(ctx, []string{"subject vector content"})
		if err != nil {
			t.Fatalf("Embed: %v", err)
		}
		if err := rt.semanticStore.Upsert(ctx, ssc, []semantic.Point{{
			ID: semantic.PointID(ssc, semantic.KindTurn, "m1"), TenantID: tenant, UserID: user,
			Kind: semantic.KindTurn, Content: "subject vector content", Vector: vecs[0],
		}}); err != nil {
			t.Fatalf("semanticStore.Upsert: %v", err)
		}
	}
}

// factsCount returns how many current facts a subject can find, used to assert a
// tier was (or was not) erased.
func factsHas(t *testing.T, rt *appRuntime, tenant, user string) bool {
	t.Helper()
	fsc, err := facts.NewScope(tenant, user)
	if err != nil {
		t.Fatalf("facts.NewScope: %v", err)
	}
	got, err := rt.factsStore.Search(context.Background(), fsc, "subject fact content", 10)
	if err != nil {
		t.Fatalf("factsStore.Search: %v", err)
	}
	return len(got) > 0
}

func wmHas(t *testing.T, rt *appRuntime, tenant, user string) bool {
	t.Helper()
	got, err := rt.wm.Recent(context.Background(), memory.Key{Tenant: tenant, User: user, Session: "s1"}, 10)
	if err != nil {
		t.Fatalf("wm.Recent: %v", err)
	}
	return len(got) > 0
}

// TestEraseSubjectFansOutAndPreservesIsolation is the DoD for GDPR Art.17 subject
// erasure at the App facade: erasing (tenantA,userX) removes that subject's data
// from every tier, is idempotent, and leaves (tenantA,userY) and (tenantB,userX)
// completely intact.
func TestEraseSubjectFansOutAndPreservesIsolation(t *testing.T) {
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))
	defer closeApp(t, app)()

	rt, err := app.ensureRuntime()
	if err != nil {
		t.Fatalf("ensureRuntime: %v", err)
	}
	ctx := context.Background()

	// Three subjects: the erase target and the two isolation neighbours.
	seedSubject(t, rt, "acme", "alice")   // target
	seedSubject(t, rt, "acme", "bob")     // same tenant, other user
	seedSubject(t, rt, "globex", "alice") // other tenant, same user

	rep, err := app.EraseSubject(ctx, "acme", "alice")
	if err != nil {
		t.Fatalf("EraseSubject: %v", err)
	}
	if rep.FactsDeleted < 1 || rep.WorkingMemoryDeleted < 1 || rep.SemanticDeleted < 1 {
		t.Fatalf("EraseReport = %+v, want >=1 deleted in every tier", rep)
	}
	if rep.TenantID != "acme" || rep.UserID != "alice" {
		t.Fatalf("EraseReport identity = (%q,%q), want (acme,alice)", rep.TenantID, rep.UserID)
	}

	// Target is gone from every tier.
	if factsHas(t, rt, "acme", "alice") {
		t.Fatal("facts survived erase for the target subject")
	}
	if wmHas(t, rt, "acme", "alice") {
		t.Fatal("working memory survived erase for the target subject")
	}

	// Neighbours are fully intact — the isolation-preserving erase.
	for _, n := range []struct{ tenant, user string }{{"acme", "bob"}, {"globex", "alice"}} {
		if !factsHas(t, rt, n.tenant, n.user) {
			t.Fatalf("facts for %s/%s were erased — cross-subject leak", n.tenant, n.user)
		}
		if !wmHas(t, rt, n.tenant, n.user) {
			t.Fatalf("working memory for %s/%s was erased — cross-subject leak", n.tenant, n.user)
		}
	}

	// Idempotent: erasing again removes nothing.
	rep2, err := app.EraseSubject(ctx, "acme", "alice")
	if err != nil {
		t.Fatalf("second EraseSubject: %v", err)
	}
	if rep2.FactsDeleted != 0 || rep2.WorkingMemoryDeleted != 0 || rep2.SemanticDeleted != 0 {
		t.Fatalf("second EraseReport = %+v, want all zero (not idempotent)", rep2)
	}
}

// TestEraseSubjectRejectsBlankIdentity proves a blank/malformed subject errors
// and never wildcard-deletes: a neighbour seeded first survives the rejected calls.
func TestEraseSubjectRejectsBlankIdentity(t *testing.T) {
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))
	defer closeApp(t, app)()

	rt, err := app.ensureRuntime()
	if err != nil {
		t.Fatalf("ensureRuntime: %v", err)
	}
	seedSubject(t, rt, "acme", "alice")

	for _, tc := range []struct{ tenant, user string }{
		{"", "alice"},
		{"acme", ""},
		{"acme", "bad:user"},
	} {
		if _, err := app.EraseSubject(context.Background(), tc.tenant, tc.user); err == nil {
			t.Fatalf("EraseSubject(%q,%q) = nil err, want a validation error", tc.tenant, tc.user)
		}
	}
	// The real subject is untouched by the rejected calls (no wildcard delete).
	if !factsHas(t, rt, "acme", "alice") || !wmHas(t, rt, "acme", "alice") {
		t.Fatal("a rejected erase deleted real data — wildcard leak")
	}
}
