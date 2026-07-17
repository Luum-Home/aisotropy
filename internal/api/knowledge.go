// Package api: the HTTP arm of the knowledge-ingestion surface (ADR-0018,
// ADR-0019). Mirrors chat.go's route-registration pattern (Huma operation,
// tenant/user identity in the path, never the body) so document ingestion
// follows the same multi-tenancy gate as every other route.
package api

import (
	"context"
	"log/slog"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// httpMethodDelete is the HTTP method the knowledge-deletion routes register
// under (ADR-0018 open q#3 / ADR-0019 open q#1).
const httpMethodDelete = "DELETE"

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
func RegisterKnowledge(api huma.API, store semantic.Store, emb semantic.Embedder, opts semantic.ChunkOptions, guard *Guard) {
	huma.Register(api, huma.Operation{
		OperationID: "ingest-knowledge",
		Method:      httpMethodPost,
		Path:        "/v1/tenant/{tenant_id}/user/{user_id}/knowledge",
		Summary:     "Ingest documents into the semantic tier (KindDocument, ADR-0018)",
		Description: "Validates the contract, isolates by tenant/user via ctx, scans each " +
			"document for prompt-injection payloads (stored/indirect injection guardrail), " +
			"chunks each document's content, embeds the chunks (BYO Embedder), and upserts them as " +
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

		// Stored / indirect prompt-injection gate (Gap 3): score each document's
		// text BEFORE it is embedded/stored. A flagged doc is always logged (a
		// signal in the record); with injection-blocking enabled the whole ingest
		// is rejected rather than silently poisoning tenant/user memory.
		if berr := scanDocumentsForInjection(ctx, guard, in.TenantID, in.UserID, in.Body.Documents); berr != nil {
			return nil, berr
		}

		n, err := semantic.Ingest(ctx, store, emb, sc, opts, documentsFromRequest(in.Body.Documents))
		if err != nil {
			return nil, huma.Error500InternalServerError("knowledge ingestion failed", err)
		}

		out := &KnowledgeOutput{}
		out.Body.ChunksIngested = n
		return out, nil
	})
}

// scanDocumentsForInjection runs the injection detector over each document's
// content before it is embedded/stored — the stored/indirect prompt-injection
// gate (Gap 3). It ALWAYS logs a WARN naming the flagged doc id + fired signals
// (so a flagged document is never persisted without a signal in the record) and,
// when the operator has injection-blocking enabled (guard.BlockInjection),
// returns a 400 naming the flagged docs so the whole ingest is rejected. A nil
// guard (or nil aggregator) is a pass-through, so wiring that passes nil — and
// the tests that do — is unchanged. user may be empty for the tenant-shared
// route (no user path segment).
func scanDocumentsForInjection(ctx context.Context, guard *Guard, tenant, user string, docs []KnowledgeDocument) error {
	if guard == nil || guard.G == nil {
		return nil
	}
	det := guard.G.Detector()
	var flagged []string
	for _, d := range docs {
		v := det.Inspect(d.Content)
		if !v.Suspicious {
			continue
		}
		flagged = append(flagged, d.ID)
		slog.WarnContext(ctx, "knowledge ingest flagged possible prompt injection in document content",
			"tenant_id", tenant, "user_id", user,
			"doc_id", d.ID, "signals", v.Signals, "score", v.Score,
			"blocking", guard.BlockInjection,
		)
	}
	if len(flagged) > 0 && guard.BlockInjection {
		return huma.Error400BadRequest("document(s) rejected by ingest guardrail (possible prompt injection): " + strings.Join(flagged, ", "))
	}
	return nil
}

// documentsFromRequest maps the wire KnowledgeDocument batch onto
// internal/semantic.Document, shared by every ingest route (user-scoped and
// tenant-shared).
func documentsFromRequest(reqDocs []KnowledgeDocument) []semantic.Document {
	docs := make([]semantic.Document, 0, len(reqDocs))
	for _, d := range reqDocs {
		docs = append(docs, semantic.Document{ID: d.ID, Content: d.Content, Source: d.Source})
	}
	return docs
}

// KnowledgeDeleteInput is the DELETE .../knowledge/{doc_id} path, user-scoped
// (ADR-0018 open q#3).
type KnowledgeDeleteInput struct {
	TenantID string `path:"tenant_id" pattern:"^[a-zA-Z0-9_-]{1,64}$" doc:"Unique customer/tenant ID" example:"acme"`
	UserID   string `path:"user_id" pattern:"^[a-zA-Z0-9_-]{1,64}$" doc:"Unique user ID within the tenant" example:"alice"`
	DocID    string `path:"doc_id" pattern:"^[a-zA-Z0-9_.-]{1,128}$" doc:"Document id previously used to ingest this document" example:"handbook-v3"`
}

// KnowledgeDeleteOutput is the strict output contract for both delete
// routes (user-scoped and tenant-shared).
type KnowledgeDeleteOutput struct {
	Body struct {
		Deleted int `json:"deleted" doc:"Number of KindDocument chunks removed"`
	}
}

// RegisterKnowledgeDelete mounts the user-scoped document-deletion endpoint
// (ADR-0018 open q#3): DELETE /v1/tenant/{tenant_id}/user/{user_id}/knowledge/{doc_id}.
// Removes every chunk of the given document id, regardless of how many
// chunks it has (Store.DeleteBySource), scoped to (tenant_id, user_id) —
// never touching another tenant/user's document even under a colliding id.
func RegisterKnowledgeDelete(api huma.API, store semantic.Store) {
	huma.Register(api, huma.Operation{
		OperationID: "delete-knowledge",
		Method:      httpMethodDelete,
		Path:        "/v1/tenant/{tenant_id}/user/{user_id}/knowledge/{doc_id}",
		Summary:     "Delete a document's chunks from the semantic tier (KindDocument, ADR-0018 open q#3)",
		Description: "Validates the contract, isolates by tenant/user via the scoped store call, and " +
			"removes every KindDocument chunk previously ingested under doc_id — idempotent: deleting " +
			"an unknown or already-deleted doc_id returns deleted:0, never an error.",
	}, func(ctx context.Context, in *KnowledgeDeleteInput) (*KnowledgeDeleteOutput, error) {
		sc, err := semantic.NewScope(in.TenantID, in.UserID) //nolint:contextcheck // semantic.NewScope has no context parameter (internal/semantic, out of scope for this cleanup)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid tenant/user identity", err)
		}
		n, err := store.DeleteBySource(ctx, sc, semantic.KindDocument, in.DocID)
		if err != nil {
			return nil, huma.Error500InternalServerError("knowledge deletion failed", err)
		}
		out := &KnowledgeDeleteOutput{}
		out.Body.Deleted = n
		return out, nil
	})
}

// TenantKnowledgeInput is the POST /v1/tenant/{tenant_id}/knowledge body: a
// tenant-shared ingest, no user path segment (ADR-0019).
type TenantKnowledgeInput struct {
	TenantID string `path:"tenant_id" pattern:"^[a-zA-Z0-9_-]{1,64}$" doc:"Unique customer/tenant ID" example:"acme"`
	Body     KnowledgeRequest
}

// RegisterTenantKnowledge mounts the tenant-shared document-ingestion
// endpoint (ADR-0019): POST /v1/tenant/{tenant_id}/knowledge. Ingests into
// the tenant-shared bucket (semantic.NewTenantScope, an empty-user Scope),
// so every user of tenant retrieves the same corpus without re-ingesting it
// — hydrateSystemPrompt merges this bucket's KindDocument points into every
// user's RAG hydration. store/emb/opts mirror RegisterKnowledge exactly.
func RegisterTenantKnowledge(api huma.API, store semantic.Store, emb semantic.Embedder, opts semantic.ChunkOptions, guard *Guard) {
	huma.Register(api, huma.Operation{
		OperationID: "ingest-tenant-knowledge",
		Method:      httpMethodPost,
		Path:        "/v1/tenant/{tenant_id}/knowledge",
		Summary:     "Ingest documents into the tenant-shared semantic bucket (KindDocument, ADR-0019)",
		Description: "Validates the contract, scans each document for prompt-injection payloads " +
			"(stored/indirect injection guardrail — critical here because a tenant-shared doc " +
			"reaches EVERY user of the tenant), chunks each document's content, embeds the chunks " +
			"(BYO Embedder), and upserts them as KindDocument vectors scoped to a tenant-only, " +
			"empty-user Scope (semantic.NewTenantScope) — visible to every user of tenant_id via " +
			"the chat/chat-stream routes' semantic RAG hydration merge. Idempotent, exactly like " +
			"the user-scoped POST .../user/{user_id}/knowledge.",
	}, func(ctx context.Context, in *TenantKnowledgeInput) (*KnowledgeOutput, error) {
		sc, err := semantic.NewTenantScope(in.TenantID) //nolint:contextcheck // semantic.NewTenantScope has no context parameter (internal/semantic, out of scope for this cleanup)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid tenant identity", err)
		}
		ctx = context.WithValue(ctx, tenantCtxKey{}, in.TenantID)

		// Stored / indirect prompt-injection gate (Gap 3). A tenant-shared
		// document is the highest-blast-radius ingest surface: a single poisoned
		// doc would be hydrated into every user's context (cross-user hijack), so
		// scan before it is embedded/stored.
		if berr := scanDocumentsForInjection(ctx, guard, in.TenantID, "", in.Body.Documents); berr != nil {
			return nil, berr
		}

		n, err := semantic.Ingest(ctx, store, emb, sc, opts, documentsFromRequest(in.Body.Documents))
		if err != nil {
			return nil, huma.Error500InternalServerError("tenant knowledge ingestion failed", err)
		}

		out := &KnowledgeOutput{}
		out.Body.ChunksIngested = n
		return out, nil
	})
}

// TenantKnowledgeDeleteInput is the DELETE .../tenant/{tenant_id}/knowledge/{doc_id}
// path, tenant-shared (ADR-0019 open q#1).
type TenantKnowledgeDeleteInput struct {
	TenantID string `path:"tenant_id" pattern:"^[a-zA-Z0-9_-]{1,64}$" doc:"Unique customer/tenant ID" example:"acme"`
	DocID    string `path:"doc_id" pattern:"^[a-zA-Z0-9_.-]{1,128}$" doc:"Document id previously used to ingest this tenant-shared document" example:"handbook-v3"`
}

// RegisterTenantKnowledgeDelete mounts the tenant-shared document-deletion
// endpoint (ADR-0019 open q#1): DELETE /v1/tenant/{tenant_id}/knowledge/{doc_id}.
// Same delete-by-source mechanism as RegisterKnowledgeDelete, applied to the
// tenant-shared Scope instead of a (tenant,user) Scope.
func RegisterTenantKnowledgeDelete(api huma.API, store semantic.Store) {
	huma.Register(api, huma.Operation{
		OperationID: "delete-tenant-knowledge",
		Method:      httpMethodDelete,
		Path:        "/v1/tenant/{tenant_id}/knowledge/{doc_id}",
		Summary:     "Delete a tenant-shared document's chunks from the semantic tier (KindDocument, ADR-0019)",
		Description: "Removes every KindDocument chunk previously ingested under doc_id in the " +
			"tenant-shared bucket — idempotent: deleting an unknown or already-deleted doc_id " +
			"returns deleted:0, never an error. Never touches a user-private document (a disjoint " +
			"scope) or another tenant's shared bucket.",
	}, func(ctx context.Context, in *TenantKnowledgeDeleteInput) (*KnowledgeDeleteOutput, error) {
		sc, err := semantic.NewTenantScope(in.TenantID) //nolint:contextcheck // semantic.NewTenantScope has no context parameter (internal/semantic, out of scope for this cleanup)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid tenant identity", err)
		}
		n, err := store.DeleteBySource(ctx, sc, semantic.KindDocument, in.DocID)
		if err != nil {
			return nil, huma.Error500InternalServerError("tenant knowledge deletion failed", err)
		}
		out := &KnowledgeDeleteOutput{}
		out.Body.Deleted = n
		return out, nil
	})
}
