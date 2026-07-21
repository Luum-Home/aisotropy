// rqlite_reset.go scopes and, when appropriate, clears the arena's own facts
// rows in the real networked rqlite tier before this process's mode runs.
//
// History — this file originally issued an unconditional `DROP TABLE facts` /
// `DROP TABLE facts_fts` on EVERY adapter process boot. That was a workaround
// for a real (at the time) aisotropy defect: internal/facts/rqlite.go's
// migrate() bundled its `ALTER TABLE facts ADD COLUMN` into ONE rqlite
// transaction, so re-running migrate against an already-migrated schema
// aborted the whole request before the per-statement duplicate-column guard
// (mirrored from internal/facts/sqlite.go) could ever see it — every boot
// after the first failed outright.
//
// That bug is FIXED. Commit 64531e0 ("fix(facts): idempotent rqlite
// migration — re-boot no longer fails on ALTER ADD COLUMN", 2026-07-17
// 20:18:14 -0300) rewrote migrate() to run each schema statement as its own
// request, tolerating the duplicate-column error at both the request and
// per-statement level (execToleratingDupColumn, internal/facts/rqlite.go:61-89).
// This file was created 57 minutes LATER, in commit 4bf2c80 (2026-07-17
// 21:15:47 -0300), with a comment citing the already-fixed bug as the reason
// for the wipe — a stale justification caught by
// docs/architecture/arena-memory-forensics.md §3.3. See also
// internal/facts/rqlite_migrate_test.go's TestRqlite_DoubleMigrateSucceeds and
// TestRqlite_MigrateToleratesDuplicateColumn, which pin the fix so it can
// never again be re-broken unnoticed.
//
// Worse than stale, the unconditional wipe was ACTIVE HARM: every arena mode
// that proves cross-process facts recall (`memory`, `memory_scoped`,
// `isolation_leak`, `crash_recovery` — see mode_memory.go, mode_scoped.go,
// mode_crash.go) depends on aisotropy's transactional outbox having written a
// fact to rqlite during a PERSIST process invocation, then a SEPARATE RECALL
// process invocation hydrating it back. The old resetRqliteFacts() ran on
// EVERY boot, including the recall process's own — deleting whatever the
// persist process had just written, before the recall mode ever ran. The
// facts tier therefore contributed NOTHING to cross-process recall in the
// arena (arena-memory-forensics.md §3.3, §5.1).
//
// Fix: scope the reset to the (tenant_id, user_id) rows this process's
// request actually uses — mirroring internal/facts's own EraseScope
// semantics (`DELETE FROM facts WHERE tenant_id = ? AND user_id = ?`, schema.go's
// eraseScopeSQL — never a DROP TABLE, never another adapter's table) — and
// gate it on a per-(rqlite endpoint, tenant, user, session) marker file,
// mirroring datadir_isolation.go's TTL mechanism for the BadgerDB
// working-memory tier:
//
//   - marker fresh (this (tenant,user,session) triple was touched within
//     factsMarkerTTL) -> this is the SAME task's later process (a recall
//     following its persist, or a retry) -> LEAVE the rows alone so the
//     outbox-written fact can actually be recalled.
//   - marker absent or stale -> a genuinely NEW task (even one reusing the
//     SAME tenant/user identity, e.g. `memory` mode's constant arena/live
//     scope across every memory task, see mode_memory.go) -> erase just that
//     (tenant,user) scope, so an EARLIER, unrelated task's facts never leak
//     into a LATER one under the same identity. facts.Scope carries no
//     session column (internal/facts/facts.go), so this task-boundary
//     isolation is not free without an explicit marker — the whole reason
//     this file exists.
//
// No-op when RQLITE_URL is unset: the adapter is then on the embedded-SQLite
// facts path, whose migrate() is independently idempotent
// (internal/facts/sqlite.go:74-87) and whose store is process-local anyway,
// so there is nothing cross-process to isolate.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// factsMarkerTTL bounds how long a (tenant,user,session) triple is treated as
// "the same task's still-in-flight processes" — mirrors dataDirTTL
// (datadir_isolation.go): comfortably longer than a persist->recall gap or a
// rate-limit backoff retry, far shorter than the gap between independent
// benchmark runs that might reuse the same identity.
const factsMarkerTTL = 30 * time.Minute

// factsMarkerDirEnv overrides the marker directory. Used by tests so they
// never touch (or race on) the real host temp dir a concurrently running
// arena process might also be using; unset in production — run.sh never sets it.
const factsMarkerDirEnv = "AISOTROPY_ARENA_FACTS_MARKER_DIR"

// factsMarkerDir returns the directory holding one marker file per
// (rqlite endpoint, tenant, user, session) combination this process has
// already reset facts for.
func factsMarkerDir() string {
	if d := os.Getenv(factsMarkerDirEnv); d != "" {
		return d
	}
	return filepath.Join(os.TempDir(), "aisotropy-arena-facts-markers")
}

// factsMarkerPath derives a filesystem-safe marker path. Hashed (not the raw
// identity) so arbitrary session_id content from a task payload can never
// produce a path-traversal or invalid-filename issue; the rqlite endpoint
// host is included so a stale marker from one rqlite target (e.g. a previous
// docker-compose instance on a different port) can never suppress a reset
// against a different one.
func factsMarkerPath(rqliteHost, tenant, user, session string) string {
	h := sha256.Sum256([]byte(rqliteHost + "\x00" + tenant + "\x00" + user + "\x00" + session))
	return filepath.Join(factsMarkerDir(), hex.EncodeToString(h[:])+".marker")
}

// factsIdentity resolves the (tenant, user) pair req's facts rows live under.
// Mirrors every mode's own resolution: the scoped modes (memory_scoped,
// isolation_leak, crash_recovery — see mode_scoped.go's scopeIdentity)
// always carry an explicit non-empty tenant_id/user_id; every other mode
// (qa, rag, tools, memory, structured, stream, ...) drives aisotropy under
// the fixed arena/live identity (see e.g. mode_qa.go, mode_memory.go).
func factsIdentity(req *request) (tenant, user string) {
	if strings.TrimSpace(req.TenantID) != "" && strings.TrimSpace(req.UserID) != "" {
		return req.TenantID, req.UserID
	}
	return arenaTenant, arenaUser
}

// resetRqliteFacts scopes and, when this is a genuinely new task, clears the
// arena's own facts rows for req's (tenant_id, user_id) identity in the
// configured rqlite. See the package doc comment above for why this is
// scoped-and-conditional rather than an unconditional DROP TABLE. No-op when
// RQLITE_URL is unset. Best-effort throughout: any error is logged and
// ignored — a failed reset must not fail the run — and a failed erase leaves
// the marker unwritten so a LATER boot retries rather than wrongly assuming
// the scope is already clean.
func resetRqliteFacts(req *request) {
	raw := os.Getenv("RQLITE_URL")
	if raw == "" {
		return
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		logf("rqlite facts reset skipped (unparseable RQLITE_URL)")
		return
	}
	tenant, user := factsIdentity(req)
	if tenant == "" || user == "" {
		logf("rqlite facts reset skipped (no tenant/user identity on this request)")
		return
	}

	marker := factsMarkerPath(u.Host, tenant, user, req.SessionID)
	info, statErr := os.Stat(marker)
	fresh := statErr == nil && time.Since(info.ModTime()) < factsMarkerTTL
	if fresh {
		// Same task's later process (e.g. a recall following its persist, or
		// a retry) — leave the rows so the outbox-written fact can actually
		// be recalled. Refresh the marker so the window keeps extending.
		if err := touchFactsMarker(marker); err != nil {
			logf("rqlite facts marker refresh failed (tenant=%s user=%s): %v", tenant, user, err)
		}
		return
	}

	if err := eraseRqliteFactsScope(u, tenant, user); err != nil {
		logf("rqlite facts scope reset failed (tenant=%s user=%s): %v — leaving rows as-is", tenant, user, err)
		return // do not write the marker: a future boot should retry the erase
	}
	if err := touchFactsMarker(marker); err != nil {
		logf("rqlite facts marker write failed (tenant=%s user=%s): %v", tenant, user, err)
	}
	logf("rqlite facts scope reset (tenant=%s user=%s) — new task/session, prior rows under this identity cleared", tenant, user)
}

// eraseRqliteFactsScope issues a single parameterized DELETE against the
// configured rqlite's /db/execute endpoint, scoped to (tenant, user) — the
// identical predicate internal/facts's own EraseScope uses
// (internal/facts/schema.go's eraseScopeSQL), so the AFTER DELETE trigger on
// `facts` (schema.go's facts_ad) keeps the facts_fts external-content index
// in sync automatically. It only ever touches aisotropy's own `facts` table,
// never another adapter's (arena_pydantic_memory, arena_genkit_go_memory,
// ai_sdk_arena_memory, ...).
func eraseRqliteFactsScope(u *url.URL, tenant, user string) error {
	endpoint := u.Scheme + "://" + u.Host + "/db/execute"
	body, err := json.Marshal([][]any{
		{"DELETE FROM facts WHERE tenant_id = ? AND user_id = ?", tenant, user},
	})
	if err != nil {
		return fmt.Errorf("marshal erase-scope statement: %w", err)
	}
	httpReq, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build erase-scope request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(httpReq)
	if err != nil {
		return fmt.Errorf("erase-scope request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("rqlite /db/execute returned status %d", resp.StatusCode)
	}
	return nil
}

// touchFactsMarker creates (or refreshes the mtime of) the marker file at
// path, creating its parent directory as needed.
func touchFactsMarker(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir marker dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339Nano)+"\n"), 0o644); err != nil {
		return fmt.Errorf("write marker: %w", err)
	}
	return nil
}
