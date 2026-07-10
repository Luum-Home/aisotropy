package engram

import (
	"context"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// newStore builds an engram.Store over fresh in-memory facts + semantic
// substrates, plus the scopes/embedder tests need.
func newStore(t *testing.T) (Store, facts.Store, semantic.Store, semantic.Embedder) {
	t.Helper()
	fs, err := facts.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open facts: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	emb := semantic.NewHashEmbedder(0)
	ss := semantic.NewInMemoryStore(emb.Dimension())
	return Store{Facts: fs, Semantic: ss, Embedder: emb}, fs, ss, emb
}

func mustScope(t *testing.T, tenant, user string) facts.Scope {
	t.Helper()
	sc, err := facts.NewScope(tenant, user)
	if err != nil {
		t.Fatalf("facts.NewScope(%q,%q): %v", tenant, user, err)
	}
	return sc
}

func mustSemScope(t *testing.T, tenant, user string) semantic.Scope {
	t.Helper()
	sc, err := semantic.NewScope(tenant, user)
	if err != nil {
		t.Fatalf("semantic.NewScope(%q,%q): %v", tenant, user, err)
	}
	return sc
}

// (a) Save writes both the fact and, when importance-gated in, a retrievable
// Fact-kind vector.
func TestSaveDualIndexes(t *testing.T) {
	store, fs, ss, emb := newStore(t)
	ctx := context.Background()
	sc := mustScope(t, "acme", "alice")
	f := facts.Fact{ID: "f1", TenantID: "acme", UserID: "alice", Content: "prefers dark mode", Importance: 0.8}

	if err := store.Save(ctx, sc, f); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Facts tier has the fact.
	fres, err := fs.Search(ctx, sc, "dark mode", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(fres) != 1 {
		t.Fatalf("facts search count = %d, want 1", len(fres))
	}

	// Semantic tier has a retrievable Fact-kind vector, keyed by the fact id
	// via PointID, addressable independently of any Turn/Document vector.
	semSC := mustSemScope(t, "acme", "alice")
	wantID := semantic.PointID(semSC, semantic.KindFact, f.ID)
	q, _ := emb.Embed(ctx, []string{f.Content})
	sres, err := ss.Search(ctx, semSC, q[0], 5, semantic.KindFact)
	if err != nil {
		t.Fatal(err)
	}
	if len(sres) != 1 {
		t.Fatalf("semantic search count = %d, want 1", len(sres))
	}
	if sres[0].ID != wantID {
		t.Fatalf("vector id = %q, want %q", sres[0].ID, wantID)
	}
	if sres[0].Kind != semantic.KindFact {
		t.Fatalf("vector kind = %q, want %q", sres[0].Kind, semantic.KindFact)
	}
}

// Save below the importance gate must NOT dual-index.
func TestSaveBelowImportanceGateSkipsIndexing(t *testing.T) {
	store, _, ss, _ := newStore(t)
	store.MinImportance = 0.5
	ctx := context.Background()
	sc := mustScope(t, "acme", "alice")
	f := facts.Fact{ID: "f1", TenantID: "acme", UserID: "alice", Content: "likes tea", Importance: 0.1}

	if err := store.Save(ctx, sc, f); err != nil {
		t.Fatalf("Save: %v", err)
	}

	semSC := mustSemScope(t, "acme", "alice")
	id := semantic.PointID(semSC, semantic.KindFact, f.ID)
	dim := semantic.NewHashEmbedder(0).Dimension()
	zero := make([]float32, dim)
	got, serr := ss.Search(ctx, semSC, zero, 10, semantic.KindFact)
	if serr != nil {
		t.Fatal(serr)
	}
	for _, p := range got {
		if p.ID == id {
			t.Fatal("fact below MinImportance was dual-indexed, want skipped")
		}
	}
}

// (b) Supersede deletes the old fact's Fact-kind vector — search no longer
// returns it — this is bug #1 fixed by ADR-0017 phase 2.
func TestSupersedeDeletesOldVector(t *testing.T) {
	store, fs, ss, emb := newStore(t)
	ctx := context.Background()
	sc := mustScope(t, "acme", "alice")
	semSC := mustSemScope(t, "acme", "alice")

	old := facts.Fact{ID: "old", TenantID: "acme", UserID: "alice", Content: "region is us-east-1", Importance: 0.8}
	if err := store.Save(ctx, sc, old); err != nil {
		t.Fatalf("Save(old): %v", err)
	}
	oldVectorID := semantic.PointID(semSC, semantic.KindFact, old.ID)

	// Sanity: the old vector exists before the supersede.
	q, _ := emb.Embed(ctx, []string{old.Content})
	before, err := ss.Search(ctx, semSC, q[0], 5, semantic.KindFact)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range before {
		if p.ID == oldVectorID {
			found = true
		}
	}
	if !found {
		t.Fatal("precondition failed: old fact's vector not present before Supersede")
	}

	newFact := facts.Fact{ID: "new", TenantID: "acme", UserID: "alice", Content: "region is eu-west-1", Importance: 0.8}
	if err := store.Supersede(ctx, sc, old.ID, newFact); err != nil {
		t.Fatalf("Supersede: %v", err)
	}

	// THE INVARIANT: the old fact's vector is gone. Search across all Fact
	// vectors within scope must never return oldVectorID again.
	dim := emb.Dimension()
	all, err := ss.Search(ctx, semSC, make([]float32, dim), 50, semantic.KindFact)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range all {
		if p.ID == oldVectorID {
			t.Fatalf("bug #1 regression: superseded fact %q's vector %q is still retrievable", old.ID, oldVectorID)
		}
	}

	// The new fact's vector IS present and correctly keyed.
	newVectorID := semantic.PointID(semSC, semantic.KindFact, newFact.ID)
	newQ, _ := emb.Embed(ctx, []string{newFact.Content})
	newRes, err := ss.Search(ctx, semSC, newQ[0], 5, semantic.KindFact)
	if err != nil {
		t.Fatal(err)
	}
	newFound := false
	for _, p := range newRes {
		if p.ID == newVectorID {
			newFound = true
		}
	}
	if !newFound {
		t.Fatal("new fact's vector was not indexed by Supersede")
	}

	// The facts tier itself also reflects the supersede: searching finds only
	// the new content, not the old.
	fres, err := fs.Search(ctx, sc, "region", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(fres) != 1 || fres[0].Content != newFact.Content {
		t.Fatalf("facts search after supersede = %+v, want exactly the new fact", fres)
	}
}

// (c) Delete removes both fact and its Fact-kind vector — bug #2 fixed.
func TestDeleteRemovesFactAndVector(t *testing.T) {
	store, fs, ss, emb := newStore(t)
	ctx := context.Background()
	sc := mustScope(t, "acme", "alice")
	semSC := mustSemScope(t, "acme", "alice")

	f := facts.Fact{ID: "dup", TenantID: "acme", UserID: "alice", Content: "prefers dark mode", Importance: 0.6}
	if err := store.Save(ctx, sc, f); err != nil {
		t.Fatalf("Save: %v", err)
	}
	vectorID := semantic.PointID(semSC, semantic.KindFact, f.ID)
	q, _ := emb.Embed(ctx, []string{f.Content})
	before, err := ss.Search(ctx, semSC, q[0], 5, semantic.KindFact)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("precondition failed: vector not present before Delete, got %d", len(before))
	}

	if err := store.Delete(ctx, sc, []string{f.ID}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Fact row is gone (mirroring the sleep cycle's dedup cleanup).
	all, err := fs.ExportAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, ff := range all {
		if ff.TenantID == "acme" && ff.UserID == "alice" && ff.ID == f.ID {
			t.Fatal("fact row still present after Delete")
		}
	}

	// Vector is gone too — bug #2 fixed.
	dim := emb.Dimension()
	after, err := ss.Search(ctx, semSC, make([]float32, dim), 50, semantic.KindFact)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range after {
		if p.ID == vectorID {
			t.Fatalf("bug #2 regression: deleted fact %q's vector %q is still retrievable", f.ID, vectorID)
		}
	}
}

// (d) Scope isolation holds throughout: one tenant's Save/Supersede/Delete
// never touches another tenant's facts or vectors, and PointID never
// collides across tenants even for the same source fact id string (the facts
// schema's own id column is a plain global key — see factID() in
// internal/facts/extract.go, which folds tenant+user into the hash precisely
// so real callers never collide; here we assert the semantic-layer half of
// that guarantee directly).
func TestScopeIsolationHoldsThroughout(t *testing.T) {
	store, fs, ss, emb := newStore(t)
	ctx := context.Background()
	scA := mustScope(t, "acme", "alice")
	scB := mustScope(t, "globex", "bob")
	semA := mustSemScope(t, "acme", "alice")
	semB := mustSemScope(t, "globex", "bob")

	// PointID must not collide across tenants even for the identical source
	// id, because Kind AND scope are folded into the id derivation.
	const sameSourceID = "f-shared"
	idA := semantic.PointID(semA, semantic.KindFact, sameSourceID)
	idB := semantic.PointID(semB, semantic.KindFact, sameSourceID)
	if idA == idB {
		t.Fatal("PointID collided across tenants for the same source fact id")
	}

	fA := facts.Fact{ID: "acme-1", TenantID: "acme", UserID: "alice", Content: "acme secret plan", Importance: 0.9}
	fB := facts.Fact{ID: "globex-1", TenantID: "globex", UserID: "bob", Content: "globex secret plan", Importance: 0.9}
	if err := store.Save(ctx, scA, fA); err != nil {
		t.Fatalf("Save(A): %v", err)
	}
	if err := store.Save(ctx, scB, fB); err != nil {
		t.Fatalf("Save(B): %v", err)
	}

	// B's search never sees A's vector or content, and vice versa.
	dim := emb.Dimension()
	resA, err := ss.Search(ctx, semA, make([]float32, dim), 50, semantic.KindFact)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range resA {
		if p.TenantID != "acme" || p.UserID != "alice" {
			t.Fatalf("scope leak: tenant acme's search returned a point from %s/%s", p.TenantID, p.UserID)
		}
	}
	resB, err := ss.Search(ctx, semB, make([]float32, dim), 50, semantic.KindFact)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range resB {
		if p.TenantID != "globex" || p.UserID != "bob" {
			t.Fatalf("scope leak: tenant globex's search returned a point from %s/%s", p.TenantID, p.UserID)
		}
	}

	// Deleting A's fact must not remove B's fact or vector.
	if err := store.Delete(ctx, scA, []string{fA.ID}); err != nil {
		t.Fatalf("Delete(A): %v", err)
	}
	bFacts, err := fs.Search(ctx, scB, "globex secret plan", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(bFacts) != 1 {
		t.Fatalf("tenant globex's fact was affected by tenant acme's Delete: got %d results", len(bFacts))
	}
	q, _ := emb.Embed(ctx, []string{fB.Content})
	bVec, err := ss.Search(ctx, semB, q[0], 5, semantic.KindFact)
	if err != nil {
		t.Fatal(err)
	}
	if len(bVec) != 1 {
		t.Fatal("tenant globex's vector was deleted by tenant acme's Delete — isolation gate violated")
	}
}

// (e) A nil Semantic (facts-only deployment) works: Save/Supersede/Delete
// never touch the semantic tier and never error because of it.
func TestNilSemanticIsFactsOnly(t *testing.T) {
	fs, err := facts.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open facts: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	store := Store{Facts: fs} // Semantic and Embedder left nil
	ctx := context.Background()
	sc := mustScope(t, "acme", "alice")

	f := facts.Fact{ID: "f1", TenantID: "acme", UserID: "alice", Content: "prefers dark mode", Importance: 0.9,
		CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := store.Save(ctx, sc, f); err != nil {
		t.Fatalf("Save (facts-only): %v", err)
	}
	res, err := fs.Search(ctx, sc, "dark mode", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("facts-only Save did not persist: got %d results", len(res))
	}

	newFact := facts.Fact{ID: "f2", TenantID: "acme", UserID: "alice", Content: "prefers light mode", Importance: 0.9}
	if err := store.Supersede(ctx, sc, "f1", newFact); err != nil {
		t.Fatalf("Supersede (facts-only): %v", err)
	}
	res2, err := fs.Search(ctx, sc, "light mode", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2) != 1 {
		t.Fatalf("facts-only Supersede did not persist the new fact: got %d results", len(res2))
	}

	if err := store.Delete(ctx, sc, []string{"f2"}); err != nil {
		t.Fatalf("Delete (facts-only): %v", err)
	}
	res3, err := fs.Search(ctx, sc, "light mode", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res3) != 0 {
		t.Fatalf("facts-only Delete did not remove the fact: got %d results", len(res3))
	}
}
