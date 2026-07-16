package api

import (
	"context"
	"fmt"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// BitrateHydrator is the production implementation of bitrate.Hydrator (the
// anti-magic seam, ADR-0002). It builds the two contexts the Bet 6 measurement
// scores by calling the EXACT request-path hydration code — hydrateSystemPrompt
// for the "with memory" context, and the persona base prompt for the "without
// memory" baseline — so there is no second, parallel hydration path that could
// drift from what the streaming handler actually serves.
//
// It carries the same collaborators RegisterStream received, so ContextWith
// reproduces the request's CONSOLIDATED system prompt (persona + facts + semantic)
// via the identical hydrateSystemPrompt code — but deliberately passes a nil
// working memory. bpt measures learned/consolidated memory (what trends down over
// time), not the live session buffer; and the buffer would still hold the held-out
// turn being scored, which is exactly the leakage the held-out split must avoid.
type BitrateHydrator struct {
	Store    facts.Store
	Semantic semantic.Store
	Embedder semantic.Embedder
	// Reranker mirrors the streaming handler's configured reranker so the "with
	// memory" context the bitrate measurement scores matches what the request
	// path actually injects (anti-magic, ADR-0002). Nil when no reranker is
	// configured — the same default the streaming handler runs under.
	Reranker semantic.Reranker
	Persona  PersonaProvider
}

// ContextWith returns the hydrated "with memory" system prompt for (tenantID,
// userID) under productID, using query as the facts/semantic retrieval query. It
// calls the identical hydrateSystemPrompt the streaming handler uses, with a nil
// working memory so only consolidated memory (facts + semantic, already excluding
// the held-out sample) contributes — never the live session buffer (leakage).
// sessionID is not used: consolidated memory is scoped by (tenant, user).
func (h BitrateHydrator) ContextWith(ctx context.Context, tenantID, userID, sessionID, productID, query string) (string, error) {
	key := memory.Key{Tenant: tenantID, User: userID, Session: sessionID}
	scope, err := facts.NewScope(tenantID, userID) //nolint:contextcheck // facts.NewScope has no context parameter (internal/facts, out of scope for this cleanup)
	if err != nil {
		return "", fmt.Errorf("bitrate hydrate scope: %w", err)
	}
	return hydrateSystemPrompt(ctx, nil, h.Store, h.Semantic, h.Embedder, h.Reranker, h.Persona, key, scope, productID, query)
}

// ContextWithout returns the bare persona base prompt for productID — the "no
// memory" baseline, exactly the branch hydrateSystemPrompt takes when a session
// has no summary/recent/facts/semantics to fold in.
func (h BitrateHydrator) ContextWithout(ctx context.Context, productID string) (string, error) {
	persona := h.Persona
	if persona == nil {
		persona = defaultPersonaProvider{}
	}
	base, err := persona.BasePrompt(ctx, productID)
	if err != nil {
		return "", fmt.Errorf("bitrate persona base prompt: %w", err)
	}
	return base, nil
}
