package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	// Memory-extension dimensions (memory_cross_session / memory_semantic_recall /
	// memory_consolidation — docs/architecture/arena-memory-extensions.md) arrive
	// on the wire as ordinary `memory` turns plus a SECOND scope axis, user_id,
	// which the runner HOLDS constant while VARYING session_id across invocations.
	// Eino has NO user-scoped (indeed no official persisted) memory component:
	// adk is in-process ctx KV + CheckPointStore (interrupt/resume only);
	// its MemoryStore lives only in eino-examples. This adapter's SQLite store is
	// therefore session-keyed (the level Eino's own primitives reach), and it
	// deliberately does NOT synthesize a user-keyed store from user_id — that
	// would be a hand-rolled strawman the fairness protocol forbids
	// (docs/architecture/arena-fairness-protocol.md). The honest, MEASURED
	// consequence: a cross-session probe (fresh session_id, same user_id) loads
	// zero prior turns below and run-and-fails — the finding that Eino has no
	// user-scoped memory, not a bug in this adapter.
	if uid := strings.TrimSpace(in.UserID); uid != "" {
		fmt.Fprintf(os.Stderr, "PROGRESS: memory: wire user_id=%q acknowledged, but Eino has no user-scoped memory primitive (adk CheckPointStore is session/interrupt-keyed) — keying session_id=%q; a different-session recall for this user run-and-fails by construction\n", uid, in.SessionID)
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
