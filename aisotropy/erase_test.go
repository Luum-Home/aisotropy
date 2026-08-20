package aisotropy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/plugintest"
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

// TestEraseSubject_RuntimeBuildFailureSurfaces covers EraseSubject's
// ensureRuntime-error branch: a data dir that is actually a file makes runtime
// construction fail, and EraseSubject surfaces that error.
func TestEraseSubject_RuntimeBuildFailureSurfaces(t *testing.T) {
	f := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	t.Setenv("AISOTROPY_DATA_DIR", f)

	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))
	defer func() { _ = app.Close() }()

	if _, err := app.EraseSubject(context.Background(), "acme", "alice"); err == nil {
		t.Fatal("EraseSubject must surface the runtime build failure from a bad data dir")
	}
}

// scanErrDocBackend is a facts.DocBackend whose Scan fails, so the sealed
// store's EraseScope (which Scans the namespace before deleting) surfaces an
// error rather than silently reporting zero erased.
type scanErrDocBackend struct {
	*plugintest.RefDocBackend
}

func (scanErrDocBackend) Scan(context.Context, string) ([]facts.BackendDoc, error) {
	return nil, errors.New("facts scan backend down")
}

// TestEraseSubject_FactsEraseScopeErrorSurfaces covers EraseSubject's facts-tier
// EraseScope-error branch: a cancelled ctx makes the facts tier's EraseScope
// call fail (context canceled), and EraseSubject surfaces it wrapped rather than
// silently returning a zero report.
func TestEraseSubject_FactsEraseScopeErrorSurfaces(t *testing.T) {
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))
	defer closeApp(t, app)()

	if _, err := app.ensureRuntime(); err != nil {
		t.Fatalf("ensureRuntime: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := app.EraseSubject(ctx, "acme", "alice"); err == nil {
		t.Fatal("EraseSubject must surface a facts-tier EraseScope error")
	}
}

// TestEraseSubject_FactsBackendScanErrorSurfaces is the WithFactsBackend
// analogue: a community DocBackend whose Scan fails makes the sealed store's
// EraseScope error, surfaced by EraseSubject.
func TestEraseSubject_FactsBackendScanErrorSurfaces(t *testing.T) {
	backend := scanErrDocBackend{RefDocBackend: plugintest.NewRefDocBackend()}
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9), WithFactsBackend(backend))
	defer closeApp(t, app)()

	if _, err := app.EraseSubject(context.Background(), "acme", "alice"); err == nil {
		t.Fatal("EraseSubject must surface the facts backend Scan error")
	}
}

// TestEraseSubject_SemanticEraseScopeErrorSurfaces covers EraseSubject's
// semantic-tier EraseScope-error branch: a vector backend whose Scan fails
// makes the sealed store's EraseScope error, surfaced rather than swallowed.
func TestEraseSubject_SemanticEraseScopeErrorSurfaces(t *testing.T) {
	backend := scanErrVectorBackend{RefVectorBackend: plugintest.NewRefVectorBackend()}
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9), WithVectorBackend(backend))
	defer closeApp(t, app)()

	if _, err := app.EraseSubject(context.Background(), "acme", "alice"); err == nil {
		t.Fatal("EraseSubject must surface the semantic backend Scan error")
	}
}

// TestEraseSubject_WorkingMemoryEraseErrorSurfaces covers EraseSubject's
// working-memory EraseSubject-error branch: closing the working-memory store
// before the call makes wm.EraseSubject fail (DB closed), which EraseSubject
// must surface wrapped rather than silently reporting a zero count.
//
// The close below is the one call site in the package that reaches rt.wm.Close
// directly instead of going through rt.closeFn, so it skips the stop-workers +
// join that teardown does and pulls the store out from under a LIVE
// consolidation worker. That used to make this test fail for a reason unrelated
// to anything it asserts: the worker's next drain landed inside badger's
// close window, where NewIterator answers with panic("DB Closed") instead of an
// error, and an unrecovered panic in a goroutine takes the whole test binary
// down — every package in the run reported FAIL. Measured on this test before
// the fix: 4 of 60 fresh processes.
//
// It stays written this way on purpose. The runtime exposes no way to stop the
// consolidation worker without tearing down every store, and this branch needs
// the facts and semantic tiers still open to be reached at all. What changed is
// the other side: the drain now answers a store that closes underneath it with
// an error and the worker stops (internal/memory/outbox.go,
// viewTolerantOfClose). So this test is also the integration witness that the
// close-during-drain window no longer kills the process.
func TestEraseSubject_WorkingMemoryEraseErrorSurfaces(t *testing.T) {
	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))
	defer closeApp(t, app)()

	rt, err := app.ensureRuntime()
	if err != nil {
		t.Fatalf("ensureRuntime: %v", err)
	}
	if err := rt.wm.Close(); err != nil {
		t.Fatalf("wm.Close: %v", err)
	}

	_, err = app.EraseSubject(context.Background(), "acme", "alice")
	if err == nil {
		t.Fatal("EraseSubject must surface a working-memory erase error")
	}
	// Pin WHICH branch failed. EraseSubject walks facts, then semantic, then
	// working memory, and a bare err != nil is satisfied by any of the three —
	// so without this the test would keep passing while covering a tier it is
	// not named for.
	if !strings.Contains(err.Error(), "erase working memory for subject") {
		t.Fatalf("EraseSubject error = %v, want the working-memory branch", err)
	}
}
