package main

import (
	"context"
	"fmt"
	"time"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

// runQA is the single-shot chat surface: aisotropy's in-process App.Turn, at full
// parity with POST /v1/.../chat (identity->ctx isolation, pre-spend quota gate,
// guardrails, gateway model selection, working-memory + facts hydration,
// transactional-outbox persist, fail-closed reflection loop, cost accounting).
// A multi-turn request replays each user turn on a shared session — aisotropy's
// working-memory tier carries the earlier turns — and the provider usage is summed
// across them, its honest multi-turn cost.
func runQA(req *request, out *result) error {
	turns := userMessages(req.Messages)
	if len(turns) == 0 {
		return fmt.Errorf("no user message in request")
	}

	agnes, err := newAgnesClient()
	if err != nil {
		return err
	}
	app := newApp(agnes, req.System)
	defer func() { _ = app.Close() }()

	sess := sessionID(req)
	start := time.Now()
	var lastReply string
	for i, turn := range turns {
		ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
		o, terr := app.Turn(ctx, aisotropy.ChatInput{
			TenantID:  arenaTenant,
			UserID:    arenaUser,
			Prompt:    turn,
			SessionID: sess,
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
		logf("qa turn %d/%d ok (reply %d chars)", i+1, len(turns), len(o.Reply))
	}
	out.LatencyMs = time.Since(start).Milliseconds()
	out.Reply = lastReply
	setUsage(out, agnes)
	return nil
}
