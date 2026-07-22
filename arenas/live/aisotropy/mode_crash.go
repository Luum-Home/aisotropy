package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

// ---- crash_recovery mode ----
//
// Distinct from `memory` mode (which proves recall across a CLEAN process
// exit): this mode proves aisotropy's on-disk BadgerDB working memory
// (AISOTROPY_DATA_DIR) survives a HARD `kill -9` mid-conversation. It uses
// aisotropy's REAL durability seam — App.Turn persists each user turn to the
// on-disk store (internal/api/chat.go's chatPersistUserTurn -> AppendWithOutbox)
// BEFORE the model call, so a committed turn is already durable when a later
// turn's in-flight LLM call is killed. See test-crash.sh for the two-process,
// real-SIGKILL scenario.
//
// The payload selects which half of the scenario a given process plays:
//
//   - "crash_turns" non-empty -> PRODUCER (process A, SIGKILLed mid-turn by
//     test-crash.sh once turn 1 is committed and turn 2's model call is
//     in-flight).
//   - "messages" non-empty (crash_turns empty) -> RESUMER (process B, a
//     completely fresh process opened after process A is confirmed dead).
func runCrashRecovery(req *request, out *result) error {
	if strings.TrimSpace(req.SessionID) == "" {
		return fmt.Errorf("crash_recovery mode requires a non-empty \"session_id\"")
	}
	if strings.TrimSpace(req.TenantID) == "" || strings.TrimSpace(req.UserID) == "" {
		return fmt.Errorf("crash_recovery mode requires non-empty \"tenant_id\" and \"user_id\" (the identity whose on-disk working memory is recovered)")
	}
	if len(req.CrashTurns) > 0 && len(userMessages(req.Messages)) > 0 {
		return fmt.Errorf("crash_recovery mode requires exactly one of \"crash_turns\" (producer) or \"messages\" (resumer), not both")
	}

	switch {
	case len(req.CrashTurns) > 0:
		return runCrashProducer(req, out)
	case len(userMessages(req.Messages)) > 0:
		return runCrashResumer(req, out)
	default:
		return fmt.Errorf("crash_recovery mode requires either \"crash_turns\" (producer) or \"messages\" (resumer)")
	}
}

// runCrashProducer runs each crash turn in order against the real agnes model on
// ONE shared App (one open BadgerDB handle across turns), persisting each user
// turn to the on-disk store as part of App.Turn. It writes unbuffered stderr
// synchronization lines around each turn so test-crash.sh can time the kill -9
// deterministically — waiting for "turn 2 starting" (turn 1 already committed,
// turn 2's real model call now in-flight) instead of guessing a sleep.
//
// It never calls app.Close() implicitly on the killed path: when SIGKILL lands,
// no defer runs — which is the whole point. Turn 1's data survives anyway
// because App.Turn already wrote it to the value log (OS page cache persists
// across a process kill; it is lost only on OS crash / power loss).
func runCrashProducer(req *request, out *result) error {
	tenant, user, product := scopeIdentity(req)

	agnes, err := newAgnesClient()
	if err != nil {
		return err
	}
	app := newApp(agnes, req.System)
	// Best-effort close on the CLEAN path (no kill): flushes BadgerDB. On the
	// killed path this never runs, by design.
	defer func() { _ = app.Close() }()

	var lastReply string
	start := time.Now()
	for i, turnText := range req.CrashTurns {
		turnNum := i + 1
		// Unbuffered, flushed immediately: the kill-timing signal.
		fmt.Fprintf(os.Stderr, "aisotropy-crash: turn %d starting for session %q\n", turnNum, req.SessionID)

		ctx, cancel := context.WithTimeout(context.Background(), arenaScopeTimeout)
		o, terr := app.Turn(ctx, aisotropy.ChatInput{
			TenantID:  tenant,
			UserID:    user,
			Prompt:    turnText,
			SessionID: req.SessionID,
			ProductID: product,
		})
		cancel()
		if terr != nil {
			setUsage(out, app)
			out.Reply = lastReply
			out.LatencyMs = time.Since(start).Milliseconds()
			return fmt.Errorf("App.Turn failed on crash turn %d/%d: %w", turnNum, len(req.CrashTurns), terr)
		}
		lastReply = o.Reply
		fmt.Fprintf(os.Stderr, "aisotropy-crash: turn %d persisted for session %q\n", turnNum, req.SessionID)
	}

	out.LatencyMs = time.Since(start).Milliseconds()
	out.Reply = lastReply
	setUsage(out, app)
	// Resumed/PrecrashFactRecalled/StateLost stay nil: recovery is only
	// evaluated by the resumer (process B). Reaching here means the producer ran
	// to completion (no kill), so this side never exercised recovery.
	return nil
}

// runCrashResumer is a completely fresh process's view of whatever aisotropy's
// on-disk working memory already holds for (tenant, user, session). It re-opens
// the same AISOTROPY_DATA_DIR (BadgerDB replays its value log to recover the
// pre-crash turns) and runs one real recall turn. Through the public façade the
// authoritative proof that state survived is that the runtime RECALLED it: the
// fresh process could only echo expect_substring if aisotropy's on-disk store
// replayed the pre-crash turn into this turn's hydration.
//
//   - PrecrashFactRecalled: the reply contains expect_substring (the pre-crash
//     fact was truly recovered, not just "some history existed").
//   - Resumed: same signal — the session resumed WITH its pre-crash state.
//   - StateLost: the exact negation (the failure-mode signal a grader greps).
func runCrashResumer(req *request, out *result) error {
	if req.ExpectSub == "" {
		return fmt.Errorf("crash_recovery resumer requires \"expect_substring\" (the pre-crash fact the recovered reply must contain)")
	}
	question, err := lastUserMessage(req.Messages)
	if err != nil {
		return err
	}
	tenant, user, product := scopeIdentity(req)

	agnes, err := newAgnesClient()
	if err != nil {
		return err
	}
	app := newApp(agnes, req.System)
	defer func() { _ = app.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), arenaScopeTimeout)
	defer cancel()

	start := time.Now()
	o, terr := app.Turn(ctx, aisotropy.ChatInput{
		TenantID:  tenant,
		UserID:    user,
		Prompt:    question,
		SessionID: req.SessionID,
		ProductID: product,
	})
	out.LatencyMs = time.Since(start).Milliseconds()
	setUsage(out, app)
	if terr != nil {
		return fmt.Errorf("resumer App.Turn (tenant=%s session=%s): %w", tenant, req.SessionID, terr)
	}

	recalled := strings.Contains(o.Reply, req.ExpectSub)
	resumed := recalled
	stateLost := !resumed

	out.Reply = o.Reply
	out.Resumed = &resumed
	out.PrecrashFactRecalled = &recalled
	out.StateLost = &stateLost
	logf("crash_recovery resumer: tenant=%s session=%s -> resumed=%v precrash_fact_recalled=%v state_lost=%v", tenant, req.SessionID, resumed, recalled, stateLost)
	return nil
}
