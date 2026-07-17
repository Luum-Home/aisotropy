package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	einochatopenai "github.com/cloudwego/eino-ext/components/model/openai"
)

// ---- crash_recovery mode ----
//
// Distinct from `memory` mode (which only proves recall across a clean
// process exit): this mode proves whether the SAME on-disk SQLite store
// (mode_memory.go's memoryStore — reused here verbatim, never a second
// store) survives a HARD kill -9 mid-conversation, not just a normal
// process exit. See README.md#crash_recovery-mode and test-crash.sh for the
// two-process, real-SIGKILL scenario this drives.
//
// The payload shape selects which half of the scenario a given invocation
// plays:
//
//   - "crash_turns" non-empty -> PRODUCER (process A, killed mid-run by
//     test-crash.sh).
//   - "messages" non-empty (crash_turns empty) -> RESUMER (process B, a
//     completely fresh process started after the kill).
func runCrashRecovery(ctx context.Context, env *arenaEnv, in adapterInput, out *adapterOutput) error {
	if in.SessionID == "" {
		return errors.New(`crash_recovery mode requires a non-empty "session_id"`)
	}
	if len(in.CrashTurns) > 0 && len(in.Messages) > 0 {
		return errors.New(`crash_recovery mode requires exactly one of "crash_turns" (producer) or "messages" (resumer), not both`)
	}

	store, err := openMemoryStore(env.memoryDBPath)
	if err != nil {
		return err
	}
	defer store.Close()

	switch {
	case len(in.CrashTurns) > 0:
		return runCrashProducer(ctx, env, store, in, out)
	case len(in.Messages) > 0:
		return runCrashResumer(ctx, env, store, in, out)
	default:
		return errors.New(`crash_recovery mode requires either "crash_turns" (producer) or "messages" (resumer)`)
	}
}

// runCrashProducer runs each of in.CrashTurns in order against the real
// agnes chat model, persisting a turn to the shared on-disk SQLite store
// ONLY after its Generate call has already returned successfully — exactly
// mode_memory.go's design (no store.append transaction is ever opened until
// the model call it depends on has finished), so there is never a
// half-written turn to roll back or corrupt if this process is killed.
//
// Before/after each turn it writes a synchronization line to stderr
// (unbuffered, flushed immediately by the fmt/os writes) so test-crash.sh
// can time the kill -9 deterministically — waiting for "turn 2 starting"
// (i.e. turn 1 is already committed, turn 2's real network call is
// in-flight) instead of guessing a sleep duration.
func runCrashProducer(ctx context.Context, env *arenaEnv, store *memoryStore, in adapterInput, out *adapterOutput) error {
	chatModel, err := newChatModel(ctx, env, &einochatopenai.ChatModelConfig{})
	if err != nil {
		return err
	}

	var lastReply string
	var lastInTok, lastOutTok int
	var lastLatency int64

	for i, turnText := range in.CrashTurns {
		turnNum := i + 1
		fmt.Fprintf(os.Stderr, "eino-crash-arena: turn %d starting for session %q\n", turnNum, in.SessionID)

		priorTurns, err := store.load(ctx, in.SessionID)
		if err != nil {
			return fmt.Errorf("loading persisted history before turn %d: %w", turnNum, err)
		}

		wireMsgs := make([]inputMessage, 0, len(priorTurns)+2)
		if in.System != "" && turnNum == 1 {
			wireMsgs = append(wireMsgs, inputMessage{Role: "system", Content: in.System})
		}
		wireMsgs = append(wireMsgs, priorTurns...)
		userMsg := inputMessage{Role: "user", Content: turnText}
		wireMsgs = append(wireMsgs, userMsg)

		messages, err := toSchemaMessages(wireMsgs)
		if err != nil {
			return fmt.Errorf("converting messages for turn %d: %w", turnNum, err)
		}

		start := time.Now()
		reply, err := chatModel.Generate(ctx, messages)
		lastLatency = time.Since(start).Milliseconds()
		if err != nil {
			return fmt.Errorf("Eino Generate call on turn %d: %w", turnNum, err)
		}

		lastReply = reply.Content
		lastInTok, lastOutTok = usageOf(reply)

		if err := store.append(ctx, in.SessionID, []inputMessage{userMsg, {Role: "assistant", Content: reply.Content}}); err != nil {
			return fmt.Errorf("persisting turn %d: %w", turnNum, err)
		}
		fmt.Fprintf(os.Stderr, "eino-crash-arena: turn %d persisted for session %q\n", turnNum, in.SessionID)
	}

	out.Reply = lastReply
	out.InputTokens, out.OutputTokens = lastInTok, lastOutTok
	out.LatencyMS = lastLatency
	// Resumed/PrecrashFactRecalled/StateLost stay nil: this call only
	// reaches here if test-crash.sh let it run to completion (no kill), in
	// which case recovery was never exercised from this process's side —
	// the resumer call is what actually reports on recovery.
	return nil
}

// runCrashResumer is a completely fresh process's view of whatever the
// on-disk store already has for session_id. It never assumes the producer
// finished — it loads exactly what is on disk right now, answers the given
// question grounded in that history, and reports the three recovery signals
// the crash_recovery contract requires:
//
//   - Resumed: true if any prior turns were found on disk at all (loading
//     the persisted state succeeded and returned something).
//   - StateLost: true if nothing survived (the exact negation of Resumed —
//     named separately because it is the failure-mode signal a grader
//     greps for directly).
//   - PrecrashFactRecalled: true if the model's reply, grounded in the
//     recovered history, actually contains ExpectSubstring — i.e. the fact
//     stated before the crash was truly recalled, not just that some
//     history existed.
func runCrashResumer(ctx context.Context, env *arenaEnv, store *memoryStore, in adapterInput, out *adapterOutput) error {
	priorTurns, err := store.load(ctx, in.SessionID)
	if err != nil {
		return fmt.Errorf("loading persisted history for session %q: %w", in.SessionID, err)
	}
	resumed := len(priorTurns) > 0
	stateLost := !resumed

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

	factRecalled := in.ExpectSubstring != "" && strings.Contains(reply.Content, in.ExpectSubstring)
	out.Resumed = &resumed
	out.StateLost = &stateLost
	out.PrecrashFactRecalled = &factRecalled

	newTurns := append(append([]inputMessage{}, in.Messages...), inputMessage{Role: "assistant", Content: reply.Content})
	if err := store.append(ctx, in.SessionID, newTurns); err != nil {
		return fmt.Errorf("persisting resume turn for session %q: %w", in.SessionID, err)
	}

	return nil
}
