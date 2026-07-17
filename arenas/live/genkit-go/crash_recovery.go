package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
)

// runCrashRecovery proves REAL crash-durability, not just clean-exit
// persistence. "memory" mode (memory.go) only ever observes graceful process
// exits between invocations — a caller's `run.sh` always runs to completion
// before the next one starts. This mode is designed to be driven by
// test-crash.sh, which sends SIGKILL to a LIVE invocation while it is
// mid-flight on a turn, before that turn's checkpoint write to rqlite can
// happen. A later, completely fresh process invocation must then prove it can
// resume from whatever was actually committed (and ONLY that), recalling a
// fact from an earlier, already-checkpointed turn even though the
// immediately preceding turn was destroyed mid-air by kill -9.
//
// Crash-safety boundary this mode exists to prove: a turn becomes durable in
// rqlite only after BOTH appendCrashTurn calls below return successfully —
// which only happens AFTER genkit.Generate has already completed. There is no
// write-ahead log and no partial-turn checkpoint: if the process is killed
// during genkit.Generate (the slow, network-bound step — the "PROGRESS:
// ... entering genkit.Generate" marker below is the exact window
// test-crash.sh targets), that turn is atomically gone, never partially
// persisted. See README.md "crash_recovery" for the full honest note on what
// this does, and does not, prove.
func runCrashRecovery(rc *runCtx, out *arenaOutput) error {
	if rc.in.SessionID == "" {
		return fmt.Errorf("crash_recovery mode: session_id is required")
	}
	if len(rc.in.Messages) == 0 {
		return fmt.Errorf("crash_recovery mode: at least one new message is required")
	}

	rq := newRqliteClient(rc.rqliteURL)
	if err := rq.ensureCrashTable(rc.ctx); err != nil {
		return fmt.Errorf("crash_recovery: ensure table: %w", err)
	}

	prior, err := rq.loadCrashTurns(rc.ctx, rc.in.SessionID)
	if err != nil {
		return fmt.Errorf("crash_recovery: load prior checkpointed turns: %w", err)
	}
	resumed := len(prior) > 0
	out.Resumed = &resumed
	priorCount := len(prior)
	out.PriorTurnsLoaded = &priorCount
	fmt.Fprintf(os.Stderr, "PROGRESS: crash_recovery: loaded %d prior checkpointed turn(s) for session %q from rqlite (resumed=%v)\n", priorCount, rc.in.SessionID, resumed)

	// Full history = whatever rqlite has CHECKPOINTED from earlier
	// invocation(s) (which may be fewer turns than were attempted, if an
	// earlier one was killed mid-flight) + this invocation's new message(s).
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

	// Nothing for THIS turn has been written yet — only the load above
	// happened, which is read-only. From here until the checkpoint calls
	// below both succeed, this turn is entirely uncommitted: a kill -9 in
	// this window loses only this turn, and loses it atomically (never a
	// half-written row). test-crash.sh polls stderr for this exact marker
	// before sending SIGKILL.
	fmt.Fprintf(os.Stderr, "PROGRESS: crash_recovery: entering genkit.Generate (uncommitted turn — a kill -9 now loses only this turn)\n")
	start := time.Now()
	resp, err := genkit.Generate(rc.ctx, rc.g, opts...)
	out.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		return fmt.Errorf("crash_recovery: genkit.Generate: %w", err)
	}
	applyUsage(out, resp)
	out.Reply = resp.Text()

	// Checkpoint: only reachable if the process survived the call above.
	for _, m := range rc.in.Messages {
		role := m.Role
		if role == "" {
			role = "user"
		}
		if err := rq.appendCrashTurn(rc.ctx, rc.in.SessionID, role, m.Content); err != nil {
			return fmt.Errorf("crash_recovery: checkpoint inbound turn: %w", err)
		}
	}
	if err := rq.appendCrashTurn(rc.ctx, rc.in.SessionID, "assistant", out.Reply); err != nil {
		return fmt.Errorf("crash_recovery: checkpoint reply: %w", err)
	}
	checkpointed := true
	out.Checkpointed = &checkpointed
	fmt.Fprintf(os.Stderr, "PROGRESS: crash_recovery: checkpointed turn to rqlite for session %q\n", rc.in.SessionID)

	// precrash_fact_recalled / state_lost are only meaningful when the caller
	// supplies `fact` — reusing arenaInput.Fact, the same field
	// memory_scoped/isolation_leak already use (scoped_memory.go) — as the
	// keyword this invocation's reply is expected to recall from a PRE-CRASH,
	// already-checkpointed turn. Left nil (JSON null) for calls that are
	// purely checkpoint-writing steps in a crash-test sequence (e.g. turn 1),
	// not the "did it actually recover?" assertion itself.
	if rc.in.Fact != "" {
		recalled := resumed && strings.Contains(strings.ToLower(out.Reply), strings.ToLower(rc.in.Fact))
		lost := !recalled
		out.PrecrashFactRecalled = &recalled
		out.StateLost = &lost
		fmt.Fprintf(os.Stderr, "PROGRESS: crash_recovery: precrash_fact_recalled=%v state_lost=%v (looking for %q in reply)\n", recalled, lost, rc.in.Fact)
	}

	return nil
}
