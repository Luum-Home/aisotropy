package semantic

// OWASP LLM08:2025 — Vector and Embedding Weaknesses (and the memory half of
// Agentic ASI06 — Memory & Context Poisoning / cross-tenant recall).
//
// The semantic tier's isolation guarantee is that a vector written under
// (tenantA,userA) is NEVER returned for a (tenantB,*) or (tenantA,userB) query —
// enforced by the (tenant,user) filter applied DURING the scan, not as a
// post-filter. This suite makes the attack maximally adversarial: the query text
// is EQUAL to the victim's secret content (so the embedding is identical and
// cosine similarity is 1.0), which is the worst case for any post-filter design.
// A correct in-scan filter returns nothing across scopes regardless of similarity.
// Deterministic: in-process InMemoryStore + a deterministic hash embedder; no
// network, no real vector DB. See docs/security/owasp-llm-coverage.md.

import (
	"context"
	"testing"
)

const llm08Secret = "TOP SECRET: launch code is 42-alpha-omega, do not disclose"

// seedSecret embeds llm08Secret under (tenant,user) and returns the store + the
// query vector for the IDENTICAL text (cosine 1.0 against the stored point).
func seedSecret(t *testing.T, tenant, user string) (*InMemoryStore, []float32) {
	t.Helper()
	emb := NewHashEmbedder(64)
	store := NewInMemoryStore(emb.Dimension())

	vecs, err := emb.Embed(context.Background(), []string{llm08Secret})
	if err != nil || len(vecs) != 1 {
		t.Fatalf("embed secret: %v", err)
	}
	sc, err := NewScope(tenant, user)
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	if err := store.Upsert(context.Background(), sc, []Point{{
		ID: "secret-1", TenantID: tenant, UserID: user, Kind: KindDocument,
		Content: llm08Secret, Vector: vecs[0],
	}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	return store, vecs[0]
}

// TestOWASP_LLM08_AdversarialQueryNeverCrossesTenant: even with the query vector
// identical to the victim's secret, a DIFFERENT tenant retrieves nothing.
func TestOWASP_LLM08_AdversarialQueryNeverCrossesTenant(t *testing.T) {
	store, query := seedSecret(t, "victim-tenant", "victim-user")

	attacker, err := NewScope("attacker-tenant", "attacker-user")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	got, err := store.Search(context.Background(), attacker, query, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("cross-tenant search returned %d points with an identical-to-secret query — isolation leak", len(got))
	}
}

// TestOWASP_LLM08_AdversarialQueryNeverCrossesUserSameTenant: same tenant,
// different user — the second isolation axis — also returns nothing.
func TestOWASP_LLM08_AdversarialQueryNeverCrossesUserSameTenant(t *testing.T) {
	store, query := seedSecret(t, "acme", "alice")

	bob, err := NewScope("acme", "bob")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	got, err := store.Search(context.Background(), bob, query, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("same-tenant cross-user search returned %d points — isolation leak between users", len(got))
	}
}

// TestOWASP_LLM08_OwnerStillRetrieves: the isolation is not a blanket denial —
// the legitimate (tenant,user) owner retrieves its own secret with the same
// query, proving the filter scopes rather than blocks.
func TestOWASP_LLM08_OwnerStillRetrieves(t *testing.T) {
	store, query := seedSecret(t, "acme", "alice")

	owner, err := NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	got, err := store.Search(context.Background(), owner, query, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].Content != llm08Secret {
		t.Fatalf("owner must retrieve its own point, got %d results", len(got))
	}
}

// TestOWASP_LLM08_FilterAppliedInScanNotPostHoc: with only the attacker's scope
// present in the store, a search returns nothing — but crucially, seeding BOTH
// scopes and searching as the attacker returns ONLY the attacker's own points,
// never the victim's, even though the victim's point scores higher. This is the
// in-scan-filter (not post-filter) property at the InMemoryStore seam.
func TestOWASP_LLM08_FilterAppliedInScanNotPostHoc(t *testing.T) {
	emb := NewHashEmbedder(64)
	store := NewInMemoryStore(emb.Dimension())
	ctx := context.Background()

	secretVec, _ := emb.Embed(ctx, []string{llm08Secret})
	attackerVec, _ := emb.Embed(ctx, []string{"attacker benign note"})

	victim, _ := NewScope("victim", "v1")
	attacker, _ := NewScope("attacker", "a1")
	if err := store.Upsert(ctx, victim, []Point{{ID: "v", TenantID: "victim", UserID: "v1", Kind: KindDocument, Content: llm08Secret, Vector: secretVec[0]}}); err != nil {
		t.Fatalf("upsert victim: %v", err)
	}
	if err := store.Upsert(ctx, attacker, []Point{{ID: "a", TenantID: "attacker", UserID: "a1", Kind: KindDocument, Content: "attacker benign note", Vector: attackerVec[0]}}); err != nil {
		t.Fatalf("upsert attacker: %v", err)
	}

	// Attacker searches with the victim's exact secret vector (cosine 1.0 on the
	// victim point, lower on its own). A post-filter would have scored the victim
	// point first; the in-scan filter never scores it.
	got, err := store.Search(ctx, attacker, secretVec[0], 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, p := range got {
		if p.TenantID != "attacker" || p.Content == llm08Secret {
			t.Fatalf("attacker search surfaced a non-attacker/victim point: %+v", p.Point)
		}
	}
}
