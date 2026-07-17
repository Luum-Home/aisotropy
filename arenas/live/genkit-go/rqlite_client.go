package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

type rqliteClient struct {
	baseURL string
	http    *http.Client
}

func newRqliteClient(baseURL string) *rqliteClient {
	return &rqliteClient{baseURL: baseURL, http: &http.Client{Timeout: 15 * time.Second}}
}

// ensureTable creates the conversation-turn table if absent. Idempotent.
func (c *rqliteClient) ensureTable(ctx context.Context) error {
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
