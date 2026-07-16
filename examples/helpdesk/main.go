// Command helpdesk is a REALISTIC worked example of a third-party support bot
// built on Aisotropy's public surface only (ADR-0013 adoption goal + ADR-0016
// levers). Where weatherbot is the minimal "hello agent", this one wires up
// every extension surface a real bot needs, together.
//
// It is a separate Go module (examples/helpdesk/go.mod, replace => ../..) that
// imports ONLY github.com/Luum-Home/aisotropy/aisotropy — Go's internal-package
// rule physically forbids it from reaching the private packages, so the fact
// that it compiles is the "public surface only" guarantee.
//
// What it exercises, all at once:
//   - declarative WIRING from a YAML file (persona, rate limit, cost pricing,
//     and optional MCP servers)         -> OptionsFromConfigFile   (lever #5)
//   - TYPED tools whose args are Go structs with a schema + constraints the
//     model is shown and validated against (reflection on violation)
//     -> WithTypedTool           (lever #1)
//   - secure-by-config AUTH: a JWT authenticator when AISOTROPY_JWT_SECRET is
//     set, else the explicit keyless opt-out — no code fork to secure it
//     -> AuthenticatorFromEnv    (ADR-0024)
//   - a seeded tenant KNOWLEDGE base the semantic tier hydrates into the system
//     prompt, so the agent can cite policy without a tool call
//     -> IngestTenantKnowledge   (ADR-0018/0019)
//   - the full runtime for free: per-tenant quota, circuit breaker, cost meter,
//     tiered memory (working -> facts/rqlite -> semantic/Qdrant), scope-gated
//     tools, and graceful shutdown.
//
// Run it KEYLESS (no API key, no Docker) — see README.md:
//
//	go run . -config helpdesk.yaml
//
// Or against the reference moat (docker-compose.yml at the repo root) so the
// memory tiers become real networked services:
//
//	docker compose up -d qdrant rqlite
//	RQLITE_URL=http://localhost:4001 QDRANT_HOST=localhost QDRANT_PORT=6334 \
//	  go run . -config helpdesk.yaml
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

// demoTenant is the tenant the example seeds knowledge for, so a keyless
// `curl .../v1/tenant/acme/user/...` surfaces the seeded KB in the system
// prompt. A real bot ingests per-tenant on its own schedule.
const demoTenant = "acme"

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
	return fmt.Sprintf("Created ticket TKT-%d for tenant %q [%s]: %s", id, tenant, args.Priority, args.Subject), nil
}

func lookupOrder(ctx context.Context, args lookupOrderArgs) (string, error) {
	tenant, _ := aisotropy.TenantFromContext(ctx)
	// Canned status; a real bot would query its order service scoped to tenant.
	return fmt.Sprintf("Order %s for tenant %q: shipped, arriving in 2 days.", args.OrderID, tenant), nil
}

// seedKnowledge ingests a few support-policy documents into demoTenant's shared
// knowledge bucket. The semantic tier chunks + embeds them and the chat routes
// hydrate the most relevant chunks into the system prompt (RAG), so the agent
// can answer policy questions WITHOUT a tool call. Ingesting BEFORE Run works
// because both share one lazily-built semantic store (see aisotropy/knowledge.go).
//
// Keyless caveat: with no embedder configured the default is a deterministic
// HASH embedder — it wires and exercises the ingest/RAG path but does not rank
// semantically. Set AISOTROPY_EMBED_URL (or WithEmbedder) to a real model for
// meaningful retrieval; see the README.
func seedKnowledge(ctx context.Context, app *aisotropy.App) error {
	docs := []aisotropy.Document{
		{
			ID:      "returns-policy",
			Source:  "policies/returns.md",
			Content: "Returns: customers may return any item within 30 days of delivery for a full refund. Opened electronics carry a 15% restocking fee. Refunds are issued to the original payment method within 5 business days.",
		},
		{
			ID:      "shipping-policy",
			Source:  "policies/shipping.md",
			Content: "Shipping: standard delivery is 2-4 business days. Express delivery is next business day when ordered before 2pm. We ship to the US, Canada, and the EU. Order ids look like ORD-1234.",
		},
		{
			ID:      "escalation-policy",
			Source:  "policies/escalation.md",
			Content: "Escalation: if a customer is unhappy after one resolution attempt, open a high-priority ticket and tell them a human agent will follow up within 24 hours.",
		},
	}
	n, err := app.IngestTenantKnowledge(ctx, demoTenant, docs...)
	if err != nil {
		return err
	}
	slog.Info("seeded tenant knowledge base", "tenant", demoTenant, "docs", len(docs), "chunks", n)
	return nil
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

	// Secure-by-config authentication (ADR-0024), the same idiom the shipped
	// aisotropyd binary uses: AuthenticatorFromEnv returns a real HS256 JWT gate
	// when AISOTROPY_JWT_SECRET is set, otherwise the explicit keyless opt-out
	// (NoAuth) so this example runs out of the box. A weak secret is a fatal
	// config fault — the bot refuses to boot rather than come up insecure. The
	// mode label is safe to log; no key material is ever returned.
	authr, authMode, authErr := aisotropy.AuthenticatorFromEnv()
	if authErr != nil {
		log.Fatalf("helpdesk: invalid authentication configuration: %v", authErr)
	}
	slog.Info("helpdesk auth mode", "mode", authMode) // "noauth" keyless, "jwt-hs256" when secured

	// Compose the file's wiring with the Go-only behavior: the authenticator and
	// two typed tools. A Go option after the config options wins, so config is
	// defaults and code is overrides.
	app := aisotropy.New(append(opts,
		aisotropy.WithAuthenticator(authr),
		aisotropy.WithTypedTool("create_ticket", "open a support ticket", "write:tickets", createTicket),
		aisotropy.WithTypedTool("lookup_order", "look up an order's status", "read:orders", lookupOrder),
	)...)

	// Seed the knowledge base before Run (both share one semantic store). Failure
	// is non-fatal: the bot still serves, just without the seeded policy context.
	if err := seedKnowledge(context.Background(), app); err != nil {
		slog.Warn("helpdesk: seeding knowledge failed; continuing without seeded KB", "err", err)
	}

	if err := app.Run(context.Background()); err != nil {
		log.Fatalf("helpdesk exited with error: %v", err)
	}
}
