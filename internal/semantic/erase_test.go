package semantic

import (
	"context"
	"testing"
)

// TestInMemoryEraseScopeAllKinds proves EraseScope removes every point for a
// (tenant,user) across ALL kinds (Fact, Turn, and the user's own Document),
// while leaving other subjects — and tenant-shared documents — untouched. This
// is the semantic half of GDPR Art.17 subject erasure.
func TestInMemoryEraseScopeAllKinds(t *testing.T) {
	emb := NewHashEmbedder(0)
	st := NewInMemoryStore(emb.Dimension())
	ctx := context.Background()

	alice := isoScope(t, "acme", "alice")
	bob := isoScope(t, "acme", "bob")       // same tenant, different user
	other := isoScope(t, "globex", "alice") // different tenant, same user

	// Alice owns three points, one per kind.
	aliceKinds := []Kind{KindFact, KindTurn, KindDocument}
	for i, k := range aliceKinds {
		p := point(ctx, alice, emb, k, string(rune('a'+i)), "alice content "+string(k))
		if err := st.Upsert(ctx, alice, []Point{p}); err != nil {
			t.Fatalf("Upsert alice %s: %v", k, err)
		}
	}
	// Bob and the other-tenant subject each own one point.
	if err := st.Upsert(ctx, bob, []Point{point(ctx, bob, emb, KindFact, "b", "bob content")}); err != nil {
		t.Fatalf("Upsert bob: %v", err)
	}
	if err := st.Upsert(ctx, other, []Point{point(ctx, other, emb, KindFact, "o", "other content")}); err != nil {
		t.Fatalf("Upsert other: %v", err)
	}
	// Tenant-SHARED document (empty user, from a tenant-wide share): must survive
	// a per-user erase — it is not one user's personal data.
	shared, err := NewTenantScope("acme")
	if err != nil {
		t.Fatalf("NewTenantScope: %v", err)
	}
	sharedPt := Point{
		ID:       PointID(shared, KindDocument, "shared-doc"),
		TenantID: "acme",
		UserID:   "",
		Kind:     KindDocument,
		Content:  "tenant shared knowledge",
		Vector:   mustEmbed(ctx, emb, "tenant shared knowledge"),
	}
	if err := st.Upsert(ctx, shared, []Point{sharedPt}); err != nil {
		t.Fatalf("Upsert shared: %v", err)
	}

	// Erase alice: all three of her kinds go.
	n, err := st.EraseScope(ctx, alice)
	if err != nil {
		t.Fatalf("EraseScope: %v", err)
	}
	if n != 3 {
		t.Fatalf("EraseScope removed %d points, want 3 (fact+turn+document)", n)
	}

	q := embedOne(t, emb, "content")
	// Alice is empty across every kind.
	if got, _ := st.Search(ctx, alice, q, 10, aliceKinds...); len(got) != 0 {
		t.Fatalf("after erase, alice Search returned %d points, want 0: %+v", len(got), got)
	}
	// Bob and the other tenant are intact.
	if got, _ := st.Search(ctx, bob, q, 10); len(got) != 1 {
		t.Fatalf("bob Search returned %d, want 1 (erase leaked into another user)", len(got))
	}
	if got, _ := st.Search(ctx, other, q, 10); len(got) != 1 {
		t.Fatalf("globex/alice Search returned %d, want 1 (erase leaked across tenants)", len(got))
	}
	// Tenant-shared document is preserved.
	if got, _ := st.Search(ctx, shared, embedOne(t, emb, "tenant shared knowledge"), 10); len(got) != 1 {
		t.Fatalf("tenant-shared doc Search returned %d, want 1 (a per-user erase must not remove tenant-shared knowledge)", len(got))
	}

	// Idempotent: erasing again removes nothing.
	if n, err := st.EraseScope(ctx, alice); err != nil || n != 0 {
		t.Fatalf("second EraseScope = (%d,%v), want (0,nil) — not idempotent", n, err)
	}
}
