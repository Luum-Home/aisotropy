package facts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The migrate fast path exists because every rqlite write is a Raft-committed,
// fsync'd log entry, and migrate issues each of the 9 canonical schema
// statements as its OWN request. A long-lived server pays that once and never
// notices; a process-per-invocation workload pays it on every boot. The live
// arena is exactly that shape — a fresh aisotropyd per adapter call — so one
// board turned into hundreds of pointless schema round-trips and produced 14
// observed "ensure table: context deadline exceeded" failures once a shared
// rqlite was under concurrent load.
//
// These tests assert the optimisation in the only terms that matter: how many
// WRITES reach the server. They use a self-contained fake (not the shared
// fakeRqlite harness) so the write counter and the sentinel response can be
// controlled precisely, and so this file stays independent of that harness's
// evolution.

// countingRqlite is a minimal rqlite stand-in that counts /db/execute
// statements and answers the schema-sentinel query either way.
type countingRqlite struct {
	srv *httptest.Server

	mu sync.Mutex
	// writes counts STATEMENTS (not requests) sent to /db/execute — migrate
	// sends one statement per request, so for this test they coincide, but
	// counting statements is the honest measure of Raft work.
	writes int
	// schemaPresent decides what the sentinel query returns: one row (already
	// migrated) or none (fresh store).
	schemaPresent bool
}

func newCountingRqlite(t *testing.T, schemaPresent bool) *countingRqlite {
	t.Helper()
	f := &countingRqlite{schemaPresent: schemaPresent}

	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		host := strings.TrimPrefix(f.srv.URL, "http://")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"store": map[string]any{
				"leader": "raft-leader-1",
				"metadata": map[string]any{
					"raft-leader-1": map[string]any{"api_addr": host},
				},
			},
		})
	})
	mux.HandleFunc("/db/execute", func(w http.ResponseWriter, r *http.Request) {
		var stmts [][]any
		_ = json.NewDecoder(r.Body).Decode(&stmts)
		f.mu.Lock()
		f.writes += len(stmts)
		f.mu.Unlock()
		results := make([]map[string]any, 0, len(stmts))
		for range stmts {
			results = append(results, map[string]any{"rows_affected": 0})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	})
	mux.HandleFunc("/db/query", func(w http.ResponseWriter, r *http.Request) {
		var stmts [][]any
		_ = json.NewDecoder(r.Body).Decode(&stmts)
		values := [][]any{}
		if len(stmts) > 0 {
			if q, _ := stmts[0][0].(string); strings.Contains(q, "sqlite_master") && f.schemaPresent {
				values = [][]any{{1}}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{
				"columns": []string{"1"},
				"types":   []string{"integer"},
				"values":  values,
			}},
		})
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *countingRqlite) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes
}

// TestMigrate_FastPathSkipsWritesWhenSchemaPresent is the whole point: booting
// against an already-migrated store must cost ZERO writes.
func TestMigrate_FastPathSkipsWritesWhenSchemaPresent(t *testing.T) {
	f := newCountingRqlite(t, true)

	st, err := OpenRqlite(f.srv.URL)
	if err != nil {
		t.Fatalf("OpenRqlite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if got := f.writeCount(); got != 0 {
		t.Errorf("booting against a migrated store issued %d write(s); want 0 — "+
			"every one is a Raft-committed fsync paid on every process boot", got)
	}
}

// TestMigrate_FullPathWhenSchemaAbsent proves the fast path did not break the
// real thing: a fresh store must still receive the complete schema.
func TestMigrate_FullPathWhenSchemaAbsent(t *testing.T) {
	f := newCountingRqlite(t, false)

	st, err := OpenRqlite(f.srv.URL)
	if err != nil {
		t.Fatalf("OpenRqlite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	got := f.writeCount()
	if want := len(schemaStatements); got != want {
		t.Errorf("fresh store received %d schema statement(s); want %d — the fast "+
			"path must not skip a migration that has not happened", got, want)
	}
}

// TestMigrate_FastPathFailsOpen — the sentinel check is an optimisation, never a
// new failure mode. If the query itself errors (an old node, a transient blip),
// migrate must fall through to the full idempotent migration instead of
// aborting the boot.
func TestMigrate_FastPathFailsOpen(t *testing.T) {
	f := newCountingRqlite(t, false)
	// Replace the query handler with one that always fails.
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		host := strings.TrimPrefix(f.srv.URL, "http://")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"store": map[string]any{
				"leader":   "raft-leader-1",
				"metadata": map[string]any{"raft-leader-1": map[string]any{"api_addr": host}},
			},
		})
	})
	mux.HandleFunc("/db/query", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "sentinel query unavailable", http.StatusInternalServerError)
	})
	mux.HandleFunc("/db/execute", func(w http.ResponseWriter, r *http.Request) {
		var stmts [][]any
		_ = json.NewDecoder(r.Body).Decode(&stmts)
		f.mu.Lock()
		f.writes += len(stmts)
		f.mu.Unlock()
		results := make([]map[string]any, 0, len(stmts))
		for range stmts {
			results = append(results, map[string]any{"rows_affected": 0})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	})
	f.srv.Config.Handler = mux

	st, err := OpenRqlite(f.srv.URL)
	if err != nil {
		t.Fatalf("a failing sentinel check must not fail the boot: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if got := f.writeCount(); got != len(schemaStatements) {
		t.Errorf("after a failed sentinel check the full migration must run: got %d writes, want %d", got, len(schemaStatements))
	}
}
