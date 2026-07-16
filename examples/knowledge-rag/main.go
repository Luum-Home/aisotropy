// Command knowledge-rag is the LESSON 05 worked example: seed a knowledge base
// and let the semantic tier hydrate it into the system prompt (RAG), so the
// agent can answer from ingested documents WITHOUT a tool call.
//
// It is library-first: it uses App.Turn (the in-process runtime, no HTTP server,
// no SSE) so the whole ingest -> retrieve loop is a couple of function calls. It
// exercises both ingestion surfaces:
//
//   - IngestTenantKnowledge — a tenant-SHARED bucket (ADR-0019): every user of
//     the tenant can retrieve it, no per-user re-ingestion.
//   - IngestKnowledge — scoped to one (tenant, user): private to that user.
//
// Both chunk, embed, and store the documents; the chat pipeline (here App.Turn)
// hydrates the most relevant chunks into the system prompt at request time.
//
// HONESTY GATE — keyless retrieval quality. With NO embedder configured the
// default is a deterministic HASH embedder (semantic.NewHashEmbedder): it wires
// and exercises the ingest/RAG path end-to-end but does NOT rank semantically,
// and the credential-less stub LLM echoes the prompt rather than reasoning over
// the hydrated context. So keyless, the honest, observable proof that the
// pipeline ran is the CHUNK COUNT each ingest returns. For meaningful retrieval,
// set AISOTROPY_EMBED_URL (or WithEmbedder) to a real embeddings model and
// ANTHROPIC_API_KEY to a real LLM — no code change; see README.md.
//
// It is a SEPARATE Go module (examples/knowledge-rag/go.mod, replace => ../..)
// importing ONLY github.com/Luum-Home/aisotropy/aisotropy, so Go's
// internal-package rule enforces "public surface only".
//
// Run it keyless (no API key, no Docker) — see README.md:
//
//	go run .
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Luum-Home/aisotropy/aisotropy"
)

const (
	demoTenant = "acme"
	demoUser   = "alice"
)

func main() {
	// Default embedder (deterministic HASH placeholder) and stub LLM: keyless.
	// Drop WithEmbedder / set AISOTROPY_EMBED_URL + ANTHROPIC_API_KEY for real RAG.
	app := aisotropy.New(
		aisotropy.WithPersona("kb-bot", aisotropy.Persona{
			BasePrompt: "You are KBBot. Answer using the ingested knowledge base; cite the source.",
		}),
	)
	defer func() { _ = app.Close() }()

	ctx := context.Background()

	// (1) Tenant-shared knowledge: retrievable by every user of demoTenant.
	tenantDocs := []aisotropy.Document{
		{
			ID:      "returns-policy",
			Source:  "policies/returns.md",
			Content: "Returns: customers may return any item within 30 days of delivery for a full refund. Opened electronics carry a 15% restocking fee.",
		},
		{
			ID:      "shipping-policy",
			Source:  "policies/shipping.md",
			Content: "Shipping: standard delivery is 2-4 business days. Express is next business day when ordered before 2pm. We ship to the US, Canada, and the EU.",
		},
	}
	tenantChunks, err := app.IngestTenantKnowledge(ctx, demoTenant, tenantDocs...)
	if err != nil {
		log.Fatalf("knowledge-rag: ingest tenant knowledge: %v", err)
	}
	fmt.Printf("ingested %d tenant-shared doc(s) -> %d chunk(s) into tenant %q\n", len(tenantDocs), tenantChunks, demoTenant)

	// (2) Per-user knowledge: private to (demoTenant, demoUser).
	userDocs := []aisotropy.Document{
		{
			ID:      "alice-preferences",
			Source:  "profile/alice.md",
			Content: "Alice prefers email over phone, ships to a Seattle address, and is enrolled in the priority-support program.",
		},
	}
	userChunks, err := app.IngestKnowledge(ctx, demoTenant, demoUser, userDocs...)
	if err != nil {
		log.Fatalf("knowledge-rag: ingest user knowledge: %v", err)
	}
	fmt.Printf("ingested %d user-scoped doc(s) -> %d chunk(s) for %q/%q\n\n", len(userDocs), userChunks, demoTenant, demoUser)

	// (3) A turn that RETRIEVES: the runtime hydrates the most relevant ingested
	// chunks (tenant-shared + this user's) into the system prompt before the model
	// answers. Reusing the same tenant/user is what scopes retrieval to their KB.
	out, err := app.Turn(ctx, aisotropy.ChatInput{
		TenantID:  demoTenant,
		UserID:    demoUser,
		SessionID: "kb-session-1",
		ProductID: "kb-bot",
		Prompt:    "What is the return window, and how does Alice prefer to be contacted?",
	})
	if err != nil {
		log.Fatalf("knowledge-rag: turn: %v", err)
	}

	fmt.Printf("reply (tenant %s): %s\n", out.TenantID, out.Reply)
	if out.Stub {
		fmt.Println()
		fmt.Println("NOTE: stub=true — this reply came from the credential-less stub LLM, which")
		fmt.Println("echoes the prompt. The ingest/RAG path ran (see the chunk counts above), but")
		fmt.Println("meaningful retrieval needs a real embedder + model. Set AISOTROPY_EMBED_URL and")
		fmt.Println("ANTHROPIC_API_KEY (no code change) to see the model answer from the KB.")
	}
}
