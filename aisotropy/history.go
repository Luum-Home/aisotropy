package aisotropy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/Luum-Home/aisotropy/internal/memory"
)

// HistoryRole is the speaker of an ingested conversation turn.
type HistoryRole string

const (
	// HistoryRoleUser marks a turn the end user wrote. User turns are the ones
	// consolidation mines for durable facts, so they are enqueued on the outbox.
	HistoryRoleUser HistoryRole = "user"
	// HistoryRoleAssistant marks a turn the assistant produced. Stored as
	// context, not enqueued — mirroring the live chat path, which consolidates
	// the user's input and not its own output (internal/api/chat.go).
	HistoryRoleAssistant HistoryRole = "assistant"
)

// HistoryMessage is one prior conversation turn to load into a session.
type HistoryMessage struct {
	Role    HistoryRole
	Content string
}

// IngestHistory loads prior conversation turns into a session's working memory
// — and, for user turns, onto the consolidation outbox — WITHOUT calling the
// model even once.
//
// WHY THIS EXISTS. `App.Turn` takes one prompt and answers it: establishing N
// turns of prior context costs N model calls, because the only way to put a
// turn into memory was to have the model produce a reply to it. That is correct
// for a live conversation and wrong for every other way history arrives:
// migrating an existing product's transcripts, restoring a session after a
// store change, seeding an evaluation fixture, or handing an agent the context
// of a conversation that happened somewhere else. All of those pay a full
// inference bill to persist text the caller already has.
//
// The live-arena comparison made the cost legible: frameworks whose adapters
// replay a transcript inside a SINGLE call establish context for one call's
// tokens, while aisotropy's contract charged one call per turn. That asymmetry
// is a missing API, not a flaw in the per-turn contract — this is the missing
// API. The per-turn contract stays exactly as it is for live conversation.
//
// WHAT IT COSTS: zero model calls and zero embedding calls on the write path.
// User turns land on the transactional outbox, so the sleep-cycle worker
// distills and indexes them asynchronously on its own schedule — the same route
// a live turn takes, at the same time it would have taken it.
//
// ISOLATION: every write is scoped by the compound key {tenant, user, session}
// and validated before it reaches BadgerDB, identical to the chat path. There
// is no way for this API to write outside the caller-supplied scope.
//
// Turns are appended in the given order, so callers should pass them
// chronologically (oldest first). Ingesting the same turn twice is safe on the
// outbox — the event ID is a content hash, so a re-ingest overwrites rather than
// duplicating the consolidation intent — but the working-memory buffer will
// hold the duplicate, exactly as a user repeating themselves would.
//
// Returns the number of turns persisted. On a partial failure it returns the
// count that succeeded together with the error, so a caller can tell "nothing
// landed" from "the tail failed".
func (a *App) IngestHistory(ctx context.Context, tenant, user, session string, msgs ...HistoryMessage) (int, error) {
	if len(msgs) == 0 {
		return 0, nil
	}
	rt, err := a.ensureRuntime()
	if err != nil {
		return 0, fmt.Errorf("aisotropy: ingest history: %w", err)
	}
	if rt.wm == nil {
		return 0, fmt.Errorf("aisotropy: ingest history: working memory is not configured")
	}

	key := memory.Key{Tenant: tenant, User: user, Session: session}
	if err := key.Validate(); err != nil {
		return 0, fmt.Errorf("aisotropy: ingest history: %w", err)
	}

	now := time.Now().UTC()
	n := 0
	for i, m := range msgs {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue // an empty turn carries no context and no fact
		}

		switch m.Role {
		case HistoryRoleUser:
			// Same route a live user turn takes: buffer + outbox in one
			// BadgerDB transaction, so consolidation cannot miss it.
			event := memory.NewOutboxEvent(key, historyEventID(key, content, i), content, now)
			if err := rt.wm.AppendWithOutbox(ctx, key, memory.Message{Role: string(HistoryRoleUser), Content: content}, event); err != nil {
				return n, fmt.Errorf("aisotropy: ingest history: turn %d: %w", i, err)
			}
		case HistoryRoleAssistant:
			if err := rt.wm.Append(ctx, key, memory.Message{Role: string(HistoryRoleAssistant), Content: content}); err != nil {
				return n, fmt.Errorf("aisotropy: ingest history: turn %d: %w", i, err)
			}
		default:
			return n, fmt.Errorf("aisotropy: ingest history: turn %d: unknown role %q (want %q or %q)",
				i, m.Role, HistoryRoleUser, HistoryRoleAssistant)
		}
		n++
	}
	return n, nil
}

// historyEventID derives a stable outbox event id from the scope, the turn
// content and its position. Position is included deliberately: a transcript may
// legitimately repeat a line ("yes"), and two identical turns at different
// points are two separate consolidation intents, not one retried write.
func historyEventID(key memory.Key, content string, idx int) string {
	h := sha256.Sum256([]byte(
		key.Tenant + "\x00" + key.User + "\x00" + key.Session + "\x00" +
			fmt.Sprintf("%d", idx) + "\x00" + content,
	))
	return hex.EncodeToString(h[:12])
}
