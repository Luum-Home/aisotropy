package facts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// This file exercises the NETWORKED rqlite facts backend (rqlite.go) against a
// faithful in-memory HTTP fake of an rqlite node, using only net/http/httptest.
// The fake speaks the same /status, /db/execute and /db/query protocol gorqlite
// uses (cluster discovery -> leader api_addr -> POSTed [][]statement bodies), so
// the RqliteStore's real request-building, response-parsing, scope filtering and
// error handling run end to end. The live-node round-trip (Raft, real FTS5 bm25)
// stays behind the `manual` build tag in rqlite_manual_test.go.

// storedRow mirrors one row of the facts table inside the fake node.
type storedRow struct {
	id, tenant, user, content, supersededBy string
	importance                              float64
	createdMS, updatedMS                    int64
}

// fakeRqlite is a minimal, stateful in-memory rqlite node over HTTP. It handles
// the subset of the API the RqliteStore uses and applies the real (tenant,user,
// superseded_by) scope filter, so scope isolation is genuinely exercised.
type fakeRqlite struct {
	srv  *httptest.Server
	rows map[string]storedRow

	// forceStatus, when non-zero, makes every /db/* call return that HTTP code
	// (used to drive the store's transport-error branches).
	forceStatus int
	// resultError, when set, is returned as a per-statement "error" for the next
	// /db/* call (used to drive the store's per-result error branches).
	resultError string
	// notOpen makes /status report a store that has no leader yet.
	notOpen bool
	// onSearchPool, when set, is called with the candidate-pool argument the
	// store requested on a searchSQL query (args[3]) — lets a test assert the
	// pool-floor computation without a live node.
	onSearchPool func(int)
	// malformedRows makes /db/query return one row whose created_at cell is a
	// non-numeric string, so the store's row Scan into an int64 fails — driving
	// the scan-error branch that guards against a garbage backend response.
	malformedRows bool
	// ddlError, when set, is returned as the per-statement result "error" for any
	// statement containing "ADD COLUMN" — reproducing how a real rqlite reports the
	// idempotent ALTER's "duplicate column" failure when that statement is run in
	// its OWN single-statement request (results[i].error, the SQLite message
	// preserved), which is exactly what migrate does per statement so the guard can
	// see the text. Drives the migrate-idempotency regression in rqlite_migrate_test.go.
	ddlError string
}

func newFakeRqlite(t *testing.T) *fakeRqlite {
	t.Helper()
	f := &fakeRqlite{rows: map[string]storedRow{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", f.handleStatus)
	mux.HandleFunc("/db/execute", f.handleExecute)
	mux.HandleFunc("/db/query", f.handleQuery)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// url is the connection string handed to OpenRqlite.
func (f *fakeRqlite) url() string { return f.srv.URL }

// host is the httptest server's host:port, advertised as the leader's api_addr
// so gorqlite's cluster discovery follows it back to this same fake.
func (f *fakeRqlite) host() string { return strings.TrimPrefix(f.srv.URL, "http://") }

func (f *fakeRqlite) handleStatus(w http.ResponseWriter, _ *http.Request) {
	if f.notOpen {
		// Empty leader -> gorqlite's parseLeaderRaftAddr returns "store is not open".
		_, _ = w.Write([]byte(`{"store":{"leader":"","metadata":{}}}`))
		return
	}
	resp := map[string]any{
		"store": map[string]any{
			"leader": "raft-leader-1",
			"metadata": map[string]any{
				"raft-leader-1": map[string]any{"api_addr": f.host()},
			},
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// decodeStatements parses the [][]interface{} body gorqlite POSTs.
func decodeStatements(r *http.Request) [][]any {
	var stmts [][]any
	_ = json.NewDecoder(r.Body).Decode(&stmts)
	return stmts
}

func (f *fakeRqlite) handleExecute(w http.ResponseWriter, r *http.Request) {
	if f.forceStatus != 0 {
		http.Error(w, "boom", f.forceStatus)
		return
	}
	stmts := decodeStatements(r)
	results := make([]map[string]any, 0, len(stmts))
	for _, st := range stmts {
		query, _ := st[0].(string)
		if f.ddlError != "" && strings.Contains(query, "ADD COLUMN") {
			// Per-statement result error with the SQLite text preserved — how
			// rqlite reports a single-statement ALTER's duplicate-column failure
			// (results[i].error), which migrate must recognise and tolerate.
			results = append(results, map[string]any{"error": f.ddlError})
			continue
		}
		if f.resultError != "" {
			results = append(results, map[string]any{"error": f.resultError})
			continue
		}
		args := st[1:]
		switch query {
		case upsertSQL:
			f.applyUpsert(args)
			results = append(results, map[string]any{"rows_affected": 1, "last_insert_id": 1})
		case markSupersededSQL:
			results = append(results, map[string]any{"rows_affected": f.applySupersede(args)})
		case deleteSQL:
			results = append(results, map[string]any{"rows_affected": f.applyDelete(args)})
		default:
			// Schema/DDL statements: acknowledge with an empty result.
			results = append(results, map[string]any{})
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
}

func (f *fakeRqlite) handleQuery(w http.ResponseWriter, r *http.Request) {
	if f.forceStatus != 0 {
		http.Error(w, "boom", f.forceStatus)
		return
	}
	stmts := decodeStatements(r)
	st := stmts[0]
	query, _ := st[0].(string)
	args := st[1:]

	if f.resultError != "" {
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"error": f.resultError}}})
		return
	}

	switch query {
	case searchSQL:
		if f.onSearchPool != nil && len(args) >= 4 {
			f.onSearchPool(int(asFloat(args[3])))
		}
		if f.malformedRows {
			f.writeRows(w,
				[]string{"id", "tenant_id", "user_id", "content", "importance", "created_at", "updated_at", "superseded_by", "relevance"},
				[][]any{{"id1", "acme", "alice", "c", 0.5, "not-a-number", 0, "", -1.0}})
			return
		}
		f.writeRows(w,
			[]string{"id", "tenant_id", "user_id", "content", "importance", "created_at", "updated_at", "superseded_by", "relevance"},
			f.searchRows(args))
	case exportAllSQL:
		if f.malformedRows {
			f.writeRows(w,
				[]string{"id", "tenant_id", "user_id", "content", "importance", "created_at", "updated_at", "superseded_by"},
				[][]any{{"id1", "acme", "alice", "c", 0.5, "not-a-number", 0, ""}})
			return
		}
		f.writeRows(w,
			[]string{"id", "tenant_id", "user_id", "content", "importance", "created_at", "updated_at", "superseded_by"},
			f.exportRows())
	default:
		f.writeRows(w, []string{"x"}, nil)
	}
}

func (f *fakeRqlite) writeRows(w http.ResponseWriter, columns []string, values [][]any) {
	res := map[string]any{"columns": columns}
	if len(values) > 0 {
		res["values"] = values
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{res}})
}

func asFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		x, _ := n.Float64()
		return x
	}
	return 0
}

func (f *fakeRqlite) applyUpsert(args []any) {
	// Arg order mirrors upsertSQL: id, tenant, user, content, fts_content,
	// importance, created, updated, superseded_by. The fake substring-matches
	// the display `content`, so args[4] (fts_content) is accepted but unused.
	row := storedRow{
		id:           args[0].(string),
		tenant:       args[1].(string),
		user:         args[2].(string),
		content:      args[3].(string),
		importance:   asFloat(args[5]),
		createdMS:    int64(asFloat(args[6])),
		updatedMS:    int64(asFloat(args[7])),
		supersededBy: args[8].(string),
	}
	// Preserve created_at on upsert, mirroring the ON CONFLICT clause.
	if existing, ok := f.rows[row.id]; ok {
		row.createdMS = existing.createdMS
	}
	f.rows[row.id] = row
}

// applySupersede implements markSupersededSQL: args = newID, updatedMS, oldID, tenant, user.
func (f *fakeRqlite) applySupersede(args []any) int {
	newID := args[0].(string)
	updatedMS := int64(asFloat(args[1]))
	oldID := args[2].(string)
	tenant := args[3].(string)
	user := args[4].(string)
	row, ok := f.rows[oldID]
	if !ok || row.tenant != tenant || row.user != user || row.supersededBy != "" {
		return 0
	}
	row.supersededBy = newID
	row.updatedMS = updatedMS
	f.rows[oldID] = row
	return 1
}

// applyDelete implements deleteSQL: args = tenant, user, id.
func (f *fakeRqlite) applyDelete(args []any) int {
	tenant := args[0].(string)
	user := args[1].(string)
	id := args[2].(string)
	row, ok := f.rows[id]
	if !ok || row.tenant != tenant || row.user != user {
		return 0
	}
	delete(f.rows, id)
	return 1
}

// searchRows implements searchSQL scope + FTS filtering: args = match, tenant, user, pool.
func (f *fakeRqlite) searchRows(args []any) [][]any {
	match := args[0].(string)
	tenant := args[1].(string)
	user := args[2].(string)
	terms := parseFTSTerms(match)
	var out [][]any
	for _, row := range f.rows {
		if row.tenant != tenant || row.user != user || row.supersededBy != "" {
			continue
		}
		if !contentMatches(row.content, terms) {
			continue
		}
		out = append(out, []any{row.id, row.tenant, row.user, row.content, row.importance,
			row.createdMS, row.updatedMS, row.supersededBy, -1.0 /* bm25 relevance */})
	}
	return out
}

func (f *fakeRqlite) exportRows() [][]any {
	var out [][]any
	for _, row := range f.rows {
		if row.supersededBy != "" {
			continue
		}
		out = append(out, []any{row.id, row.tenant, row.user, row.content, row.importance,
			row.createdMS, row.updatedMS, row.supersededBy})
	}
	return out
}

// parseFTSTerms extracts the bare words from a `"a" OR "b"` FTS match string.
func parseFTSTerms(match string) []string {
	var terms []string
	for _, part := range strings.Split(match, " OR ") {
		terms = append(terms, strings.Trim(part, `"`))
	}
	return terms
}

func contentMatches(content string, terms []string) bool {
	lc := strings.ToLower(content)
	for _, t := range terms {
		if t != "" && strings.Contains(lc, t) {
			return true
		}
	}
	return false
}

// --- Tests ---------------------------------------------------------------

func openFake(t *testing.T, f *fakeRqlite) *RqliteStore {
	t.Helper()
	st, err := OpenRqlite(f.url())
	if err != nil {
		t.Fatalf("OpenRqlite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestRqlite_OpenAndMigrate(t *testing.T) {
	f := newFakeRqlite(t)
	openFake(t, f) // OpenRqlite runs migrate() over the schema statements.
}

func TestRqlite_OpenErrorOnUnreachable(t *testing.T) {
	// A syntactically valid URL that refuses connections: cluster discovery fails.
	if _, err := OpenRqlite("http://127.0.0.1:1"); err == nil {
		t.Fatalf("expected open error against an unreachable node")
	}
}

func TestRqlite_OpenErrorWhenStoreNotOpen(t *testing.T) {
	f := newFakeRqlite(t)
	f.notOpen = true
	if _, err := OpenRqlite(f.url()); err == nil {
		t.Fatalf("expected open error when /status reports no leader")
	}
}

func TestRqlite_MigrateResultError(t *testing.T) {
	f := newFakeRqlite(t)
	f.resultError = "disk full"
	// migrate runs during OpenRqlite; a per-statement error must fail the open.
	if _, err := OpenRqlite(f.url()); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("expected migrate statement error, got %v", err)
	}
}

func TestRqlite_SaveAndSearchWithScopeIsolation(t *testing.T) {
	f := newFakeRqlite(t)
	st := openFake(t, f)
	ctx := context.Background()

	alice := Scope{tenant: "acme", user: "alice"}
	bob := Scope{tenant: "acme", user: "bob"}

	if err := st.Save(ctx, alice, Fact{ID: "a1", TenantID: "acme", UserID: "alice",
		Content: "the user is migrating billing to postgres", Importance: 0.6}); err != nil {
		t.Fatalf("Save alice: %v", err)
	}
	if err := st.Save(ctx, bob, Fact{ID: "b1", TenantID: "acme", UserID: "bob",
		Content: "the user is migrating billing to postgres", Importance: 0.9}); err != nil {
		t.Fatalf("Save bob: %v", err)
	}

	got, err := st.Search(ctx, alice, "migrating billing", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].UserID != "alice" || got[0].ID != "a1" {
		t.Fatalf("scope isolation broken: %+v", got)
	}
	if got[0].Score == 0 {
		t.Fatalf("expected a computed score, got 0")
	}
}

func TestRqlite_SaveScopeMismatch(t *testing.T) {
	f := newFakeRqlite(t)
	st := openFake(t, f)
	err := st.Save(context.Background(), Scope{tenant: "acme", user: "alice"},
		Fact{ID: "x", TenantID: "evil", UserID: "alice", Content: "c"})
	if err != ErrScopeMismatch {
		t.Fatalf("got %v, want ErrScopeMismatch", err)
	}
}

func TestRqlite_SearchEmptyQueryAndZeroLimit(t *testing.T) {
	f := newFakeRqlite(t)
	st := openFake(t, f)
	sc := Scope{tenant: "acme", user: "alice"}
	if got, err := st.Search(context.Background(), sc, "  ??  ", 5); err != nil || got != nil {
		t.Fatalf("empty match: got %v (%v)", got, err)
	}
	if got, err := st.Search(context.Background(), sc, "real", 0); err != nil || got != nil {
		t.Fatalf("zero limit: got %v (%v)", got, err)
	}
}

func TestRqlite_SupersedeTransactional(t *testing.T) {
	f := newFakeRqlite(t)
	st := openFake(t, f)
	ctx := context.Background()
	sc := Scope{tenant: "acme", user: "alice"}

	if err := st.Save(ctx, sc, Fact{ID: "a", TenantID: "acme", UserID: "alice",
		Content: "user migrating billing database", Importance: 0.5}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	newF := Fact{ID: "a2", TenantID: "acme", UserID: "alice",
		Content: "user finished migrating billing", Importance: 0.7}
	if err := st.Supersede(ctx, sc, "a", newF); err != nil {
		t.Fatalf("Supersede: %v", err)
	}

	got, err := st.Search(ctx, sc, "migrating billing", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].ID != "a2" {
		t.Fatalf("after supersede want only a2, got %+v", got)
	}
}

func TestRqlite_SupersedeNotFound(t *testing.T) {
	f := newFakeRqlite(t)
	st := openFake(t, f)
	sc := Scope{tenant: "acme", user: "alice"}
	// Target does not exist -> rows_affected 0 -> ErrFactNotFound.
	err := st.Supersede(context.Background(), sc, "missing",
		Fact{ID: "n", TenantID: "acme", UserID: "alice", Content: "c"})
	if err != ErrFactNotFound {
		t.Fatalf("got %v, want ErrFactNotFound", err)
	}
}

func TestRqlite_SupersedeScopeMismatch(t *testing.T) {
	f := newFakeRqlite(t)
	st := openFake(t, f)
	err := st.Supersede(context.Background(), Scope{tenant: "acme", user: "alice"}, "old",
		Fact{ID: "n", TenantID: "evil", UserID: "alice", Content: "c"})
	if err != ErrScopeMismatch {
		t.Fatalf("got %v, want ErrScopeMismatch", err)
	}
}

func TestRqlite_ExportAll(t *testing.T) {
	f := newFakeRqlite(t)
	st := openFake(t, f)
	ctx := context.Background()
	a := Scope{tenant: "acme", user: "alice"}
	g := Scope{tenant: "globex", user: "bob"}
	_ = st.Save(ctx, a, Fact{ID: "z", TenantID: "acme", UserID: "alice", Content: "one"})
	_ = st.Save(ctx, g, Fact{ID: "a", TenantID: "globex", UserID: "bob", Content: "two"})

	all, err := st.ExportAll(ctx)
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ExportAll returned %d, want 2 across tenants", len(all))
	}
}

func TestRqlite_DeleteScoped(t *testing.T) {
	f := newFakeRqlite(t)
	st := openFake(t, f)
	ctx := context.Background()
	sc := Scope{tenant: "acme", user: "alice"}

	// Empty ids is a no-op.
	if err := st.Delete(ctx, sc, nil); err != nil {
		t.Fatalf("Delete(nil): %v", err)
	}
	_ = st.Save(ctx, sc, Fact{ID: "d1", TenantID: "acme", UserID: "alice", Content: "deletable keyword"})
	_ = st.Save(ctx, sc, Fact{ID: "d2", TenantID: "acme", UserID: "alice", Content: "keeper keyword"})
	if err := st.Delete(ctx, sc, []string{"d1"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ := st.Search(ctx, sc, "keyword", 5)
	if len(got) != 1 || got[0].ID != "d2" {
		t.Fatalf("after delete want only d2, got %+v", got)
	}
}

func TestRqlite_TransportErrors(t *testing.T) {
	ctx := context.Background()
	sc := Scope{tenant: "acme", user: "alice"}

	t.Run("save http 500", func(t *testing.T) {
		f := newFakeRqlite(t)
		st := openFake(t, f)
		f.forceStatus = http.StatusInternalServerError
		if err := st.Save(ctx, sc, Fact{ID: "x", TenantID: "acme", UserID: "alice", Content: "c"}); err == nil {
			t.Fatalf("expected transport error")
		}
	})
	t.Run("search http 500", func(t *testing.T) {
		f := newFakeRqlite(t)
		st := openFake(t, f)
		f.forceStatus = http.StatusInternalServerError
		if _, err := st.Search(ctx, sc, "real query", 5); err == nil {
			t.Fatalf("expected transport error")
		}
	})
	t.Run("export http 500", func(t *testing.T) {
		f := newFakeRqlite(t)
		st := openFake(t, f)
		f.forceStatus = http.StatusInternalServerError
		if _, err := st.ExportAll(ctx); err == nil {
			t.Fatalf("expected transport error")
		}
	})
	t.Run("delete http 500", func(t *testing.T) {
		f := newFakeRqlite(t)
		st := openFake(t, f)
		f.forceStatus = http.StatusInternalServerError
		if err := st.Delete(ctx, sc, []string{"x"}); err == nil {
			t.Fatalf("expected transport error")
		}
	})
}

func TestRqlite_SaveResultError(t *testing.T) {
	f := newFakeRqlite(t)
	st := openFake(t, f)
	f.resultError = "constraint failed"
	err := st.Save(context.Background(), Scope{tenant: "acme", user: "alice"},
		Fact{ID: "x", TenantID: "acme", UserID: "alice", Content: "c"})
	if err == nil || !strings.Contains(err.Error(), "constraint failed") {
		t.Fatalf("expected per-result error, got %v", err)
	}
}

func TestRqlite_ImportanceRankingRoundTrips(t *testing.T) {
	// Two matching facts, different importance: the store's Go-side re-ranking
	// (Relevance x Importance x Recency over identical bm25) must order the
	// higher-importance fact first, proving Search parses importance/timestamps.
	f := newFakeRqlite(t)
	st := openFake(t, f)
	ctx := context.Background()
	sc := Scope{tenant: "acme", user: "alice"}
	now := time.Now()
	_ = st.Save(ctx, sc, Fact{ID: "low", TenantID: "acme", UserID: "alice",
		Content: "keyword alpha", Importance: 0.2, CreatedAt: now, UpdatedAt: now})
	_ = st.Save(ctx, sc, Fact{ID: "high", TenantID: "acme", UserID: "alice",
		Content: "keyword beta", Importance: 0.95, CreatedAt: now, UpdatedAt: now})
	got, err := st.Search(ctx, sc, "keyword", 5)
	if err != nil || len(got) != 2 {
		t.Fatalf("Search: got %d (%v)", len(got), err)
	}
	if got[0].ID != "high" {
		t.Fatalf("expected higher-importance fact first, got order %s,%s", got[0].ID, got[1].ID)
	}
}

func TestRedactURL(t *testing.T) {
	// Credentials are masked.
	if got := redactURL("http://user:secret@localhost:4001"); strings.Contains(got, "secret") {
		t.Fatalf("password leaked in redactURL: %q", got)
	}
	// No credentials: returned roughly intact.
	if got := redactURL("http://localhost:4001"); !strings.Contains(got, "localhost:4001") {
		t.Fatalf("host lost in redactURL: %q", got)
	}
	// Malformed URL falls back to the placeholder.
	if got := redactURL("://bad::url"); got != "<rqlite>" {
		t.Fatalf("malformed url: got %q, want <rqlite>", got)
	}
}
