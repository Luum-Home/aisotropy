package main

import (
	"context"
	"fmt"
	"time"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

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
// boundary — which is the whole point of the memory dimension. Each call is one
// App.Turn (one provider round-trip); the session_id is what stitches them.
func runMemory(req *request, out *result) error {
	if req.SessionID == "" {
		return fmt.Errorf("memory mode requires a non-empty \"session_id\" so the two process invocations share a session")
	}
	turns := userMessages(req.Messages)
	if len(turns) == 0 {
		return fmt.Errorf("memory mode requires at least one user message this turn")
	}

	agnes, err := newAgnesClient()
	if err != nil {
		return err
	}
	app := newApp(agnes, req.System)
	defer func() { _ = app.Close() }()

	start := time.Now()
	var lastReply string
	for i, turn := range turns {
		ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
		o, terr := app.Turn(ctx, aisotropy.ChatInput{
			TenantID:  arenaTenant,
			UserID:    arenaUser,
			Prompt:    turn,
			SessionID: req.SessionID,
			ProductID: productID,
		})
		cancel()
		if terr != nil {
			setUsage(out, agnes)
			out.Reply = lastReply
			out.LatencyMs = time.Since(start).Milliseconds()
			return fmt.Errorf("App.Turn failed on turn %d/%d: %w", i+1, len(turns), terr)
		}
		lastReply = o.Reply
	}
	out.LatencyMs = time.Since(start).Milliseconds()
	out.Reply = lastReply
	setUsage(out, agnes)
	logf("memory turn(s) ok on session %q (data_dir persists across processes)", req.SessionID)
	return nil
}
