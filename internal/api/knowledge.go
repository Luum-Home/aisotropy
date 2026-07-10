// Package api: the HTTP arm of the knowledge-ingestion surface (ADR-0018).
// Mirrors chat.go's route-registration pattern (Huma operation, tenant/user
// identity in the path, never the body) so document ingestion follows the
// same multi-tenancy gate as every other route.
package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// KnowledgeDocument is one document to ingest, per ADR-0018. The tags govern
// both HTTP validation and the published JSON Schema (the struct-as-contract
// thesis, docs/vision/thesis.md).
type KnowledgeDocument struct {
	ID      string `json:"id" doc:"Caller-chosen document identifier; re-ingesting the same id idempotently replaces its chunks" example:"handbook-v3"`
	Content string `json:"content" doc:"Plain-text document body to chunk, embed, and store"`
	Source  string `json:"source,omitempty" doc:"Optional provenance (url, filename, section) surfaced on retrieved chunks for citations"`
}

// KnowledgeRequest is the POST .../knowledge body: a batch of documents to
// ingest in one call.
type KnowledgeRequest struct {
	Documents []KnowledgeDocument `json:"documents" doc:"Documents to chunk, embed, and store as KindDocument vectors"`
}

// KnowledgeInput combines the tenant/user-scoped path params with the
// validated body, mirroring ChatInput: identity travels in the path, never
// the body (the multi-tenancy gate).
type KnowledgeInput struct {
	TenantID string `path:"tenant_id" pattern:"^[a-zA-Z0-9_-]{1,64}$" doc:"Unique customer/tenant ID" example:"acme"`
	UserID   string `path:"user_id" pattern:"^[a-zA-Z0-9_-]{1,64}$" doc:"Unique user ID within the tenant" example:"alice"`
	Body     KnowledgeRequest
}

// KnowledgeOutput is the strict output contract.
type KnowledgeOutput struct {
	Body struct {
		ChunksIngested int `json:"chunks_ingested" doc:"Number of KindDocument chunks written across all submitted documents"`
	}
}

// RegisterKnowledge mounts the document-ingestion endpoint (ADR-0018) on the
// Huma API: POST /v1/tenant/{tenant_id}/user/{user_id}/knowledge. store and
// emb are the runtime's semantic store/embedder — the same pair the
// streaming chat route's RAG hydration reads from (hydrateSystemPrompt in
// chat_stream.go), so an ingested document is retrievable via chat
// immediately, with no separate wiring. opts tunes chunking (size/overlap in
// runes; semantic.DefaultChunkOptions() is a sensible default).
func RegisterKnowledge(api huma.API, store semantic.Store, emb semantic.Embedder, opts semantic.ChunkOptions) {
	huma.Register(api, huma.Operation{
		OperationID: "ingest-knowledge",
		Method:      httpMethodPost,
		Path:        "/v1/tenant/{tenant_id}/user/{user_id}/knowledge",
		Summary:     "Ingest documents into the semantic tier (KindDocument, ADR-0018)",
		Description: "Validates the contract, isolates by tenant/user via ctx, chunks each " +
			"document's content, embeds the chunks (BYO Embedder), and upserts them as " +
			"KindDocument vectors scoped to (tenant_id, user_id). Idempotent: re-ingesting " +
			"the same document id replaces its chunks in place. Ingested documents are " +
			"retrievable immediately via the chat/chat-stream routes' semantic RAG hydration.",
	}, func(ctx context.Context, in *KnowledgeInput) (*KnowledgeOutput, error) {
		// Identity in the path, validated through semantic.NewScope
		// (ADR-0011): a bad tenant/user is rejected before any chunk/embed
		// work happens. There is no session concept for ingestion (unlike
		// chat), so this does not reuse withIdentity/memory.Key.Validate,
		// which requires a non-empty session.
		sc, err := semantic.NewScope(in.TenantID, in.UserID) //nolint:contextcheck // semantic.NewScope has no context parameter (internal/semantic, out of scope for this cleanup)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid tenant/user identity", err)
		}
		ctx = context.WithValue(ctx, tenantCtxKey{}, in.TenantID)
		ctx = context.WithValue(ctx, userCtxKey{}, in.UserID)

		docs := make([]semantic.Document, 0, len(in.Body.Documents))
		for _, d := range in.Body.Documents {
			docs = append(docs, semantic.Document{ID: d.ID, Content: d.Content, Source: d.Source})
		}

		n, err := semantic.Ingest(ctx, store, emb, sc, opts, docs)
		if err != nil {
			return nil, huma.Error500InternalServerError("knowledge ingestion failed", err)
		}

		out := &KnowledgeOutput{}
		out.Body.ChunksIngested = n
		return out, nil
	})
}
