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
)

// ---- Multi-tenant scoped memory (memory_scoped, isolation_leak modes) ----
//
// Eino has NO native multi-tenant memory isolation primitive — no key
// prefixing, no payload filter, nothing that scopes stored state by
// tenant/user/product the way aisotropy enforces by construction (key-prefix
// + Qdrant in-scan filter; see README.md). These two modes hand-roll a
// compound scope key over the same kind of real, on-disk SQLite store
// `memory` mode already uses (mode_memory.go — never an in-process map), and
// prove — with a real agnes recall turn — that the hand-rolled key actually
// isolates tenants.
//
// This is a deliberate, documented dev responsibility: if whoever builds on
// Eino computes this key wrong (e.g. forgets to fold tenant_id into it, or
// swaps the argument order), the mistake is silent at compile time and shows
// up only as one tenant's data answering another tenant's question. That
// failure mode is exactly what isolation_leak is designed to catch.

const scopeKeyDelim = "\x1f" // ASCII Unit Separator: not expected in real tenant/user/product/session identifiers

// scopeKey hand-rolls the compound multi-tenant scope key. This is the
// single place where a missing or misordered field would silently break
// isolation — see the package doc comment above.
func scopeKey(tenantID, userID, productID, sessionID string) string {
	parts := []string{tenantID, userID, productID}
	if sessionID != "" {
		parts = append(parts, sessionID)
	}
	return strings.Join(parts, scopeKeyDelim)
}

func validateScopeFields(in adapterInput) error {
	if strings.TrimSpace(in.TenantID) == "" {
		return errors.New(`this mode requires a non-empty "tenant_id"`)
	}
	if strings.TrimSpace(in.UserID) == "" {
		return errors.New(`this mode requires a non-empty "user_id"`)
	}
	if strings.TrimSpace(in.ProductID) == "" {
		return errors.New(`this mode requires a non-empty "product_id"`)
	}
	return nil
}

// scopedFact is a row recalled from the store, columns and all — not just
// the fact text — so callers (isolation_leak in particular) can
// independently re-verify tenant_id on every row they get back, as defense
// in depth on top of the scope_key match itself.
type scopedFact struct {
	TenantID  string
	UserID    string
	ProductID string
	SessionID string
	Fact      string
	CreatedAt string
}

type scopedMemoryStore struct {
	db *sql.DB
}

// openScopedMemoryStore opens the SAME on-disk SQLite file `memory` mode
// uses (env.memoryDBPath), in a separate table (scoped_facts). Reusing the
// physical store — real file on disk, not an in-process map — is what makes
// the isolation proof meaningful across separate process invocations, the
// same way memory mode's cross-process recall is proven in README.md.
func openScopedMemoryStore(path string) (*scopedMemoryStore, error) {
	if path != ":memory:" {
		if dir := filepath.Dir(path); dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("creating scoped memory db directory %q: %w", dir, err)
			}
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening on-disk sqlite %q: %w", path, err)
	}
	if path == ":memory:" {
		// See openMemoryStore's identical comment (mode_memory.go) — a
		// second pooled connection to ":memory:" would see an empty
		// database, so the pool is pinned to exactly one connection.
		db.SetMaxOpenConns(1)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS scoped_facts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		scope_key TEXT NOT NULL,
		tenant_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		product_id TEXT NOT NULL,
		session_id TEXT NOT NULL DEFAULT '',
		fact TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating scoped_facts table: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_scoped_facts_scope_key ON scoped_facts(scope_key)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating scoped_facts scope_key index: %w", err)
	}
	return &scopedMemoryStore{db: db}, nil
}

func (s *scopedMemoryStore) Close() error { return s.db.Close() }

// write hand-rolls tenant isolation by storing scope alongside the
// individual columns it was built from.
func (s *scopedMemoryStore) write(ctx context.Context, scope string, in adapterInput, fact string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO scoped_facts (scope_key, tenant_id, user_id, product_id, session_id, fact, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		scope, in.TenantID, in.UserID, in.ProductID, in.SessionID, fact, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// recall looks up every fact stored under the exact hand-rolled compound
// scope key. Nothing here filters by anything other than scope_key — if
// scope_key had been computed without tenant_id, this is exactly where the
// leak would happen.
func (s *scopedMemoryStore) recall(ctx context.Context, scope string) ([]scopedFact, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT tenant_id, user_id, product_id, session_id, fact, created_at FROM scoped_facts WHERE scope_key = ? ORDER BY id ASC`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []scopedFact
	for rows.Next() {
		var f scopedFact
		if err := rows.Scan(&f.TenantID, &f.UserID, &f.ProductID, &f.SessionID, &f.Fact, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func joinFacts(facts []scopedFact) string {
	if len(facts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(facts))
	for _, f := range facts {
		parts = append(parts, f.Fact)
	}
	return strings.Join(parts, "\n")
}

func scopedRecallSystemPrompt(factsText string) string {
	if factsText == "" {
		factsText = "(none)"
	}
	return "You are a scoped-memory assistant. Answer the user's question using ONLY the facts " +
		"listed below, which were stored for their exact tenant/user/product scope. If the list " +
		"is \"(none)\", you have no information for this scope — say so plainly. Never use " +
		"outside knowledge or guess.\n\nFacts stored for this scope:\n" + factsText
}

type scopedGenerateResult struct {
	content      string
	inputTokens  int
	outputTokens int
	latencyMS    int64
}

// scopedGenerate makes one real Eino Generate call (agnes) grounded in
// exactly the facts the scoped store returned for the requesting scope —
// shared by both memory_scoped's recall path and isolation_leak's probe
// path, so the isolation behavior under test is identical in both modes.
func scopedGenerate(ctx context.Context, env *arenaEnv, query, factsText string) (scopedGenerateResult, error) {
	messages, err := toSchemaMessages([]inputMessage{
		{Role: "system", Content: scopedRecallSystemPrompt(factsText)},
		{Role: "user", Content: query},
	})
	if err != nil {
		return scopedGenerateResult{}, fmt.Errorf("converting messages: %w", err)
	}

	chatModel, err := newChatModel(ctx, env, &einochatopenai.ChatModelConfig{})
	if err != nil {
		return scopedGenerateResult{}, err
	}

	start := time.Now()
	reply, err := chatModel.Generate(ctx, messages)
	latencyMS := time.Since(start).Milliseconds()
	if err != nil {
		return scopedGenerateResult{}, fmt.Errorf("Eino Generate call: %w", err)
	}

	inTok, outTok := usageOf(reply)
	return scopedGenerateResult{content: reply.Content, inputTokens: inTok, outputTokens: outTok, latencyMS: latencyMS}, nil
}

// scopedRecallResult is the memory_scoped-mode-only "scoped_recall" output
// field (see main.go's adapterOutput). same_scope_hit and cross_tenant_miss
// are two named views of the same underlying store lookup: a recall issued
// for the scope it was written under reports same_scope_hit=true; a recall
// issued for a scope nothing was ever written under (e.g. a different
// tenant_id) reports cross_tenant_miss=true. Both are always populated —
// which one is the meaningful assertion depends on what the caller is
// testing.
type scopedRecallResult struct {
	SameScopeHit    bool   `json:"same_scope_hit"`
	CrossTenantMiss bool   `json:"cross_tenant_miss"`
	RecalledText    string `json:"recalled_text"`
}

// runMemoryScoped stores or recalls a fact under a hand-rolled compound
// scope (tenant_id, user_id, product_id[, session_id]). Which operation runs
// is inferred from the payload: a non-empty "fact" writes, a non-empty
// "query" recalls (grounded by a real agnes Generate call against exactly
// what the scoped store returned).
func runMemoryScoped(ctx context.Context, env *arenaEnv, in adapterInput, out *adapterOutput) error {
	if err := validateScopeFields(in); err != nil {
		return err
	}

	store, err := openScopedMemoryStore(env.memoryDBPath)
	if err != nil {
		return err
	}
	defer store.Close()

	scope := scopeKey(in.TenantID, in.UserID, in.ProductID, in.SessionID)

	switch {
	case in.Fact != "" && in.Query != "":
		return errors.New(`memory_scoped mode requires exactly one of "fact" (write) or "query" (recall), not both`)

	case in.Fact != "":
		if err := store.write(ctx, scope, in, in.Fact); err != nil {
			return fmt.Errorf("writing scoped fact: %w", err)
		}
		out.Reply = fmt.Sprintf("stored fact for tenant=%s user=%s product=%s", in.TenantID, in.UserID, in.ProductID)
		out.InputTokens, out.OutputTokens = -1, -1 // pure store write — no LLM call made
		return nil

	case in.Query != "":
		facts, err := store.recall(ctx, scope)
		if err != nil {
			return fmt.Errorf("recalling scoped facts: %w", err)
		}
		recalledText := joinFacts(facts)
		found := len(facts) > 0

		result, err := scopedGenerate(ctx, env, in.Query, recalledText)
		if err != nil {
			return err
		}
		out.LatencyMS = result.latencyMS
		out.Reply = result.content
		out.InputTokens, out.OutputTokens = result.inputTokens, result.outputTokens
		out.ScopedRecall = &scopedRecallResult{
			SameScopeHit:    found,
			CrossTenantMiss: !found,
			RecalledText:    recalledText,
		}
		return nil

	default:
		return errors.New(`memory_scoped mode requires either "fact" (write) or "query" (recall)`)
	}
}

// runIsolationLeak seeds a secret under one tenant's scope ("fact") or
// probes a scope for leakage ("query"), reusing the exact same scoped store
// memory_scoped writes to. The leak assertion is computed from what the
// store actually returned for the probing scope — re-checking tenant_id on
// every returned row — not from the model's phrasing, matching this mode's
// contract: the isolation assertion is on what the scoped store returns.
func runIsolationLeak(ctx context.Context, env *arenaEnv, in adapterInput, out *adapterOutput) error {
	if err := validateScopeFields(in); err != nil {
		return err
	}

	store, err := openScopedMemoryStore(env.memoryDBPath)
	if err != nil {
		return err
	}
	defer store.Close()

	scope := scopeKey(in.TenantID, in.UserID, in.ProductID, in.SessionID)

	switch {
	case in.Fact != "" && in.Query != "":
		return errors.New(`isolation_leak mode requires exactly one of "fact" (seed the secret) or "query" (probe), not both`)

	case in.Fact != "":
		if err := store.write(ctx, scope, in, in.Fact); err != nil {
			return fmt.Errorf("seeding secret: %w", err)
		}
		out.Reply = fmt.Sprintf("seeded secret under tenant=%s scope", in.TenantID)
		out.InputTokens, out.OutputTokens = -1, -1 // pure store write — no LLM call made
		return nil

	case in.Query != "":
		facts, err := store.recall(ctx, scope)
		if err != nil {
			return fmt.Errorf("probing scoped store: %w", err)
		}
		recalledText := joinFacts(facts)

		result, err := scopedGenerate(ctx, env, in.Query, recalledText)
		if err != nil {
			return err
		}
		out.LatencyMS = result.latencyMS
		out.Reply = result.content
		out.InputTokens, out.OutputTokens = result.inputTokens, result.outputTokens

		leaked := false
		for _, f := range facts {
			if f.TenantID != in.TenantID {
				leaked = true
				break
			}
		}
		out.LeakDetected = &leaked
		return nil

	default:
		return errors.New(`isolation_leak mode requires either "fact" (seed) or "query" (probe)`)
	}
}
