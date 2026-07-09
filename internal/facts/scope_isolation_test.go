package facts

import (
	"strings"
	"testing"
)

// TestEverySQLStatementIsTenantScoped is the isolation guard the tenancy
// statement builder (ADR-0011) would provide by construction if it could
// express this tier's FTS5 MATCH / bm25 / ON CONFLICT SQL — which it cannot.
// Since the SQL is therefore hand-written, this test fails the build if any
// facts statement stops carrying BOTH the tenant_id and user_id predicate,
// catching a future unscoped query (the "silent isolation leak" the ADR warns
// about) before it ships.
func TestEverySQLStatementIsTenantScoped(t *testing.T) {
	statements := map[string]string{
		"upsertSQL":         upsertSQL,
		"searchSQL":         searchSQL,
		"markSupersededSQL": markSupersededSQL,
	}
	for name, sql := range statements {
		if !strings.Contains(sql, "tenant_id") {
			t.Errorf("%s does not reference tenant_id — isolation gate violated:\n%s", name, sql)
		}
		if !strings.Contains(sql, "user_id") {
			t.Errorf("%s does not reference user_id — isolation gate violated:\n%s", name, sql)
		}
	}
	// The two read/mutate statements must bind the scope in a WHERE predicate,
	// not merely mention the columns (e.g. in a SELECT list). upsertSQL is an
	// INSERT whose scope travels as bound VALUES, so it is checked only for
	// column presence above.
	for _, name := range []string{"searchSQL", "markSupersededSQL"} {
		sql := statements[name]
		if !strings.Contains(sql, "tenant_id = ?") || !strings.Contains(sql, "user_id = ?") {
			t.Errorf("%s must bind tenant_id = ? AND user_id = ? in its WHERE clause:\n%s", name, sql)
		}
	}
}

// TestNewScopeValidatesThroughTenancy proves the blessed constructor rejects an
// invalid identity via the shared internal/tenancy validation rule, rather than
// accepting anything the way a bare Scope{...} literal would.
func TestNewScopeValidatesThroughTenancy(t *testing.T) {
	if _, err := NewScope("", "alice"); err == nil {
		t.Error("NewScope with empty tenant should fail validation, got nil error")
	}
	sc, err := NewScope("acme", "alice")
	if err != nil {
		t.Fatalf("NewScope with a valid identity returned error: %v", err)
	}
	if sc.TenantID != "acme" || sc.UserID != "alice" {
		t.Errorf("NewScope = %+v, want tenant=acme user=alice", sc)
	}
}
