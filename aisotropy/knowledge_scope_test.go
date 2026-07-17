package aisotropy

import (
	"context"
	"testing"
)

// TestKnowledgeSDK_BadIdentityRejected covers the validation error branch of the
// remaining SDK ingestion/deletion arms (IngestKnowledge's is already covered):
// each validates its scope through semantic.NewScope/NewTenantScope and returns
// an error on a bad identity rather than silently accepting a cross-tenant or
// malformed scope (ADR-0011).
func TestKnowledgeSDK_BadIdentityRejected(t *testing.T) {
	app := New(WithLLM(fastStub()))
	defer closeApp(t, app)()
	ctx := context.Background()

	t.Run("DeleteKnowledge empty tenant", func(t *testing.T) {
		if _, err := app.DeleteKnowledge(ctx, "", "alice", "doc"); err == nil {
			t.Fatal("DeleteKnowledge with an empty tenant must return an error")
		}
	})
	t.Run("IngestTenantKnowledge empty tenant", func(t *testing.T) {
		if _, err := app.IngestTenantKnowledge(ctx, "", Document{ID: "d", Content: "c"}); err == nil {
			t.Fatal("IngestTenantKnowledge with an empty tenant must return an error")
		}
	})
	t.Run("DeleteTenantKnowledge empty tenant", func(t *testing.T) {
		if _, err := app.DeleteTenantKnowledge(ctx, "", "doc"); err == nil {
			t.Fatal("DeleteTenantKnowledge with an empty tenant must return an error")
		}
	})
}
