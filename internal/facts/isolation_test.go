package facts

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

// This file is the store-level adversarial isolation suite for the facts tier.
// It locks the constitutional multi-tenant gate (.claude/rules/
// constitutional-gates.md) into CI at the storage layer: every scoped READ must
// return ONLY the scope's own rows (including the tenantA/userB and
// tenantB/userA cross combinations), every WRITE must reject a cross-scope Fact
// structurally (ErrScopeMismatch), and the one deliberate unscoped read
// (ExportAll) must remain the single reviewed exception — a second unscoped
// read method on the Store interface fails the enumeration guard below.
//
// All tests run against the in-process SQLiteStore(":memory:") default, so no
// external service is required. Run with -race to catch cross-scope bleed.

// isoScopes are the four (tenant,user) combinations used across the suite: two
// primary tenants plus the two cross combinations that a naive single-column
// filter (tenant-only or user-only) would leak between.
func isoScopes(t *testing.T) (a, b, crossTU, crossUT Scope) {
	t.Helper()
	mk := func(tenant, user string) Scope {
		sc, err := NewScope(tenant, user)
		if err != nil {
			t.Fatalf("NewScope(%q,%q): %v", tenant, user, err)
		}
		return sc
	}
	// a = tenantA/userA, b = tenantB/userB,
	// crossTU = tenantA/userB, crossUT = tenantB/userA.
	return mk("acme", "alice"), mk("globex", "bob"), mk("acme", "bob"), mk("globex", "alice")
}

func newFactsStore(t *testing.T) *SQLiteStore {
	t.Helper()
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func factFor(sc Scope, id, content string) Fact {
	return Fact{
		ID:         id,
		TenantID:   sc.Tenant(),
		UserID:     sc.User(),
		Content:    content,
		Importance: 0.5,
	}
}

// TestIsolationFactsSearchNeverCrossScope writes one fact per scope — all four
// with the SAME searchable content — and asserts each scope's Search returns
// exactly its own row. A tenant-only or user-only filter would surface the
// cross combinations; only the full (tenant_id AND user_id) predicate keeps
// each scope's result set to one row.
func TestIsolationFactsSearchNeverCrossScope(t *testing.T) {
	st := newFactsStore(t)
	ctx := context.Background()
	a, b, crossTU, crossUT := isoScopes(t)

	const content = "the quarterly revenue secret keyword"
	seed := map[string]Scope{
		"fact-a":       a,
		"fact-b":       b,
		"fact-crossTU": crossTU,
		"fact-crossUT": crossUT,
	}
	for id, sc := range seed {
		if err := st.Save(ctx, sc, factFor(sc, id, content)); err != nil {
			t.Fatalf("Save(%s): %v", id, err)
		}
	}

	cases := []struct {
		name   string
		scope  Scope
		wantID string
	}{
		{"tenantA/userA", a, "fact-a"},
		{"tenantB/userB", b, "fact-b"},
		{"tenantA/userB", crossTU, "fact-crossTU"},
		{"tenantB/userA", crossUT, "fact-crossUT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := st.Search(ctx, tc.scope, "secret keyword", 10)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("Search under %s returned %d facts, want exactly 1 (cross-scope leak): %+v", tc.name, len(got), got)
			}
			if got[0].ID != tc.wantID {
				t.Fatalf("Search under %s returned %q, want %q — a foreign scope's fact leaked", tc.name, got[0].ID, tc.wantID)
			}
			if got[0].TenantID != tc.scope.Tenant() || got[0].UserID != tc.scope.User() {
				t.Fatalf("Search under %s returned a fact scoped to (%q,%q), want (%q,%q)",
					tc.name, got[0].TenantID, got[0].UserID, tc.scope.Tenant(), tc.scope.User())
			}
		})
	}
}

// TestIsolationFactsSaveRejectsCrossScopeWrite pins the STRUCTURAL write guard:
// Save must reject a Fact whose (TenantID,UserID) differs from the operation
// Scope with ErrScopeMismatch, on every axis (tenant-only, user-only, both).
// This is the omitted-filter regression guard for the write path — a refactor
// that drops the scope check would let a caller stamp another tenant's identity
// onto a row.
func TestIsolationFactsSaveRejectsCrossScopeWrite(t *testing.T) {
	st := newFactsStore(t)
	ctx := context.Background()
	a, b, _, _ := isoScopes(t)

	mism := []struct {
		name string
		fact Fact
	}{
		{"foreign tenant+user", factFor(b, "x1", "c")},
		{"foreign tenant only", Fact{ID: "x2", TenantID: b.Tenant(), UserID: a.User(), Content: "c", Importance: 0.5}},
		{"foreign user only", Fact{ID: "x3", TenantID: a.Tenant(), UserID: b.User(), Content: "c", Importance: 0.5}},
	}
	for _, m := range mism {
		t.Run(m.name, func(t *testing.T) {
			if err := st.Save(ctx, a, m.fact); !errors.Is(err, ErrScopeMismatch) {
				t.Fatalf("Save with %s under scope A = %v, want ErrScopeMismatch", m.name, err)
			}
		})
	}

	// And the reviewed cross-tenant read confirms nothing was written.
	all, err := st.ExportAll(ctx)
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("cross-scope writes were rejected but %d rows landed anyway: %+v", len(all), all)
	}
}

// TestIsolationFactsSupersedeScoped proves Supersede cannot reach across
// scopes: a fact current under scope A is invisible to a Supersede issued under
// scope B (ErrFactNotFound), and B's attempt neither archives A's fact nor
// leaks A's row. It also confirms a cross-scope newFact is rejected up front.
func TestIsolationFactsSupersedeScoped(t *testing.T) {
	st := newFactsStore(t)
	ctx := context.Background()
	a, b, _, _ := isoScopes(t)

	if err := st.Save(ctx, a, factFor(a, "a-old", "alice original preference")); err != nil {
		t.Fatalf("Save a-old: %v", err)
	}

	// B tries to supersede A's fact id. The scope filter in the UPDATE WHERE
	// means the row is not current *within B*, so it must be not-found.
	err := st.Supersede(ctx, b, "a-old", factFor(b, "b-new", "bob replacement"))
	if !errors.Is(err, ErrFactNotFound) {
		t.Fatalf("cross-scope Supersede = %v, want ErrFactNotFound", err)
	}

	// A's fact is still current and still searchable only under A.
	got, err := st.Search(ctx, a, "preference", 10)
	if err != nil {
		t.Fatalf("Search a: %v", err)
	}
	if len(got) != 1 || got[0].ID != "a-old" {
		t.Fatalf("A's fact should remain current after B's failed supersede, got %+v", got)
	}

	// A cross-scope newFact is rejected before any archival happens.
	if err := st.Supersede(ctx, a, "a-old", factFor(b, "b-new", "x")); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("Supersede with a foreign-scope newFact = %v, want ErrScopeMismatch", err)
	}
}

// TestIsolationFactsDeleteScoped proves the cleanup write honors the isolation
// gate: a Delete issued under scope A for an id that belongs to scope B is a
// no-op (the WHERE binds tenant_id AND user_id), so B's fact survives.
func TestIsolationFactsDeleteScoped(t *testing.T) {
	st := newFactsStore(t)
	ctx := context.Background()
	a, b, _, _ := isoScopes(t)

	if err := st.Save(ctx, b, factFor(b, "b-keep", "bob durable fact")); err != nil {
		t.Fatalf("Save b-keep: %v", err)
	}
	// A deletes B's id — must not touch B's row.
	if err := st.Delete(ctx, a, []string{"b-keep"}); err != nil {
		t.Fatalf("Delete under A: %v", err)
	}
	got, err := st.Search(ctx, b, "durable", 10)
	if err != nil {
		t.Fatalf("Search b: %v", err)
	}
	if len(got) != 1 || got[0].ID != "b-keep" {
		t.Fatalf("B's fact was removed by A's scoped Delete (isolation leak on the cleanup write), got %+v", got)
	}
}

// TestIsolationFactsConcurrent hammers the store with parallel writers and
// readers under scopes A and B and asserts no reader ever observes the other
// scope's rows. Run under -race, it also guards against data races in the Go
// access path around the shared DB handle.
func TestIsolationFactsConcurrent(t *testing.T) {
	st := newFactsStore(t)
	ctx := context.Background()
	a, b, _, _ := isoScopes(t)

	const perScope = 50
	var wg sync.WaitGroup

	writer := func(sc Scope, tag string) {
		defer wg.Done()
		for i := 0; i < perScope; i++ {
			f := factFor(sc, fmt.Sprintf("%s-%d", tag, i), "shared concurrent keyword payload")
			if err := st.Save(ctx, sc, f); err != nil {
				t.Errorf("Save %s-%d: %v", tag, i, err)
				return
			}
		}
	}
	// readerScope must only ever see forbiddenTenant/forbiddenUser NEVER.
	reader := func(sc Scope, other Scope, tag string) {
		defer wg.Done()
		for i := 0; i < perScope; i++ {
			got, err := st.Search(ctx, sc, "concurrent keyword", 100)
			if err != nil {
				t.Errorf("Search %s: %v", tag, err)
				return
			}
			for _, sf := range got {
				if sf.TenantID == other.Tenant() && sf.UserID == other.User() {
					t.Errorf("CROSS-SCOPE LEAK: reader %s saw a fact scoped to (%q,%q): %+v",
						tag, other.Tenant(), other.User(), sf)
					return
				}
				if sf.TenantID != sc.Tenant() || sf.UserID != sc.User() {
					t.Errorf("reader %s saw a fact outside its scope: %+v", tag, sf)
					return
				}
			}
		}
	}

	wg.Add(4)
	go writer(a, "A")
	go writer(b, "B")
	go reader(a, b, "readA")
	go reader(b, a, "readB")
	wg.Wait()

	// Final assertion: each scope sees exactly its own perScope rows.
	for _, sc := range []Scope{a, b} {
		got, err := st.Search(ctx, sc, "concurrent keyword", 1000)
		if err != nil {
			t.Fatalf("final Search: %v", err)
		}
		if len(got) != perScope {
			t.Fatalf("scope (%q,%q) final Search returned %d, want %d",
				sc.Tenant(), sc.User(), len(got), perScope)
		}
	}
}

// TestIsolationFactsExportAllIsTheSoleUnscopedRead is the "omitted-filter
// regression" enumeration guard for the Store interface. ExportAll is the ONE
// deliberate cross-tenant read (consumed only by the offline sleep-cycle
// consolidator, which re-partitions by tenant before use). Every OTHER method
// that returns Fact rows MUST take a Scope argument. If someone adds a second
// unscoped read method, this test fails — forcing the intent to be reviewed and
// documented rather than silently shipping a leak-shaped API.
func TestIsolationFactsExportAllIsTheSoleUnscopedRead(t *testing.T) {
	storeType := reflect.TypeOf((*Store)(nil)).Elem()
	scopeType := reflect.TypeOf(Scope{})
	factSlice := reflect.TypeOf([]Fact(nil))
	scoredSlice := reflect.TypeOf([]ScoredFact(nil))

	returnsRows := func(m reflect.Method) bool {
		for i := 0; i < m.Type.NumOut(); i++ {
			out := m.Type.Out(i)
			if out == factSlice || out == scoredSlice {
				return true
			}
		}
		return false
	}
	takesScope := func(m reflect.Method) bool {
		// Interface method types have no receiver in In(); params start at In(0).
		for i := 0; i < m.Type.NumIn(); i++ {
			if m.Type.In(i) == scopeType {
				return true
			}
		}
		return false
	}

	var unscopedReaders []string
	for i := 0; i < storeType.NumMethod(); i++ {
		m := storeType.Method(i)
		if !returnsRows(m) {
			continue
		}
		if !takesScope(m) {
			unscopedReaders = append(unscopedReaders, m.Name)
		}
	}

	if len(unscopedReaders) != 1 || unscopedReaders[0] != "ExportAll" {
		t.Fatalf("expected ExportAll to be the ONLY unscoped Store read; found unscoped row-returning methods: %v\n"+
			"A new unscoped read is a cross-tenant leak surface — add a Scope arg, or if it is a second reviewed "+
			"cross-tenant export, update this guard AND the isolation docs deliberately.", unscopedReaders)
	}
}
