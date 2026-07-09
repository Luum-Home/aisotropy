// Command aisotropyd is the Aisotropy agent runtime binary.
//
// As of Phase 8 (adoption surface) this is a THIN consumer of the public
// builder in package aisotropy: it registers the demo tools, the demo executor,
// and the default persona, then calls App.Run. All runtime wiring — working
// memory, tier routing, facts + semantic tiers, the outbox worker, OTel, the
// circuit breaker, cheap-model degradation, the sleep cycle, rate limiting, the
// cost meter, the /healthz + /health + /costs endpoints, the three /v1 chat
// routes, and SIGINT/SIGTERM graceful shutdown — lives in aisotropy.App.Run, not
// here. The env-var contract (AISOTROPY_ADDR, AISOTROPY_DATA_DIR,
// AISOTROPY_DEDICATED_TENANTS, RQLITE_URL, QDRANT_*, AISOTROPY_CHEAP_MODEL,
// AISOTROPY_SLEEPCYCLE_BIN, AISOTROPY_RATE_PER_SEC, AISOTROPY_BURST,
// ANTHROPIC_API_KEY) is unchanged.
//
// The demo tools (search_docs, read_financials, write_tasks) + the echo demo
// executor are placeholders, not a product: they exist so the binary boots and
// exercises the scope-gated agent loop without a real integration. A real bot is
// built the same way this file is — against package aisotropy only — replacing
// the demo tools/executor/persona with its own (see examples/weatherbot and
// docs/guides/build-your-own-agent).
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/Luum-Home/aisotropy/aisotropy"
	"github.com/Luum-Home/aisotropy/internal/api"
)

func main() {
	app := aisotropy.New(
		// Scope-gated demo tools (Phase 7 behavior axis): read_financials and
		// write_tasks require a scope; search_docs is always available.
		aisotropy.WithTool(aisotropy.Tool{Name: "search_docs", Description: "search the tenant knowledge base"}),
		aisotropy.WithTool(aisotropy.Tool{Name: "read_financials", Description: "read financial records", RequiredScope: "read:financial-data"}),
		aisotropy.WithTool(aisotropy.Tool{Name: "write_tasks", Description: "create and update tasks", RequiredScope: "write:tasks"}),
		// The demo executor echoes calls and never fails; a real deployment
		// replaces it with WithToolExecutor(itsOwnExecutor).
		aisotropy.WithToolExecutor(api.DemoExecutor{}),
		// Register the built-in default persona explicitly for the empty product
		// id, so the binary's persona is self-documenting rather than implicit.
		aisotropy.WithPersona("", aisotropy.Persona{BasePrompt: "You are the Aisotropy agent. Respond briefly and directly."}),
	)

	if err := app.Run(context.Background()); err != nil {
		slog.Error("aisotropyd exited with error", "err", err)
		os.Exit(1)
	}
}
