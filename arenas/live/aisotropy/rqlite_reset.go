// rqlite_reset.go works around a real aisotropy defect discovered while wiring
// the networked rqlite facts tier: internal/facts/rqlite.go's migrate() is NOT
// idempotent across process restarts. Its schema includes a bare
// `ALTER TABLE facts ADD COLUMN fts_content ...`; on a re-run the column already
// exists, and rqlite reports a TRANSACTION-level "there were 1 statement errors"
// that migrate returns BEFORE it can reach the per-statement duplicate-column
// guard it (correctly) applies on the embedded-SQLite path (internal/facts/
// sqlite.go). So the SECOND and every later boot against an already-migrated
// rqlite fails outright.
//
// The honest fix belongs in internal/facts/rqlite.go (mirror the SQLite tier's
// per-statement tolerance) — but internal/ is out of scope for this adapter. So
// this adapter keeps the REAL networked rqlite tier (per the arena brief) and
// resets ONLY aisotropy's own facts schema (the `facts` + `facts_fts` tables it
// owns) before each boot, so migrate always runs against a clean schema and the
// non-idempotent ALTER succeeds. It never touches other adapters' tables
// (arena_pydantic_memory, arena_genkit_go_memory, ai_sdk_arena_memory, ...).
//
// This does NOT weaken any mode: aisotropy still writes facts to real rqlite via
// its outbox and reads them via FTS5 during hydration within the run. Memory-mode
// cross-process recall does not depend on it either — that rides on the on-disk
// BadgerDB working-memory tier (AISOTROPY_DATA_DIR), which is never reset here.
package main

import (
	"bytes"
	"net/http"
	"net/url"
	"os"
	"time"
)

// resetRqliteFacts drops aisotropy's own facts schema in the configured rqlite so
// the non-idempotent migrate() re-creates it cleanly. No-op when RQLITE_URL is
// unset (the adapter is then on the embedded-SQLite facts path, whose migrate is
// already idempotent). Best-effort: any error is logged and ignored — a failed
// reset must not fail the run, and if rqlite is unreachable the runtime boot will
// surface that on its own.
func resetRqliteFacts() {
	raw := os.Getenv("RQLITE_URL")
	if raw == "" {
		return
	}
	// Build the execute endpoint from scheme+host only, dropping any path/query
	// (RQLITE_URL may carry gorqlite tuning params like ?disableClusterDiscovery
	// for the Docker path — those must not leak into this REST URL).
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		logf("rqlite reset skipped (unparseable RQLITE_URL)")
		return
	}
	endpoint := u.Scheme + "://" + u.Host + "/db/execute"
	// Drop `facts` first (removes its triggers that reference facts_fts), then the
	// FTS5 virtual table (which drops its own shadow tables).
	body := `[["DROP TABLE IF EXISTS facts"],["DROP TABLE IF EXISTS facts_fts"]]`
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader([]byte(body)))
	if err != nil {
		logf("rqlite reset skipped (build request): %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		logf("rqlite reset skipped (request failed): %v", err)
		return
	}
	_ = resp.Body.Close()
	logf("rqlite facts schema reset (workaround for non-idempotent internal/facts/rqlite migrate)")
}
