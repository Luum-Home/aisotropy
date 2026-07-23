package main

import (
	"context"
	"fmt"
	"time"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

// runForgetful is the RECALL half of the "forgetful integrator" isolation
// dimension (docs/architecture/arena-tenancy-levels.md §"forgetful integrator";
// scored by scoring.ScoreForgetful in the runner). The runner has already, in a
// SEPARATE process invocation, written a fact under an explicit scope A (a real
// user, wire-mode "memory" → runMemory). THIS invocation is the exact real-world
// integrator mistake: read the fact back with the scope OMITTED.
//
// THE FAIRNESS INVARIANT (why this is a distinct handler, not a branch of
// runMemory): the omission must be exercised at aisotropy's OWN API boundary,
// never masked by adapter glue. runMemory BACKFILLS the fixed arenaUser whenever
// req.UserID is blank (mode_memory.go) — convenient there, but here it would
// measure the ADAPTER's default-filling, not the FRAMEWORK's enforcement, which
// is precisely the fairness violation the design forbids
// (docs/architecture/arena-idiomaticity-and-dx.md). So this handler does the
// opposite: it forwards req.UserID VERBATIM (blank when the integrator omitted
// it) into aisotropy.App.Turn and lets aisotropy decide.
//
// WHY aisotropy lands in "refuses" BY CONSTRUCTION (the executable self-audit):
// App.Turn (aisotropy/turn.go:97) maps straight onto api.ChatCore, whose very
// first action is chatIdentity → withIdentity → memory.Key.Validate:
//
//   - internal/api/core.go:55-58    — ChatCore's first line calls chatIdentity;
//     an error becomes huma.Error422UnprocessableEntity("invalid tenant/user
//     identity", …), returned BEFORE the quota gate, the guardrail, or ANY model
//     call — so the refusal is deterministic and fully offline-observable.
//   - internal/api/chat.go:193-196  — withIdentity runs (memory.Key{Tenant,User,
//     Session}).Validate() before injecting identity into ctx.
//   - internal/memory/key.go:96-107 / 34-40 — Key.Validate → ValidateIdentity-
//     Component returns `memory: user requires a non-empty value` for a blank
//     user. There is NO code path that assembles a storage key from a blank
//     identity; omission is unrepresentable, not defended-against.
//
// The runner recognizes that error (softenForgetfulRefusal, main.go) and scores
// it "refuses" (PASS). If aisotropy ever instead RETURNED the scoped fact here,
// Turn would not error, this handler would surface the reply, and the runner
// would score it a leak — the symmetric-rigor path that can fail aisotropy.
func runForgetful(req *request, out *result) error {
	out.Model = modelName()

	query, err := lastUserMessage(req.Messages)
	if err != nil {
		return fmt.Errorf("isolation_forgetful recall requires a user message to ask: %w", err)
	}

	agnes, err := newAgnesClient()
	if err != nil {
		return err
	}
	app := newApp(agnes, memoryRecallPersona)
	defer func() { _ = app.Close() }()

	// The load-bearing line: req.UserID is forwarded RAW. When the runner omitted
	// the user scope it is "" — and aisotropy's own Key.Validate (NOT this
	// adapter) is what rejects it. tenant stays the real arena tenant; the axis
	// the integrator "forgot" is the user.
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	o, terr := app.Turn(ctx, aisotropy.ChatInput{
		TenantID:  arenaTenant,
		UserID:    req.UserID, // RAW, blank on the omitted-scope read — no backfill
		Prompt:    query,
		SessionID: req.SessionID,
		ProductID: productID,
	})
	cancel()
	if terr != nil {
		// aisotropy refused the omitted scope at its own boundary (expected: a 422
		// "invalid tenant/user identity" wrapping "memory: user requires a
		// non-empty value"). Surface it verbatim so the runner's
		// softenForgetfulRefusal recognizes the enforcement and scores "refuses".
		setUsage(out, app)
		return fmt.Errorf("App.Turn refused the omitted-scope read (tenant=%q user=%q session=%q): %w",
			arenaTenant, req.UserID, req.SessionID, terr)
	}
	// A framework that did NOT enforce would reach here with a reply; the runner
	// then scores leak (returned the scoped fact) vs silent_partition (a
	// different/empty scope). aisotropy is not expected to reach this branch.
	out.Reply = o.Reply
	setUsage(out, app)
	logf("isolation_forgetful recall completed WITHOUT enforcement (tenant=%q user=%q) — runner will score leak vs silent_partition", arenaTenant, req.UserID)
	return nil
}
