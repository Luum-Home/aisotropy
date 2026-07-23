package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

// memoryRecallPersona is the F1b fallback system prompt for this mode's recall
// invocation. arenas/live/runner/tasks.go's buildMemory sends NO "system" on
// the recall call by design (only the persist call carries the task's
// system), so newApp(agnes, "") would add no WithPersona at all and
// internal/api/persona.go's defaultPersonaProvider would serve the terse
// built-in default ("Respond briefly and directly.") — a persona with no
// signal that memory even matters. mastra's memory.mjs DEFAULT_INSTRUCTIONS
// and pydantic-ai's adapter.py mode_memory both substitute a memory-aware
// default on the exact same no-system-supplied path; this mirrors that. Kept
// deliberately NEUTRAL and NON-LEADING — it names the capability, not the
// answer.
const memoryRecallPersona = "You are a helpful assistant with a good memory. Answer from the conversation so far."

// effectiveMemorySystem resolves the F1b fallback: reqSystem unchanged when
// non-empty (the persist invocation always carries the task's real system
// prompt), memoryRecallPersona when empty (the recall invocation, by the
// runner's design — see buildMemory in runner/tasks.go). Extracted as its own
// function so the fallback decision is unit-testable without driving the
// whole App/stub pipeline — aisotropy's own structured-output wrapping
// (ChatCore's fail-closed JSON-schema instruction) means the system prompt
// itself never reaches the stub's echoed reply text, so this is verified at
// the point the decision is actually made, not by scraping a reply.
func effectiveMemorySystem(reqSystem string) string {
	if reqSystem == "" {
		return memoryRecallPersona
	}
	return reqSystem
}

// runMemory exercises aisotropy's REAL tiered memory ACROSS SEPARATE PROCESS
// INVOCATIONS — never an in-process map. The runner drives this mode twice with
// the SAME session_id and the SAME AISOTROPY_DATA_DIR / RQLITE_URL:
//
//	call 1 (fresh process): states a fact ("my favourite station is ...").
//	   App.Turn persists the user turn into on-disk BadgerDB working memory and,
//	   via the transactional outbox, into the rqlite facts tier. The process exits.
//	call 2 (a BRAND-NEW process): asks a question that depends on that fact.
//	   App.Turn re-opens the SAME on-disk BadgerDB + rqlite, hydrates the session's
//	   prior turns, and the model answers using the earlier fact.
//
// Because the state lives on disk / in networked rqlite, it survives the process
// boundary — which is the whole point of the memory dimension. Each real user
// turn is one App.Turn (one provider round-trip); the session_id is what
// stitches separate process invocations together.
//
// F1a — the suite's canned assistant lines (buildMemory interleaves them with
// the user turns on the persist invocation, see runner/tasks.go) are NOT
// discarded here. aisotropy.App.Turn's ChatInput carries a single Prompt
// string with no message-list / assistant-role slot (aisotropy/turn.go), and
// the public façade has NO OTHER seam that persists an assistant-authored turn
// into working memory without a real model call — aisotropy/extend.go's own
// doc comment is explicit that "the working-memory store ... remain[s]
// runtime-owned with no consumer seam" (confirmed by reading the façade's
// exported surface: aisotropy/turn.go, aisotropy/knowledge.go's
// IngestKnowledge is the semantic/RAG tier, not conversational working
// memory, and no App method writes a turn without calling Turn/RunTask). So
// each canned assistant line is folded into the FOLLOWING user turn's prompt
// as plain established context instead — the best available approximation
// given that gap. Before this fix, the adapter sent only the bare user text
// with no record that e.g. a booking had already been "confirmed" by the
// script, which is what primed the refusal cascade this closes (see
// docs/architecture/arena-memory-forensics.md for the full forensic
// analysis). This is honest, not deceptive: the text is surfaced as
// established context for THIS turn's real model call, never fabricated as
// something the model itself said — aisotropy's own hydration persists
// whatever the model ACTUALLY replies, exactly like any other turn.
func runMemory(req *request, out *result) error {
	if req.SessionID == "" {
		return fmt.Errorf("memory mode requires a non-empty \"session_id\" so the two process invocations share a session")
	}
	if len(req.Messages) == 0 {
		return fmt.Errorf("memory mode requires at least one message this turn")
	}

	agnes, err := newAgnesClient()
	if err != nil {
		return err
	}
	app := newApp(agnes, effectiveMemorySystem(req.System))
	defer func() { _ = app.Close() }()

	// SECOND scope axis (arena-memory-extensions.md): the memory-EXTENSION
	// dimensions (memory_cross_session / memory_semantic_recall /
	// memory_consolidation) VARY session_id while HOLDING a per-task user_id
	// constant, and that user is what carries the fact across the session
	// boundary through aisotropy's user-scoped facts/semantic tiers (working
	// memory is session-keyed, so it deliberately does NOT help here). Plain
	// `memory` mode sends no user_id, so it keeps the fixed arena/live user
	// unchanged. tenant stays arenaTenant either way.
	user := arenaUser
	if strings.TrimSpace(req.UserID) != "" {
		user = req.UserID
	}

	start := time.Now()
	var lastReply string
	var turnsRun int
	var establishedFacts []string // canned assistant lines seen so far (F1a)
	for _, m := range req.Messages {
		switch m.Role {
		case "assistant":
			establishedFacts = append(establishedFacts, m.Content)
			continue
		case "user":
			// handled below
		default:
			continue // ignore unknown roles defensively
		}

		prompt := m.Content
		if len(establishedFacts) > 0 {
			prompt = withEstablishedContext(establishedFacts, m.Content)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
		o, terr := app.Turn(ctx, aisotropy.ChatInput{
			TenantID:  arenaTenant,
			UserID:    user,
			Prompt:    prompt,
			SessionID: req.SessionID,
			ProductID: productID,
		})
		cancel()
		turnsRun++
		if terr != nil {
			setUsage(out, app)
			out.Reply = lastReply
			out.LatencyMs = time.Since(start).Milliseconds()
			return fmt.Errorf("App.Turn failed on turn %d: %w", turnsRun, terr)
		}
		lastReply = o.Reply
	}
	if turnsRun == 0 {
		return fmt.Errorf("memory mode requires at least one user message this turn")
	}
	out.LatencyMs = time.Since(start).Milliseconds()
	out.Reply = lastReply
	setUsage(out, app)
	// Consolidation drain barrier (arena-memory-extensions.md §3.4). Close the
	// App NOW, before the process returns/exits, rather than leaving it solely to
	// the deferred Close: App.Close() tears the runtime down, which STOPS the
	// consolidation outbox worker only AFTER it has drained (aisotropy/app.go's
	// runtime.close), flushing this invocation's persisted turn toward the facts
	// tier. This is the deterministic barrier the memory_consolidation dimension
	// needs — no sleep, no polling: process teardown IS the barrier. Idempotent
	// (closeOnce), so the deferred Close above is a safe no-op afterward. Cross-
	// PROCESS completion is additionally guaranteed by the recall process's own
	// cold-boot outbox reconciliation (aisotropy/app.go's RecoverOutbox at
	// startRuntime), which drains any residual on-disk outbox events before it
	// hydrates — so a fact persisted here is queryable in the next process
	// regardless of exact worker timing.
	drainConsolidation(app)
	logf("memory turn(s) ok on session %q user=%q (data_dir persists across processes)", req.SessionID, user)
	return nil
}

// drainConsolidation is the memory_consolidation deterministic drain barrier:
// it closes the App so the runtime teardown stops the outbox worker only after
// it has drained, flushing persisted turns toward the durable facts tier before
// the process exits. Named + extracted so the barrier is an explicit, testable
// step rather than an implicit consequence of a deferred Close. Idempotent and
// nil-safe via the façade's closeOnce (aisotropy/knowledge.go's App.Close).
//
// Scope note: this is the strongest barrier reachable from the adapter with the
// PUBLIC façade alone. A truly synchronous "block until the fact is queryable in
// rqlite" barrier would need a façade drain/pending-count accessor (the internal
// memory.WorkingMemory.PendingOutboxCount/RecoverOutbox are not exported on the
// aisotropy.App surface, and this adapter is a separate module that cannot reach
// internal packages) — a follow-up on the core façade, out of adapter scope.
func drainConsolidation(app *aisotropy.App) {
	if app == nil {
		return
	}
	_ = app.Close()
}

// withEstablishedContext (F1a) folds the canned assistant lines seen so far in
// THIS invocation's transcript into the next user turn's prompt, so the real
// model call is informed by what the script says already happened — without
// the adapter fabricating an assistant-role history it has no supported way
// to inject (see runMemory's F1a doc comment). Deliberately short and
// factual: it states what was established, not how to answer.
func withEstablishedContext(facts []string, userText string) string {
	var b strings.Builder
	b.WriteString("(For context — already established earlier in this conversation:\n")
	for _, f := range facts {
		b.WriteString("- ")
		b.WriteString(f)
		b.WriteString("\n")
	}
	b.WriteString(")\n\n")
	b.WriteString(userText)
	return b.String()
}
