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
	// Genkit Go has NO user-scoped memory primitive — re-verified in source
	// against genkit go@v1.10.0, because the citation this comment used to carry
	// (`core/x/session`) does not exist in that version. The real surface is
	// ai/exp/session.go: `SessionStore[State]` = SnapshotReader + SnapshotWriter,
	// whose entire key space is `snapshotID` / `sessionID`
	// ("WHERE sessionId = ? ORDER BY createdAt DESC"), plus a Firebase
	// `SessionStoreOption` over the same shape. Nothing outside _test.go in that
	// surface mentions a user or owner axis. So the CONCLUSION held; the evidence
	// for it had gone stale, which is exactly the kind of drift that turns a
	// once-true claim into an unfounded one.
	//
	// This adapter USED to stop there and let the cross-session probe
	// run-and-fail, reading the fairness protocol as forbidding a synthesized
	// user key. That reading was inverted: §4's concern is adapters that
	// UNDERSTATE a competitor by hand-rolling instead of using the framework's
	// best official primitive, and where the framework offers NO primitive,
	// declining to supply the axis measures the adapter author, not Genkit. The
	// evidence it was measuring authorship: pydantic-ai, spring-ai and
	// llamaindex sit at the identical capability level — app-level replay, no
	// native user axis, because their frameworks have none either — and all
	// three PASSED, while this adapter and eino refused and FAILED.
	//
	// The axis is now supplied here, uniformly with those three, and the matrix
	// cell carries `pass:<provenance>` naming it ADAPTER-SUPPLIED. The framework
	// finding survives as a visible label instead of a silent fail.
	scopeKey := rc.in.SessionID
	if uid := strings.TrimSpace(rc.in.UserID); uid != "" {
		scopeKey = "user:" + uid
		fmt.Fprintf(os.Stderr, "PROGRESS: memory: wire user_id=%q -> ADAPTER-SUPPLIED user-scoped key %q (Genkit Go has no user-scoped memory primitive: ai/exp/session.go SessionStore is snapshot/session-keyed). Cross-session recall for this user is app-level replay, not a framework guarantee\n", uid, scopeKey)
	}

	rq := newRqliteClient(rc.rqliteURL)
	if err := rq.ensureTable(rc.ctx); err != nil {
		return fmt.Errorf("memory: ensure table: %w", err)
	}

	prior, err := rq.loadTurns(rc.ctx, scopeKey)
	if err != nil {
		return fmt.Errorf("memory: load prior turns: %w", err)
	}
	fmt.Fprintf(os.Stderr, "PROGRESS: memory: loaded %d prior turn(s) for scope %q from rqlite\n", len(prior), scopeKey)

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
		if err := rq.appendTurn(rc.ctx, scopeKey, role, m.Content); err != nil {
			return fmt.Errorf("memory: persist inbound turn: %w", err)
		}
	}
	if err := rq.appendTurn(rc.ctx, scopeKey, "assistant", out.Reply); err != nil {
		return fmt.Errorf("memory: persist reply: %w", err)
	}
	fmt.Fprintf(os.Stderr, "PROGRESS: memory: persisted turn(s) to rqlite for scope %q\n", scopeKey)

	return nil
}
