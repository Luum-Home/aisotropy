package semantic

import (
	"context"
	"testing"
)

// TestNewTenantScopeHasEmptyUser verifies NewTenantScope produces a Scope
// with a tenant and an empty user (ADR-0019's "empty-user Scope" shape), and
// that it round-trips through PointID/Upsert like any other Scope.
func TestNewTenantScopeHasEmptyUser(t *testing.T) {
	sc, err := NewTenantScope("acme")
	if err != nil {
		t.Fatalf("NewTenantScope: %v", err)
	}
	if sc.Tenant() != "acme" {
		t.Fatalf("Tenant() = %q, want acme", sc.Tenant())
	}
	if sc.User() != "" {
		t.Fatalf("User() = %q, want empty (tenant-shared bucket)", sc.User())
	}
}

// TestNewTenantScopeRejectsEmptyTenant verifies a bad tenant is a returned
// error, never a silently accepted zero-value Scope (mirroring NewScope).
func TestNewTenantScopeRejectsEmptyTenant(t *testing.T) {
	if _, err := NewTenantScope(""); err == nil {
		t.Fatal("expected an error for an empty tenant, got nil")
	}
}

// TestTenantScopeIsolatedFromUserScope is the ADR-0019 DoD: a user-scoped
// search never returns a tenant-shared point and vice versa — each scan
// stays strictly single-scope; sharing happens by merging two searches at
// the call site (chat_stream.go's hydrateSystemPrompt), never by widening
// the store's own filter.
func TestTenantScopeIsolatedFromUserScope(t *testing.T) {
	emb := NewHashEmbedder(0)
	st := NewInMemoryStore(emb.Dimension())
	ctx := context.Background()

	tenantScope, err := NewTenantScope("acme")
	if err != nil {
		t.Fatalf("NewTenantScope: %v", err)
	}
	userScope := Scope{tenant: "acme", user: "alice"}

	const content = "the acme employee handbook"
	tenantDoc := point(ctx, tenantScope, emb, KindDocument, "handbook", content)
	userDoc := point(ctx, userScope, emb, KindDocument, "private-notes", content)

	if err := st.Upsert(ctx, tenantScope, []Point{tenantDoc}); err != nil {
		t.Fatalf("Upsert tenant-shared: %v", err)
	}
	if err := st.Upsert(ctx, userScope, []Point{userDoc}); err != nil {
		t.Fatalf("Upsert user-scoped: %v", err)
	}

	q := embedOne(t, emb, content)

	userResults, err := st.Search(ctx, userScope, q, 10, KindDocument)
	if err != nil {
		t.Fatalf("Search (user scope): %v", err)
	}
	if len(userResults) != 1 || userResults[0].ID != userDoc.ID {
		t.Fatalf("user-scoped search = %+v, want exactly the user's own document (tenant-shared point leaked)", userResults)
	}

	tenantResults, err := st.Search(ctx, tenantScope, q, 10, KindDocument)
	if err != nil {
		t.Fatalf("Search (tenant scope): %v", err)
	}
	if len(tenantResults) != 1 || tenantResults[0].ID != tenantDoc.ID {
		t.Fatalf("tenant-shared search = %+v, want exactly the tenant-shared document (user point leaked)", tenantResults)
	}
}

// TestTenantScopeIsolatedAcrossTenants verifies the tenant-shared bucket
// never leaks across the TENANT boundary — ADR-0019 relaxes only the
// within-tenant user axis, never cross-tenant isolation.
func TestTenantScopeIsolatedAcrossTenants(t *testing.T) {
	emb := NewHashEmbedder(0)
	st := NewInMemoryStore(emb.Dimension())
	ctx := context.Background()
	const content = "confidential acme incident runbook"

	acmeShared, err := NewTenantScope("acme")
	if err != nil {
		t.Fatalf("NewTenantScope(acme): %v", err)
	}
	globexShared, err := NewTenantScope("globex")
	if err != nil {
		t.Fatalf("NewTenantScope(globex): %v", err)
	}

	doc := point(ctx, acmeShared, emb, KindDocument, "runbook", content)
	if err := st.Upsert(ctx, acmeShared, []Point{doc}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	leaked, err := st.Search(ctx, globexShared, embedOne(t, emb, content), 10, KindDocument)
	if err != nil {
		t.Fatalf("Search (globex shared): %v", err)
	}
	if len(leaked) != 0 {
		t.Fatalf("cross-tenant leak: globex's tenant-shared search returned %d acme documents, want 0", len(leaked))
	}
}

// TestTenantScopeSearchNeverReturnsFactOrTurn is the ADR-0019 "Mandatory"
// guardrail: only KindDocument is ever written to, or read from, the
// tenant-shared bucket. Even if a Fact- or Turn-kind point somehow ended up
// upserted under a tenant-shared Scope (never done by any production call
// site — Ingest/IngestTenantKnowledge only ever write KindDocument, and
// hydrateSystemPrompt's tenant-shared search always passes KindDocument as
// its only kind filter), the KindDocument-filtered search a caller is
// expected to use against this bucket must never surface it.
func TestTenantScopeSearchNeverReturnsFactOrTurn(t *testing.T) {
	emb := NewHashEmbedder(0)
	st := NewInMemoryStore(emb.Dimension())
	ctx := context.Background()

	tenantScope, err := NewTenantScope("acme")
	if err != nil {
		t.Fatalf("NewTenantScope: %v", err)
	}

	const content = "quarterly planning notes"
	docPt := point(ctx, tenantScope, emb, KindDocument, "plan-doc", content)
	factPt := point(ctx, tenantScope, emb, KindFact, "plan-fact", content)
	turnPt := point(ctx, tenantScope, emb, KindTurn, "plan-turn", content)
	if err := st.Upsert(ctx, tenantScope, []Point{docPt, factPt, turnPt}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	q := embedOne(t, emb, content)

	// The only kind filter a tenant-shared read ever uses in production
	// (KindDocument alone): must return exactly the Document point.
	docOnly, err := st.Search(ctx, tenantScope, q, 10, KindDocument)
	if err != nil {
		t.Fatalf("Search (KindDocument): %v", err)
	}
	if len(docOnly) != 1 || docOnly[0].ID != docPt.ID {
		t.Fatalf("tenant-shared KindDocument search = %+v, want exactly the Document point", docOnly)
	}
	for _, p := range docOnly {
		if p.Kind == KindFact || p.Kind == KindTurn {
			t.Fatalf("tenant-shared bucket returned a %s-kind point from a KindDocument-filtered search: %+v", p.Kind, p)
		}
	}
}
