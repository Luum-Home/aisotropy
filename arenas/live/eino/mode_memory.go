package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	einochatopenai "github.com/cloudwego/eino-ext/components/model/openai"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registers as "sqlite"
)

// runMemory persists multi-turn conversation state to a REAL on-disk SQLite
// database (never an in-process map) keyed by session_id, so a fact stated
// in one process invocation can be recalled by a completely separate later
// invocation — see README.md for the two-process smoke test that proves
// this. The chat call itself still goes through Eino's native chat model
// component; only the cross-process persistence layer is custom (Eino has
// no built-in persisted-memory component to drive natively).
func runMemory(ctx context.Context, env *arenaEnv, in adapterInput, out *adapterOutput) error {
	if in.SessionID == "" {
		return errors.New("memory mode requires a non-empty \"session_id\"")
	}
	if len(in.Messages) == 0 {
		return errors.New("memory mode requires at least one message in this turn")
	}

	store, err := openMemoryStore(env.memoryDBPath)
	if err != nil {
		return err
	}
	defer store.Close()

	priorTurns, err := store.load(ctx, in.SessionID)
	if err != nil {
		return fmt.Errorf("loading persisted history for session %q: %w", in.SessionID, err)
	}

	wireMsgs := make([]inputMessage, 0, len(priorTurns)+len(in.Messages)+1)
	if in.System != "" {
		wireMsgs = append(wireMsgs, inputMessage{Role: "system", Content: in.System})
	}
	wireMsgs = append(wireMsgs, priorTurns...)
	wireMsgs = append(wireMsgs, in.Messages...)

	messages, err := toSchemaMessages(wireMsgs)
	if err != nil {
		return fmt.Errorf("converting messages: %w", err)
	}

	chatModel, err := newChatModel(ctx, env, &einochatopenai.ChatModelConfig{})
	if err != nil {
		return err
	}

	start := time.Now()
	reply, err := chatModel.Generate(ctx, messages)
	out.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		return fmt.Errorf("Eino Generate call: %w", err)
	}

	out.Reply = reply.Content
	out.InputTokens, out.OutputTokens = usageOf(reply)

	newTurns := append(append([]inputMessage{}, in.Messages...), inputMessage{Role: "assistant", Content: reply.Content})
	if err := store.append(ctx, in.SessionID, newTurns); err != nil {
		return fmt.Errorf("persisting turn for session %q: %w", in.SessionID, err)
	}

	return nil
}

type memoryStore struct {
	db *sql.DB
}

func openMemoryStore(path string) (*memoryStore, error) {
	if path != ":memory:" {
		if dir := filepath.Dir(path); dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("creating memory db directory %q: %w", dir, err)
			}
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening on-disk sqlite %q: %w", path, err)
	}
	if path == ":memory:" {
		// ARENA_STUB=1's in-memory store (see clients.go/README.md#stub-mode):
		// database/sql pools multiple physical connections by default, and
		// each connection to ":memory:" is its OWN separate, non-shared
		// SQLite instance — a second connection would see an empty database.
		// Pinning the pool to exactly one connection makes load()/append()
		// within a single process invocation see one consistent in-memory
		// store, matching what a single on-disk file already guarantees.
		db.SetMaxOpenConns(1)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS turns (
		session_id TEXT NOT NULL,
		turn_index INTEGER NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (session_id, turn_index)
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating turns table: %w", err)
	}
	return &memoryStore{db: db}, nil
}

func (s *memoryStore) Close() error { return s.db.Close() }

func (s *memoryStore) load(ctx context.Context, sessionID string) ([]inputMessage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT role, content FROM turns WHERE session_id = ? ORDER BY turn_index ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []inputMessage
	for rows.Next() {
		var m inputMessage
		if err := rows.Scan(&m.Role, &m.Content); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *memoryStore) append(ctx context.Context, sessionID string, turns []inputMessage) error {
	var nextIndex int
	row := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(turn_index), 0) + 1 FROM turns WHERE session_id = ?`, sessionID)
	if err := row.Scan(&nextIndex); err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO turns (session_id, turn_index, role, content, created_at) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i, m := range turns {
		if _, err := stmt.ExecContext(ctx, sessionID, nextIndex+i, m.Role, m.Content, now); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}
