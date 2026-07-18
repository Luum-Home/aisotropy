package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// rqlite gives the "memory" mode a REAL, networked, on-disk store: state
// written by one process invocation is read back by a completely separate
// later invocation, proving persistence rather than an in-memory dictionary
// (arenas/live/_shared/README.md "Memory-persistence & tool-execution
// conventions"). Talks to rqlite's documented HTTP API directly
// (POST /db/execute, POST /db/query) with plain net/http — rqlite ships no Go
// client in this repo's dependency graph, and the HTTP surface is small and
// stable, so adding a dependency for it would not buy anything.
const memoryTable = "arena_genkit_go_memory"

// ---- ARENA_STUB in-memory backing (stub.go sets stubMode from the env) ----
//
// When stubMode is true, every table below is backed by a package-level
// in-process map instead of a real HTTP round-trip to rqlite, so every
// rqlite-touching mode (memory/memory_scoped/isolation_leak/crash_recovery)
// runs fully offline. HONEST limitation: this can only ever prove
// WITHIN-PROCESS behavior — rqlite's whole point for "memory"/
// "crash_recovery" is CROSS-PROCESS persistence (a second, separate `run.sh`
// invocation recalling what an earlier, exited process wrote), and an
// in-memory map does not survive process exit. See README.md "ARENA_STUB".
var (
	stubStoreMu    sync.Mutex
	stubTurns      = map[string][]turn{} // memory table: session_id -> turns
	stubCrashTurns = map[string][]turn{} // crash-recovery table: session_id -> turns
	stubScoped     []stubScopedRow       // scoped-memory table: append-only, filtered on read
)

// stubScopedRow mirrors one row of arena_genkit_go_scoped_memory.
type stubScopedRow struct {
	tenantID, userID, productID, sessionID, fact string
}

type rqliteClient struct {
	baseURL string
	http    *http.Client
	stub    bool
}

func newRqliteClient(baseURL string) *rqliteClient {
	return &rqliteClient{baseURL: baseURL, http: &http.Client{Timeout: 15 * time.Second}, stub: stubMode}
}

// ensureTable creates the conversation-turn table if absent. Idempotent.
func (c *rqliteClient) ensureTable(ctx context.Context) error {
	if c.stub {
		return nil // the in-memory map needs no schema
	}
	stmt := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`, memoryTable)
	_, err := c.execute(ctx, []any{stmt})
	return err
}

// appendTurn persists one conversation turn for sessionID.
func (c *rqliteClient) appendTurn(ctx context.Context, sessionID, role, content string) error {
	if c.stub {
		stubStoreMu.Lock()
		stubTurns[sessionID] = append(stubTurns[sessionID], turn{Role: role, Content: content})
		stubStoreMu.Unlock()
		return nil
	}
	stmt := []any{
		fmt.Sprintf("INSERT INTO %s(session_id, role, content, created_at) VALUES(?, ?, ?, ?)", memoryTable),
		sessionID, role, content, time.Now().UnixMilli(),
	}
	_, err := c.execute(ctx, stmt)
	return err
}

// turn is one persisted conversation turn.
type turn struct {
	Role    string
	Content string
}

// loadTurns returns every turn previously persisted for sessionID, oldest
// first — the cross-process recall this mode exists to prove.
func (c *rqliteClient) loadTurns(ctx context.Context, sessionID string) ([]turn, error) {
	if c.stub {
		stubStoreMu.Lock()
		defer stubStoreMu.Unlock()
		out := make([]turn, len(stubTurns[sessionID]))
		copy(out, stubTurns[sessionID])
		return out, nil
	}
	stmt := []any{
		fmt.Sprintf("SELECT role, content FROM %s WHERE session_id = ? ORDER BY id ASC", memoryTable),
		sessionID,
	}
	rows, err := c.query(ctx, stmt)
	if err != nil {
		return nil, err
	}
	turns := make([]turn, 0, len(rows))
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		role, _ := row[0].(string)
		content, _ := row[1].(string)
		turns = append(turns, turn{Role: role, Content: content})
	}
	return turns, nil
}

// ---- Scoped (multi-tenant) memory table — memory_scoped / isolation_leak ----
//
// Genkit-Go has NO native multi-tenant memory-isolation primitive: the
// framework's own state hooks (this file, memory.go) are keyed however the
// caller chooses. The "memory" mode above keys by session_id ALONE — it
// proves cross-process persistence but not tenant isolation. This table
// hand-rolls a COMPOUND scope key (tenant_id, user_id, product_id,
// session_id) as real, parameterized SQL WHERE-clause columns — filtered
// server-side by rqlite during the query, not fetched-then-filtered in Go
// (the same "filter during the scan, not after" discipline this repo applies
// to Qdrant payload filtering, see .claude/rules/constitutional-gates.md).
// Getting this WHERE clause wrong (e.g. dropping tenant_id) is a real,
// silent cross-tenant leak — there is no framework guardrail catching it.
// See README.md "memory_scoped / isolation_leak" for the full honest note.
const scopedMemoryTable = "arena_genkit_go_scoped_memory"

// ensureScopedTable creates the compound-scope fact table if absent.
// Idempotent.
func (c *rqliteClient) ensureScopedTable(ctx context.Context) error {
	if c.stub {
		return nil
	}
	stmt := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tenant_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			product_id TEXT NOT NULL,
			session_id TEXT NOT NULL DEFAULT '',
			fact TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`, scopedMemoryTable)
	_, err := c.execute(ctx, []any{stmt})
	return err
}

// writeScopedFact persists one fact under the compound scope
// (tenantID, userID, productID, sessionID). Every scope component is bound
// as a SQL parameter, never string-concatenated.
func (c *rqliteClient) writeScopedFact(ctx context.Context, tenantID, userID, productID, sessionID, fact string) error {
	if c.stub {
		stubStoreMu.Lock()
		stubScoped = append(stubScoped, stubScopedRow{tenantID: tenantID, userID: userID, productID: productID, sessionID: sessionID, fact: fact})
		stubStoreMu.Unlock()
		return nil
	}
	stmt := []any{
		fmt.Sprintf("INSERT INTO %s(tenant_id, user_id, product_id, session_id, fact, created_at) VALUES(?, ?, ?, ?, ?, ?)", scopedMemoryTable),
		tenantID, userID, productID, sessionID, fact, time.Now().UnixMilli(),
	}
	_, err := c.execute(ctx, stmt)
	return err
}

// recallScopedFacts returns every fact previously written under the EXACT
// compound scope (tenantID, userID, productID, sessionID), newest first. The
// WHERE clause filters on all four columns in the query itself — this IS the
// isolation boundary: a scope that omits or gets tenant_id wrong will read
// (or leak) another tenant's rows, and nothing in Genkit-Go or rqlite
// prevents that by construction. The caller (this adapter) is fully
// responsible for supplying every scope column on every read and write.
func (c *rqliteClient) recallScopedFacts(ctx context.Context, tenantID, userID, productID, sessionID string) ([]string, error) {
	if c.stub {
		stubStoreMu.Lock()
		defer stubStoreMu.Unlock()
		var facts []string
		for i := len(stubScoped) - 1; i >= 0; i-- { // newest first, matching ORDER BY id DESC
			r := stubScoped[i]
			if r.tenantID == tenantID && r.userID == userID && r.productID == productID && r.sessionID == sessionID {
				facts = append(facts, r.fact)
			}
		}
		return facts, nil
	}
	stmt := []any{
		fmt.Sprintf("SELECT fact FROM %s WHERE tenant_id = ? AND user_id = ? AND product_id = ? AND session_id = ? ORDER BY id DESC", scopedMemoryTable),
		tenantID, userID, productID, sessionID,
	}
	rows, err := c.query(ctx, stmt)
	if err != nil {
		return nil, err
	}
	facts := make([]string, 0, len(rows))
	for _, row := range rows {
		if len(row) < 1 {
			continue
		}
		if f, ok := row[0].(string); ok {
			facts = append(facts, f)
		}
	}
	return facts, nil
}

// ---- Crash-recovery checkpoint table — crash_recovery mode ----
//
// A table SEPARATE from arena_genkit_go_memory above: crash_recovery mode
// (crash_recovery.go) is driven by test-crash.sh, which SIGKILLs a live
// invocation mid-flight. Keeping its rows out of the plain "memory" table
// means a crash-recovery run's necessarily-partial history can never be
// misread as a "memory" mode proof (or vice versa), even if a caller reused
// the same session_id string across both modes.
const crashRecoveryTable = "arena_genkit_go_crash_recovery"

// ensureCrashTable creates the crash-recovery checkpoint table if absent.
// Idempotent.
func (c *rqliteClient) ensureCrashTable(ctx context.Context) error {
	if c.stub {
		return nil
	}
	stmt := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`, crashRecoveryTable)
	_, err := c.execute(ctx, []any{stmt})
	return err
}

// appendCrashTurn persists one CHECKPOINTED turn for sessionID. Only ever
// called AFTER genkit.Generate has already returned successfully — see
// crash_recovery.go for why that ordering IS the crash-safety boundary this
// mode proves: a turn killed mid-Generate never reaches this call at all.
func (c *rqliteClient) appendCrashTurn(ctx context.Context, sessionID, role, content string) error {
	if c.stub {
		stubStoreMu.Lock()
		stubCrashTurns[sessionID] = append(stubCrashTurns[sessionID], turn{Role: role, Content: content})
		stubStoreMu.Unlock()
		return nil
	}
	stmt := []any{
		fmt.Sprintf("INSERT INTO %s(session_id, role, content, created_at) VALUES(?, ?, ?, ?)", crashRecoveryTable),
		sessionID, role, content, time.Now().UnixMilli(),
	}
	_, err := c.execute(ctx, stmt)
	return err
}

// loadCrashTurns returns every CHECKPOINTED turn previously persisted for
// sessionID, oldest first. A turn only appears here if the process that
// generated it survived long enough to call appendCrashTurn — a turn killed
// mid-genkit.Generate leaves zero rows here, by construction.
func (c *rqliteClient) loadCrashTurns(ctx context.Context, sessionID string) ([]turn, error) {
	if c.stub {
		stubStoreMu.Lock()
		defer stubStoreMu.Unlock()
		out := make([]turn, len(stubCrashTurns[sessionID]))
		copy(out, stubCrashTurns[sessionID])
		return out, nil
	}
	stmt := []any{
		fmt.Sprintf("SELECT role, content FROM %s WHERE session_id = ? ORDER BY id ASC", crashRecoveryTable),
		sessionID,
	}
	rows, err := c.query(ctx, stmt)
	if err != nil {
		return nil, err
	}
	turns := make([]turn, 0, len(rows))
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		role, _ := row[0].(string)
		content, _ := row[1].(string)
		turns = append(turns, turn{Role: role, Content: content})
	}
	return turns, nil
}

type rqliteExecResult struct {
	LastInsertID int64  `json:"last_insert_id"`
	RowsAffected int64  `json:"rows_affected"`
	Error        string `json:"error"`
}

type rqliteExecResponse struct {
	Results []rqliteExecResult `json:"results"`
}

func (c *rqliteClient) execute(ctx context.Context, stmt []any) (*rqliteExecResponse, error) {
	body, err := json.Marshal([]any{stmt})
	if err != nil {
		return nil, fmt.Errorf("rqlite: marshal execute body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/db/execute?level=strong", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rqlite: build execute request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rqlite: POST %s/db/execute: %w", c.baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rqlite: /db/execute returned HTTP %d", resp.StatusCode)
	}
	var out rqliteExecResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("rqlite: decode execute response: %w", err)
	}
	for _, r := range out.Results {
		if r.Error != "" {
			return &out, fmt.Errorf("rqlite: statement error: %s", r.Error)
		}
	}
	return &out, nil
}

type rqliteQueryResult struct {
	Columns []string `json:"columns"`
	Values  [][]any  `json:"values"`
	Error   string   `json:"error"`
}

type rqliteQueryResponse struct {
	Results []rqliteQueryResult `json:"results"`
}

func (c *rqliteClient) query(ctx context.Context, stmt []any) ([][]any, error) {
	body, err := json.Marshal([]any{stmt})
	if err != nil {
		return nil, fmt.Errorf("rqlite: marshal query body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/db/query?level=strong", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rqlite: build query request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rqlite: POST %s/db/query: %w", c.baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rqlite: /db/query returned HTTP %d", resp.StatusCode)
	}
	var out rqliteQueryResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("rqlite: decode query response: %w", err)
	}
	if len(out.Results) == 0 {
		return nil, nil
	}
	if out.Results[0].Error != "" {
		return nil, fmt.Errorf("rqlite: query error: %s", out.Results[0].Error)
	}
	return out.Results[0].Values, nil
}
