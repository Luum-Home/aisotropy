package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
)

// runMemory proves REAL cross-process persistence: conversation turns are
// written to a real rqlite node over HTTP (RQLITE_URL, default
// http://localhost:4001) keyed by session_id, and every invocation re-loads
// the full prior history from rqlite before calling agnes — including turns
// written by an EARLIER, now-exited process. A caller demonstrates this with
// two separate `run.sh` invocations sharing one session_id: the first states
// a fact, the second (a fresh process, with no reference to that fact in its
// own input) asks for it back and can only answer correctly by having
// actually re-hydrated it from rqlite. See test scripts / README.md "memory".
func runMemory(rc *runCtx, out *arenaOutput) error {
	if rc.in.SessionID == "" {
		return fmt.Errorf("memory mode: session_id is required")
	}
	if len(rc.in.Messages) == 0 {
		return fmt.Errorf("memory mode: at least one new message is required")
	}

	// Memory-extension dimensions (memory_cross_session / memory_semantic_recall /
	// memory_consolidation — docs/architecture/arena-memory-extensions.md) arrive
	// on the wire as ordinary `memory` turns plus a SECOND scope axis, user_id,
	// which the runner HOLDS constant while VARYING session_id across invocations.
	// Genkit Go has NO user-scoped memory primitive: its experimental
	// core/x/session (genkit.WithExperimental) is transcript-BY-SESSION-ID only —
	// server-managed replay keyed on the session, not on the user. So this adapter
	// keys the framework's NATIVE scope (session_id) and deliberately does NOT
	// synthesize a user-keyed store from user_id (that would be a hand-rolled
	// strawman the fairness protocol forbids — docs/architecture/
	// arena-fairness-protocol.md). The honest, MEASURED consequence: a
	// cross-session probe (fresh session_id, same user_id) re-hydrates zero prior
	// turns below and run-and-fails — the finding that Genkit Go has no
	// user-scoped memory, not a bug in this adapter.
	if uid := strings.TrimSpace(rc.in.UserID); uid != "" {
		fmt.Fprintf(os.Stderr, "PROGRESS: memory: wire user_id=%q acknowledged, but Genkit Go has no user-scoped memory primitive (core/x/session is session-keyed) — keying NATIVE scope session_id=%q; a different-session recall for this user run-and-fails by construction\n", uid, rc.in.SessionID)
	}

	rq := newRqliteClient(rc.rqliteURL)
	if err := rq.ensureTable(rc.ctx); err != nil {
		return fmt.Errorf("memory: ensure table: %w", err)
	}

	prior, err := rq.loadTurns(rc.ctx, rc.in.SessionID)
	if err != nil {
		return fmt.Errorf("memory: load prior turns: %w", err)
	}
	fmt.Fprintf(os.Stderr, "PROGRESS: memory: loaded %d prior turn(s) for session %q from rqlite\n", len(prior), rc.in.SessionID)

	// Full history = whatever rqlite persisted from earlier process(es) +
	// this invocation's new message(s). The new message(s) are NOT assumed to
	// already be in rqlite (they are persisted below, after the model call).
	msgs := make([]*ai.Message, 0, len(prior)+len(rc.in.Messages))
	for _, t := range prior {
		role := ai.RoleUser
		if t.Role == "assistant" || t.Role == "model" {
			role = ai.RoleModel
		}
		msgs = append(msgs, &ai.Message{Role: role, Content: []*ai.Part{ai.NewTextPart(t.Content)}})
	}
	msgs = append(msgs, toGenkitMessages(rc.in.Messages)...)

	opts := []ai.GenerateOption{ai.WithModel(rc.model), ai.WithMessages(msgs...)}
	if rc.in.System != "" {
		opts = append(opts, ai.WithSystem(rc.in.System))
	}

	start := time.Now()
	resp, err := genkit.Generate(rc.ctx, rc.g, opts...)
	out.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		return fmt.Errorf("memory: genkit.Generate: %w", err)
	}
	applyUsage(out, resp)
	out.Reply = resp.Text()

	// Persist this turn's new message(s) and the fresh reply so a LATER,
	// separate process invocation recalls them.
	for _, m := range rc.in.Messages {
		role := m.Role
		if role == "" {
			role = "user"
		}
		if err := rq.appendTurn(rc.ctx, rc.in.SessionID, role, m.Content); err != nil {
			return fmt.Errorf("memory: persist inbound turn: %w", err)
		}
	}
	if err := rq.appendTurn(rc.ctx, rc.in.SessionID, "assistant", out.Reply); err != nil {
		return fmt.Errorf("memory: persist reply: %w", err)
	}
	fmt.Fprintf(os.Stderr, "PROGRESS: memory: persisted turn(s) to rqlite for session %q\n", rc.in.SessionID)

	return nil
}
