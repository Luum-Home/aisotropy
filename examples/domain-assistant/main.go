// Command domain-assistant is the "here is the WHOLE SHAPE" reference example:
// a knowledge-grounded SUPPORT assistant for a FICTIONAL software product,
// wiring every surface a real domain bot needs in one file so you can see how
// the pieces fit together:
//
//   - a PERSONA          — a support voice, via WithPersona            (ADR-0016)
//   - TYPED TOOLS        — lookup_order + create_ticket, args as Go
//     structs the model is shown and validated against, via WithTypedTool
//   - a CORPUS / RAG     — a small product FAQ ingested so the runtime can
//     hydrate relevant chunks into the system prompt, via
//     IngestTenantKnowledge (ADR-0018/0019)
//   - a GUARDRAIL CHECK  — a grounded-output policy that refuses product claims
//     the corpus does not support, via WithGuardrailCheck            (ADR-0033)
//   - the DEV UI         — a zero-setup browser chat page, via WithDevUI
//   - keyless AUTH       — the explicit NoAuth opt-out, via WithAuthenticator
//
// It is a SEPARATE Go module (examples/domain-assistant/go.mod, replace => ../..)
// that imports ONLY github.com/Luum-Home/aisotropy/aisotropy — never any private
// package. Go's internal-package rule physically forbids this module from
// reaching aisotropy/internal/..., so the fact that it compiles is the "public
// surface only" guarantee: the whole domain app is expressible on the façade.
//
// # The fictional product
//
// "Nimbus" is a FICTIONAL cloud file-sync product invented purely for this
// example — it is not a real product, company, or service. Its support domain
// (storage quotas, sync conflicts, sharing, plan tiers) is generic on purpose,
// so the example teaches the WIRING, not any real policy.
//
// # Honesty gate (read this)
//
//   - This is a DEV/DEMO program, not production. It runs KEYLESS: with no
//     ANTHROPIC_API_KEY set, the App falls back to the built-in stub LLM. The
//     stub does not reason — its deterministic reply embeds your prompt text —
//     so the grounded guardrail is observable without a real model. A real
//     provider (set ANTHROPIC_API_KEY, or WithLLM) changes nothing about WHERE
//     the seams fire.
//   - With no embedder configured the semantic tier uses a deterministic HASH
//     embedder: it wires and exercises the ingest/RAG path but does not rank
//     semantically. Set AISOTROPY_EMBED_URL (or WithEmbedder) to a real model
//     for meaningful retrieval — see the README.
//   - The tool handlers are trivial in-process STUBS (a counter, a canned
//     status). A real bot calls its own systems of record here.
//   - The guardrail is a SIMPLE deterministic heuristic (regex + an approved-
//     facts set), a stand-in for real grounding (retrieval + entailment/NLI).
//     The example's point is the SEAM and its wiring, not the classifier.
//
// Run it keyless (no API key, no Docker), then open the dev UI:
//
//	cd examples/domain-assistant
//	go run .
//	# then browse to http://localhost:8080/ui and chat
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/Luum-Home/aisotropy/aisotropy"
)

// demoTenant is the tenant this example seeds knowledge for. It is "acme" to
// MATCH the dev UI page's default tenant field, so the seeded FAQ is exactly the
// knowledge the runtime hydrates when you open /ui and chat without editing any
// field. (Tenant is a customer/workspace id — unrelated to the product name;
// "acme" is a demo customer org using the Nimbus support bot.) A real bot
// ingests per-tenant on its own schedule.
const demoTenant = "acme"

// nimbusPersona is the support voice. It is registered under BOTH the generic
// default product id ("") — which a plain curl with product_id:"" gets — AND
// under "dev-ui", the product id the shipped dev UI page sends by default, so
// opening /ui and chatting uses this persona with zero edits. A multi-product
// deployment would instead register distinct personas per product id and select
// one per turn via ChatInput.ProductID.
var nimbusPersona = aisotropy.Persona{
	BasePrompt: "You are the Nimbus support assistant. Answer questions about the " +
		"Nimbus file-sync product using only the provided knowledge. If the answer " +
		"is not in your knowledge, say you are not sure and offer to open a ticket. " +
		"Be concise and friendly.",
}

// --- Typed tools -----------------------------------------------------------
//
// A typed tool's argument STRUCT is the single contract: its json/jsonschema
// tags drive BOTH the JSON schema the model is shown AND the server-side decode
// + validation of what the model returns. An invalid argument is reflected back
// to the model, never coerced — the same "struct as the contract" idea Aisotropy
// uses for HTTP/OpenAPI, now for tool inputs.

// lookupOrderArgs constrains order_id to a pattern, so a malformed id reflects
// back to the model instead of reaching the handler.
type lookupOrderArgs struct {
	OrderID string `json:"order_id" jsonschema:"pattern=^ORD-[0-9]+$,description=an order id like ORD-1024"`
}

// createTicketArgs is the create_ticket contract: priority is constrained to an
// enum, so a bad value reflects instead of reaching the handler.
type createTicketArgs struct {
	Subject  string `json:"subject" jsonschema:"description=one-line ticket subject"`
	Priority string `json:"priority" jsonschema:"enum=low,enum=medium,enum=high,description=ticket priority"`
	Body     string `json:"body,omitempty" jsonschema:"description=optional details"`
}

// ticketSeq is a trivial in-process counter for the canned create_ticket
// handler; a real bot writes to its own system of record.
var ticketSeq atomic.Int64

// lookupOrder is the typed handler for lookup_order. It receives the DECODED
// args (not a string map) and the request's already-scoped ctx, from which it
// reads the calling tenant — the only identity a consumer may access (read-only,
// never constructed). A real bot would query its order service scoped to that
// tenant; here it returns a canned status.
func lookupOrder(ctx context.Context, args lookupOrderArgs) (string, error) {
	tenant, _ := aisotropy.TenantFromContext(ctx)
	return fmt.Sprintf("Order %s for tenant %q: Nimbus Pro (annual), active, renews in 240 days.", args.OrderID, tenant), nil
}

// createTicket is the typed handler for create_ticket. Tickets are labelled with
// the calling tenant (read from ctx) so the example is tenant-aware without ever
// touching an internal memory scope.
func createTicket(ctx context.Context, args createTicketArgs) (string, error) {
	tenant, _ := aisotropy.TenantFromContext(ctx)
	id := ticketSeq.Add(1)
	return fmt.Sprintf("Created ticket NIM-%d for tenant %q [%s]: %s", id, tenant, args.Priority, args.Subject), nil
}

// --- Corpus / RAG ----------------------------------------------------------

// faqDocs is the small product FAQ this bot grounds its answers in. The semantic
// tier chunks + embeds these and the chat routes hydrate the most relevant
// chunks into the system prompt (RAG), so the agent can answer policy questions
// WITHOUT a tool call. These are also the FACTS the grounded guardrail below
// enforces — the corpus is the single source of truth for both retrieval and
// the refusal check.
var faqDocs = []aisotropy.Document{
	{
		ID:      "storage-quotas",
		Source:  "faq/storage.md",
		Content: "Storage quotas: the Nimbus Free plan includes 5 GB of storage. The Nimbus Pro plan includes 1 TB of storage. Nimbus does not offer an unlimited-storage plan. Files in the trash still count toward your quota until the trash is emptied.",
	},
	{
		ID:      "sync-conflicts",
		Source:  "faq/sync.md",
		Content: "Sync conflicts: when the same file is edited on two devices while offline, Nimbus keeps both versions and appends '(conflicted copy)' to the newer one so no edit is lost. You then merge them manually. Nimbus never silently overwrites a conflicting edit.",
	},
	{
		ID:      "sharing",
		Source:  "faq/sharing.md",
		Content: "Sharing: you can share any folder with a link. Links can be view-only or edit, and can carry an expiry date. Password-protected links require the Nimbus Pro plan. Revoking a link takes effect immediately for all recipients.",
	},
	{
		ID:      "plans-billing",
		Source:  "faq/billing.md",
		Content: "Plans and billing: Nimbus Pro is billed monthly or annually; annual billing is discounted. Order ids look like ORD-1024. Downgrading to Free at renewal keeps files that fit within 5 GB; anything over quota becomes read-only until you free up space or upgrade again.",
	},
}

// seedKnowledge ingests the FAQ into demoTenant's shared knowledge bucket BEFORE
// Run. That ordering works because ingest and the chat routes share one lazily-
// built semantic store. Failure is non-fatal: the bot still serves, just without
// the seeded FAQ context.
func seedKnowledge(ctx context.Context, app *aisotropy.App) error {
	n, err := app.IngestTenantKnowledge(ctx, demoTenant, faqDocs...)
	if err != nil {
		return err
	}
	slog.Info("seeded tenant knowledge base", "tenant", demoTenant, "docs", len(faqDocs), "chunks", n)
	return nil
}

// --- Grounded-output guardrail ---------------------------------------------

// storagePattern extracts storage-size claims ("5 GB", "1 TB", "2tb") from the
// model's output so the check has a concrete claim to test against the approved
// set. Real grounding uses retrieval + entailment; this regex is the demo's
// deterministic stand-in.
var storagePattern = regexp.MustCompile(`(?i)(\d+)\s?(gb|tb)\b`)

// unlimitedPattern catches the specific claim the FAQ explicitly contradicts
// ("Nimbus does not offer an unlimited-storage plan"), so an answer promising
// unlimited storage is flagged as ungrounded.
var unlimitedPattern = regexp.MustCompile(`(?i)unlimited\s+storage`)

// approvedStorage is the set of storage figures the corpus actually supports
// (Free = 5 GB, Pro = 1 TB), normalized to "<n><unit>". Any storage figure in
// the model's output that is NOT in this set is treated as an ungrounded claim.
// It is derived by hand here to keep the example self-contained; a real system
// would build it from the ingested corpus.
var approvedStorage = map[string]bool{"5gb": true, "1tb": true}

// groundedSupport is the OutputCheck implementing the generic "grounded-output"
// pattern for this domain: it flags product claims the ingested FAQ does not
// support, so (with block-output enabled) the runtime substitutes a safe refusal
// instead of letting an invented quota or an "unlimited storage" promise reach
// the user. It reads in.Output (what the model produced); a single seam could
// just as well read in.Input or in.SystemPrompt for input-facing policy.
//
// This is intentionally a SIMPLE heuristic — the point is the SEAM and its
// wiring. Real policy uses an LLM judge / retrieval / NLI over the corpus.
type groundedSupport struct{}

func (groundedSupport) Name() string { return "grounded-support" }

func (groundedSupport) Check(_ context.Context, in aisotropy.GuardrailCheckInput) aisotropy.GuardrailVerdict {
	var ungrounded []string

	// "unlimited storage" is a claim the corpus explicitly contradicts.
	if unlimitedPattern.MatchString(in.Output) {
		ungrounded = append(ungrounded, "ungrounded:unlimited-storage")
	}

	// Any storage figure not in the approved set is an invented quota.
	for _, m := range storagePattern.FindAllStringSubmatch(in.Output, -1) {
		figure := strings.ToLower(m[1] + m[2]) // e.g. "5gb", "1tb", "500gb"
		if !approvedStorage[figure] {
			ungrounded = append(ungrounded, fmt.Sprintf("ungrounded:storage=%s", figure))
		}
	}

	if len(ungrounded) > 0 {
		log.Printf("[grounded-support] ungrounded claims=%v -> FLAG", ungrounded)
		return aisotropy.GuardrailVerdict{Suspicious: true, Signals: ungrounded, Score: 1.0}
	}
	log.Printf("[grounded-support] all inspected claims grounded -> pass")
	return aisotropy.GuardrailVerdict{}
}

// --- Wiring ----------------------------------------------------------------

func main() {
	app := aisotropy.New(
		// PERSONA. Registered under the generic default product id ("") for plain
		// HTTP callers AND under "dev-ui" (the id the shipped dev UI page sends), so
		// this single-product bot uses the Nimbus voice out of the box in both. See
		// the nimbusPersona doc for the multi-product alternative.
		aisotropy.WithPersona("", nimbusPersona),
		aisotropy.WithPersona("dev-ui", nimbusPersona),

		// TYPED TOOLS. Each is scope-gated: the model is only offered the tool when
		// the caller carries the required scope, and the args are validated against
		// the struct before the handler runs.
		aisotropy.WithTypedTool("lookup_order", "look up a customer's order/subscription status", "read:orders", lookupOrder),
		aisotropy.WithTypedTool("create_ticket", "open a support ticket for a customer", "write:tickets", createTicket),

		// GROUNDED-OUTPUT GUARDRAIL. Runs on every model response ALONGSIDE the
		// runtime's built-in checks; with AISOTROPY_GUARDRAILS_BLOCK_OUTPUT enabled
		// a flagged non-streaming turn becomes a safe refusal. Uses the PUBLIC
		// GuardrailCheckInput / GuardrailVerdict aliases — no internal import.
		aisotropy.WithGuardrailCheck(groundedSupport{}),

		// DEV UI. Mounts a zero-setup, self-contained browser chat page at GET /ui
		// (outside the /v1 auth middleware, like /healthz). It POSTs to the app's
		// own streaming endpoint so you can exercise the whole pipeline by hand.
		// It is a DEVELOPMENT CONVENIENCE and UNAUTHENTICATED — never expose it on
		// a public listener; build your own frontend for production.
		aisotropy.WithDevUI(),

		// A generous rate limit so manual clicking through the dev UI is never
		// throttled during a demo.
		aisotropy.WithRateLimit(1e9, 1e9),

		// KEYLESS AUTH. Explicitly run without a credential check so the example is
		// usable out of the box: the URL path tenant/user are trusted verbatim.
		// Without this the runtime defaults to DENY-ALL and every /v1 request gets
		// 401 (fail closed) — deliberate, but opted out here for a local demo. A
		// real deployment swaps NoAuth() for a JWT authenticator; see the helpdesk
		// example and docs/guides/build-your-own-agent.
		aisotropy.WithAuthenticator(aisotropy.NoAuth()),
	)

	// Seed the FAQ before Run (both share one semantic store). Non-fatal on error.
	if err := seedKnowledge(context.Background(), app); err != nil {
		slog.Warn("seeding knowledge failed; continuing without seeded FAQ", "err", err)
	}

	slog.Info("domain-assistant starting", "tenant", demoTenant, "dev_ui", "http://localhost:8080/ui")
	if err := app.Run(context.Background()); err != nil {
		log.Fatalf("domain-assistant exited with error: %v", err)
	}
}
