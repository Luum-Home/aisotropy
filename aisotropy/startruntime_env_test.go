package aisotropy

import (
	"context"
	"testing"
)

// TestStartRuntime_DedicatedTenantsAndDataDir drives the runtime boot (via Turn)
// with AISOTROPY_DEDICATED_TENANTS and AISOTROPY_DATA_DIR set, exercising the
// physical multi-tenancy routing branch (static allowlist -> tierRouter, the
// dedicated-tenant log path, openFactsStore/openSemanticStore routing stores)
// and the on-disk store branches (BadgerDB working memory + SQLite facts under
// the data dir) that the default in-memory tests never reach. A Turn for a
// dedicated tenant then completes end-to-end over those routed, on-disk stores.
func TestStartRuntime_DedicatedTenantsAndDataDir(t *testing.T) {
	t.Setenv("AISOTROPY_DEDICATED_TENANTS", "acme,globex")
	t.Setenv("AISOTROPY_DATA_DIR", t.TempDir())
	// Keep the in-process defaults for the networked tiers so no external
	// service is required (routing still applies, just over in-process stores).
	t.Setenv("RQLITE_URL", "")
	t.Setenv("QDRANT_HOST", "")

	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))
	defer closeApp(t, app)()

	out, err := app.Turn(context.Background(), ChatInput{
		TenantID: "acme", UserID: "alice", SessionID: "s1", Prompt: "hello",
	})
	if err != nil {
		t.Fatalf("Turn for a dedicated tenant over routed on-disk stores: %v", err)
	}
	if out.TenantID != "acme" {
		t.Errorf("tenant echo = %q, want acme", out.TenantID)
	}
	if !out.Stub {
		t.Error("expected the keyless stub client")
	}

	// A second, non-dedicated tenant shares the pooled store — proving the router
	// serves both tiers from one runtime.
	if _, err := app.Turn(context.Background(), ChatInput{
		TenantID: "shared-co", UserID: "bob", SessionID: "s2", Prompt: "hi",
	}); err != nil {
		t.Fatalf("Turn for a shared-pool tenant: %v", err)
	}
}
