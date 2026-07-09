//go:build manual

// Live validation for the rqlite facts backend. Behind the `manual` build tag,
// so it is EXCLUDED from `go test ./...` and CI: it needs a running rqlite
// node. Start one and point RQLITE_URL at it, then:
//
//	# The node MUST advertise an address the client can reach: gorqlite does
//	# cluster discovery and follows the node's advertised HTTP address, so a
//	# containerized node must advertise localhost, not its container hostname.
//	docker run -d -p 4001:4001 rqlite/rqlite \
//	    rqlited -http-addr 0.0.0.0:4001 -http-adv-addr localhost:4001 /rqlite/file/data
//	RQLITE_URL=http://localhost:4001 go test -tags manual -run TestRqliteLive -v ./internal/facts
//
// (A local binary works too: rqlited -http-addr localhost:4001 /tmp/rqlite-data.)
// It exercises the full Store contract (schema migrate, Save, FTS5 Search with
// scoping, and transactional Supersede) against the real HTTP+Raft path, using
// a unique tenant per run so it is safe to run repeatedly against a shared node.
// Verified passing against rqlite/rqlite v8 and Qdrant 1.18 (2026-07-09).
package facts

import (
	"context"
	"fmt"
	"os"
	"testing"
)

func TestRqliteLive(t *testing.T) {
	url := os.Getenv("RQLITE_URL")
	if url == "" {
		t.Skip("RQLITE_URL not set; start an rqlite node and set it to run this live validation")
	}
	st, err := OpenRqlite(url)
	if err != nil {
		t.Fatalf("OpenRqlite: %v", err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	// Unique tenant per run so repeated runs against a shared node don't collide.
	tenant := fmt.Sprintf("manual-%d", os.Getpid())
	sc := Scope{TenantID: tenant, UserID: "alice"}
	other := Scope{TenantID: tenant, UserID: "bob"}

	if err := st.Save(ctx, sc, Fact{ID: tenant + "-a", TenantID: tenant, UserID: "alice",
		Content: "the user is migrating the billing database to postgres", Importance: 0.6}); err != nil {
		t.Fatalf("Save alice: %v", err)
	}
	if err := st.Save(ctx, other, Fact{ID: tenant + "-b", TenantID: tenant, UserID: "bob",
		Content: "the user is migrating the billing database to postgres", Importance: 0.9}); err != nil {
		t.Fatalf("Save bob: %v", err)
	}

	// FTS5 + porter stemming ("migrate" matches "migrating"), scoped to alice.
	got, err := st.Search(ctx, sc, "migrate database", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].UserID != "alice" {
		t.Fatalf("isolation/search failed: got %+v, want single alice fact", got)
	}
	t.Logf("live rqlite Search OK: %q score=%.4f", got[0].Content, got[0].Score)

	// Transactional supersede over the HTTP+Raft path.
	newF := Fact{ID: tenant + "-a2", TenantID: tenant, UserID: "alice",
		Content: "the user finished migrating billing to postgres", Importance: 0.7}
	if err := st.Supersede(ctx, sc, tenant+"-a", newF); err != nil {
		t.Fatalf("Supersede: %v", err)
	}
	got, err = st.Search(ctx, sc, "migrating billing", 5)
	if err != nil {
		t.Fatalf("Search after supersede: %v", err)
	}
	if len(got) != 1 || got[0].ID != tenant+"-a2" {
		t.Fatalf("after supersede got %+v, want only the replacement", got)
	}
	t.Logf("live rqlite Supersede OK: current fact is now %q", got[0].Content)
}
