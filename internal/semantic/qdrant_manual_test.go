//go:build manual

// Live validation for the Qdrant semantic backend. Behind the `manual` build
// tag, so it is EXCLUDED from `go test ./...` and CI: it needs a running Qdrant
// node. Start one and point QDRANT_HOST at it, then:
//
//	docker run -p 6334:6334 qdrant/qdrant   # gRPC on 6334
//	QDRANT_HOST=localhost go test -tags manual -run TestQdrantLive -v ./internal/semantic
//
// It exercises the full Store contract over gRPC (collection setup, Upsert,
// payload-filtered Search, UUIDv5 idempotency) using a unique tenant per run so
// it is safe to run repeatedly against a shared node.
package semantic

import (
	"context"
	"fmt"
	"os"
	"testing"
)

func TestQdrantLive(t *testing.T) {
	host := os.Getenv("QDRANT_HOST")
	if host == "" {
		t.Skip("QDRANT_HOST not set; start a Qdrant node and set it to run this live validation")
	}
	ctx := context.Background()
	emb := NewHashEmbedder(0)
	st, err := OpenFromEnv(ctx, emb.Dimension())
	if err != nil {
		t.Fatalf("OpenFromEnv (qdrant): %v", err)
	}
	defer func() { _ = st.Close() }()

	tenant := fmt.Sprintf("manual-%d", os.Getpid())
	sc := Scope{tenant: tenant, user: "alice"}
	other := Scope{tenant: tenant, user: "bob"}

	mk := func(scope Scope, msg, content string) Point {
		return Point{ID: PointID(scope, KindTurn, msg), TenantID: scope.Tenant(), UserID: scope.User(),
			Kind: KindTurn, Content: content, Vector: mustEmbed(ctx, emb, content)}
	}

	if err := st.Upsert(ctx, sc, []Point{mk(sc, "m1", "postgres database migration plan")}); err != nil {
		t.Fatalf("Upsert alice: %v", err)
	}
	if err := st.Upsert(ctx, other, []Point{mk(other, "m1", "postgres database migration plan")}); err != nil {
		t.Fatalf("Upsert bob: %v", err)
	}

	// Payload-filtered search scoped to alice must not see bob's vector.
	got, err := st.Search(ctx, sc, mustEmbed(ctx, emb, "migrate database to postgres"), 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].UserID != "alice" {
		t.Fatalf("isolation/search failed: got %+v, want a single alice point", got)
	}
	t.Logf("live qdrant Search OK: %q score=%.4f", got[0].Content, got[0].Score)

	// Idempotency: re-upsert the same message id, then confirm no duplicate.
	if err := st.Upsert(ctx, sc, []Point{mk(sc, "m1", "postgres database migration plan v2")}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got, err = st.Search(ctx, sc, mustEmbed(ctx, emb, "postgres migration"), 10)
	if err != nil {
		t.Fatalf("Search after re-upsert: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("idempotency failed: got %d points, want 1", len(got))
	}
	t.Logf("live qdrant idempotency OK: 1 point after re-upsert, content=%q", got[0].Content)
}
