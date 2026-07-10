// Command helpdesk is a REALISTIC worked example of a third-party bot built on
// Aisotropy's public surface only (ADR-0013 adoption goal + ADR-0016 levers).
//
// It is a separate Go module (examples/helpdesk/go.mod, replace => ../..) that
// imports ONLY github.com/Luum-Home/aisotropy/aisotropy — Go's internal-package
// rule physically forbids it from reaching the private packages, so the fact
// that it compiles is the "public surface only" guarantee.
//
// It exercises every extension surface a real bot needs, together:
//   - declarative WIRING from a YAML file (persona, rate limit, cost pricing,
//     and optional MCP servers)  -> OptionsFromConfigFile  (lever #5)
//   - TYPED tools whose args are Go structs with a schema + constraints the
//     model is shown and validated against (reflection on violation)  -> WithTypedTool  (lever #1)
//   - it inherits the full runtime for free: per-tenant quota, circuit breaker,
//     cost meter, the tiered memory (working -> facts/rqlite -> semantic/Qdrant),
//     scope-gated tools, and graceful shutdown.
//
// Run it against the reference moat (docker-compose.yml at the repo root):
//
//	docker compose up -d qdrant rqlite
//	cd examples/helpdesk
//	RQLITE_URL=http://localhost:4001 QDRANT_HOST=localhost QDRANT_PORT=6334 \
//	  go run . -config helpdesk.yaml
//	curl -s localhost:8080/health | jq   # facts+semantic probes hit the real tiers
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"sync/atomic"

	"github.com/Luum-Home/aisotropy/aisotropy"
)

// createTicketArgs is the create_ticket tool's typed argument contract. The
// json/jsonschema tags drive the schema the model is shown AND the server-side
// validation: priority is constrained to an enum, so a bad value reflects back
// to the model instead of reaching the handler.
type createTicketArgs struct {
	Subject  string `json:"subject" jsonschema:"description=one-line ticket subject"`
	Priority string `json:"priority" jsonschema:"enum=low,enum=medium,enum=high,description=ticket priority"`
	Body     string `json:"body,omitempty" jsonschema:"description=optional details"`
}

// lookupOrderArgs constrains order_id to a pattern, so a malformed id reflects.
type lookupOrderArgs struct {
	OrderID string `json:"order_id" jsonschema:"pattern=^ORD-[0-9]+$,description=an order id like ORD-1024"`
}

// ticketSeq is a trivial in-process ticket counter for the canned handler; a
// real bot would write to its own system of record. Tickets are labelled with
// the calling tenant (read from ctx) so the example is tenant-aware without ever
// touching an internal memory scope (ADR-0016 invariant).
var ticketSeq atomic.Int64

func createTicket(ctx context.Context, args createTicketArgs) (string, error) {
	tenant, _ := aisotropy.TenantFromContext(ctx)
	id := ticketSeq.Add(1)
	return fmt.Sprintf("Created ticket %s-%d for tenant %q [%s]: %s", "TKT", id, tenant, args.Priority, args.Subject), nil
}

func lookupOrder(ctx context.Context, args lookupOrderArgs) (string, error) {
	tenant, _ := aisotropy.TenantFromContext(ctx)
	// Canned status; a real bot would query its order service scoped to tenant.
	return fmt.Sprintf("Order %s for tenant %q: shipped, arriving in 2 days.", args.OrderID, tenant), nil
}

func main() {
	configPath := flag.String("config", "helpdesk.yaml", "path to the wiring config (YAML)")
	flag.Parse()

	// Declarative wiring (persona, limits, pricing, optional MCP servers) from the
	// YAML file. These become builder options.
	opts, err := aisotropy.OptionsFromConfigFile(*configPath)
	if err != nil {
		log.Fatalf("helpdesk: load config: %v", err)
	}
	slog.Info("helpdesk config loaded", "path", *configPath, "options", len(opts))

	// Compose the file's wiring with the Go-only behavior: two typed tools. A Go
	// option after the config would override the file (config = defaults).
	app := aisotropy.New(append(opts,
		aisotropy.WithTypedTool("create_ticket", "open a support ticket", "write:tickets", createTicket),
		aisotropy.WithTypedTool("lookup_order", "look up an order's status", "read:orders", lookupOrder),
	)...)

	if err := app.Run(context.Background()); err != nil {
		log.Fatalf("helpdesk exited with error: %v", err)
	}
}
