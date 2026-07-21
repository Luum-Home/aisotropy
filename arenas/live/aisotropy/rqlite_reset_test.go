package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRqliteExecute is a minimal /db/execute stand-in: it just counts how
// many times it was hit and captures the last request body, so a test can
// assert whether resetRqliteFacts actually issued (or skipped) the erase.
type fakeRqliteExecute struct {
	srv       *httptest.Server
	calls     int32
	lastBody  []byte
	failEvery bool // when true, every call returns 500 (drives the failure path)
}

func newFakeRqliteExecute(t *testing.T) *fakeRqliteExecute {
	t.Helper()
	f := &fakeRqliteExecute{}
	mux := http.NewServeMux()
	mux.HandleFunc("/db/execute", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.calls, 1)
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		f.lastBody = body
		if f.failEvery {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"rows_affected": 0}}})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRqliteExecute) callCount() int { return int(atomic.LoadInt32(&f.calls)) }

// isolatedMarkerDir points AISOTROPY_ARENA_FACTS_MARKER_DIR at a fresh
// per-test directory so tests never share (or race on) the real host temp
// dir, mirroring datadir_isolation_test.go's use of t.TempDir() per test.
func isolatedMarkerDir(t *testing.T) {
	t.Helper()
	t.Setenv(factsMarkerDirEnv, filepath.Join(t.TempDir(), "facts-markers"))
}

func TestResetRqliteFacts_UnsetRqliteURL_NoOp(t *testing.T) {
	isolatedMarkerDir(t)
	t.Setenv("RQLITE_URL", "")

	req := &request{TenantID: "arena", UserID: "live", SessionID: "s1"}
	resetRqliteFacts(req) // must not panic or touch the filesystem

	if _, err := os.Stat(factsMarkerDir()); err == nil {
		t.Errorf("RQLITE_URL unset must be a complete no-op, but the marker dir was created")
	}
}

// TestResetRqliteFacts_FirstCall_ErasesScope is the base case: no marker
// exists yet, so this must be treated as a new task and the scope erased.
func TestResetRqliteFacts_FirstCall_ErasesScope(t *testing.T) {
	isolatedMarkerDir(t)
	f := newFakeRqliteExecute(t)
	t.Setenv("RQLITE_URL", f.srv.URL)

	req := &request{TenantID: "arena", UserID: "live", SessionID: "task-1"}
	resetRqliteFacts(req)

	if got := f.callCount(); got != 1 {
		t.Fatalf("expected exactly 1 erase call on a fresh (tenant,user,session), got %d", got)
	}
	var stmts [][]any
	if err := json.Unmarshal(f.lastBody, &stmts); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("expected exactly one statement, got %d", len(stmts))
	}
	query, _ := stmts[0][0].(string)
	if query != "DELETE FROM facts WHERE tenant_id = ? AND user_id = ?" {
		t.Errorf("unexpected statement: %q", query)
	}
	if len(stmts[0]) != 3 || stmts[0][1] != "arena" || stmts[0][2] != "live" {
		t.Errorf("unexpected bind args: %v", stmts[0][1:])
	}
}

// TestResetRqliteFacts_FreshMarker_SkipsErase is the actual fix under test:
// a SECOND boot for the SAME (tenant,user,session) shortly after the first —
// e.g. a memory-mode recall invocation immediately following its persist
// invocation — must NOT erase the rows the first boot's mode just wrote via
// the outbox. This is the exact bug arena-memory-forensics.md §3.3 found:
// the old unconditional DROP TABLE wiped this every time, so the facts tier
// never contributed to cross-process recall.
func TestResetRqliteFacts_FreshMarker_SkipsErase(t *testing.T) {
	isolatedMarkerDir(t)
	f := newFakeRqliteExecute(t)
	t.Setenv("RQLITE_URL", f.srv.URL)

	req := &request{TenantID: "arena", UserID: "live", SessionID: "task-1"}
	resetRqliteFacts(req) // invocation 1: persist boot
	if got := f.callCount(); got != 1 {
		t.Fatalf("invocation 1: expected 1 erase call, got %d", got)
	}

	resetRqliteFacts(req) // invocation 2: recall boot, same identity, moments later
	if got := f.callCount(); got != 1 {
		t.Fatalf("invocation 2 (fresh marker) must NOT erase — call count should stay 1, got %d", got)
	}
}

// TestResetRqliteFacts_StaleMarker_ErasesAgain: a marker older than
// factsMarkerTTL is treated as an abandoned/previous run, same as the
// datadir isolation mechanism's stale-marker handling.
func TestResetRqliteFacts_StaleMarker_ErasesAgain(t *testing.T) {
	isolatedMarkerDir(t)
	f := newFakeRqliteExecute(t)
	t.Setenv("RQLITE_URL", f.srv.URL)

	req := &request{TenantID: "arena", UserID: "live", SessionID: "task-1"}
	resetRqliteFacts(req)
	if got := f.callCount(); got != 1 {
		t.Fatalf("invocation 1: expected 1 erase call, got %d", got)
	}

	u, err := url.Parse(f.srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	marker := factsMarkerPath(u.Host, "arena", "live", "task-1")
	old := time.Now().Add(-2 * factsMarkerTTL)
	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	resetRqliteFacts(req)
	if got := f.callCount(); got != 2 {
		t.Fatalf("stale marker must trigger another erase — expected 2 calls, got %d", got)
	}
}

// TestResetRqliteFacts_DifferentSession_ErasesIndependently is the
// task-isolation guarantee this file exists to preserve: `memory` mode reuses
// the SAME (tenant=arena,user=live) identity across EVERY memory task
// (mode_memory.go), differentiated only by session_id. A fresh marker for
// task A's session must never suppress the erase for task B's DIFFERENT
// session under the same tenant/user — otherwise task B would silently
// inherit task A's facts (facts.Scope has no session column, so without this
// the isolation is not free).
func TestResetRqliteFacts_DifferentSession_ErasesIndependently(t *testing.T) {
	isolatedMarkerDir(t)
	f := newFakeRqliteExecute(t)
	t.Setenv("RQLITE_URL", f.srv.URL)

	reqA := &request{TenantID: "arena", UserID: "live", SessionID: "mem-h01-overwrite"}
	resetRqliteFacts(reqA)
	if got := f.callCount(); got != 1 {
		t.Fatalf("task A: expected 1 erase call, got %d", got)
	}

	reqB := &request{TenantID: "arena", UserID: "live", SessionID: "mem-h03-longhaul"}
	resetRqliteFacts(reqB)
	if got := f.callCount(); got != 2 {
		t.Fatalf("task B (different session, same tenant/user) must still erase — expected 2 calls, got %d", got)
	}
}

// TestResetRqliteFacts_EraseFailure_DoesNotWriteMarker: if the erase request
// itself fails, the marker must NOT be written — a later boot must retry the
// erase rather than wrongly treat the scope as already clean.
func TestResetRqliteFacts_EraseFailure_DoesNotWriteMarker(t *testing.T) {
	isolatedMarkerDir(t)
	f := newFakeRqliteExecute(t)
	f.failEvery = true
	t.Setenv("RQLITE_URL", f.srv.URL)

	req := &request{TenantID: "arena", UserID: "live", SessionID: "task-1"}
	resetRqliteFacts(req) // best-effort: must not panic despite the 500

	u, err := url.Parse(f.srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	marker := factsMarkerPath(u.Host, "arena", "live", "task-1")
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("a failed erase must not leave a marker behind (would suppress a needed retry)")
	}
}

// TestFactsIdentity covers the (tenant,user) fallback resolution: the scoped
// modes (memory_scoped, isolation_leak, crash_recovery) always carry an
// explicit identity; every other mode (qa, rag, tools, memory, ...) resolves
// to the fixed arena/live identity those modes actually drive aisotropy under.
func TestFactsIdentity(t *testing.T) {
	cases := []struct {
		name         string
		req          *request
		wantT, wantU string
	}{
		{"explicit tenant/user (scoped modes)", &request{TenantID: "tenantA", UserID: "userB"}, "tenantA", "userB"},
		{"empty tenant/user falls back to arena/live", &request{}, arenaTenant, arenaUser},
		{"tenant set but user empty still falls back (both required)", &request{TenantID: "tenantA"}, arenaTenant, arenaUser},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotT, gotU := factsIdentity(c.req)
			if gotT != c.wantT || gotU != c.wantU {
				t.Errorf("factsIdentity() = (%q,%q), want (%q,%q)", gotT, gotU, c.wantT, c.wantU)
			}
		})
	}
}
