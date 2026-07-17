// Package engram is the Fact-kind aggregate facade mandated by ADR-0017
// (docs/decisions/0017-semantic-tier-multi-kind-memory.md), phase 2. It is
// the corrected, *scoped* G1 aggregate root: not an aggregate over the whole
// semantic tier (a distilled fact and a raw conversational turn are
// different memories with independent lifecycles — see the ADR's
// "Alternatives considered" E), but the sole owner of the one kind where a
// vector truly is a projection of a fact — semantic.KindFact.
//
// Store composes the facts tier (internal/facts) and the semantic tier
// (internal/semantic) and makes "a fact and its Fact-kind vector live and
// die together" an invariant enforced by construction: Save dual-indexes,
// Supersede/Delete remove both sides. Per the ADR's "Mandatory going
// forward", every Fact-kind vector mutation MUST go through this facade —
// never internal/semantic directly for Fact-kind vectors.
//
// Store deliberately does not touch semantic.KindTurn or semantic.KindDocument
// vectors: the Turn path stays in internal/consolidation (bounded in a later
// phase) and Document ingestion is a follow-up feature.
package engram

import (
	"context"
	"fmt"
	"time"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/semantic"
	"github.com/Luum-Home/aisotropy/internal/tenancy"
)

// Store is the Fact-kind aggregate root: a facade over the facts tier and the
// semantic tier's Fact-kind vectors. Semantic and Embedder are optional (nil
// means facts-only — no dual-indexing), so facts-only deployments keep
// working unchanged; Facts is required for every operation.
type Store struct {
	Facts    facts.Store
	Semantic semantic.Store
	Embedder semantic.Embedder
	// MinImportance gates dual-indexing (ADR-0017 open question #2): a fact's
	// content is embedded into a Fact-kind vector only when its Importance is
	// >= MinImportance. The zero value always dual-indexes every fact.
	MinImportance float64
}

// Save writes f to the facts tier and, when Semantic/Embedder are configured
// and f clears the importance gate, dual-indexes its content as a
// semantic.KindFact vector keyed by the fact id — so a fresh fact is born
// with its vector already in place.
func (s Store) Save(ctx context.Context, sc facts.Scope, f facts.Fact) error {
	if err := s.Facts.Save(ctx, sc, f); err != nil {
		return fmt.Errorf("engram: save fact: %w", err)
	}
	return s.indexFact(ctx, sc, f)
}

// Supersede marks oldID as replaced by newFact in the facts tier, deletes
// oldID's Fact-kind vector (closing the "stale vector after supersede" gap —
// ADR-0017's bug #1), and dual-indexes newFact under the same rule as Save.
func (s Store) Supersede(ctx context.Context, sc facts.Scope, oldID string, newFact facts.Fact) error {
	if err := s.Facts.Supersede(ctx, sc, oldID, newFact); err != nil {
		return fmt.Errorf("engram: supersede fact: %w", err)
	}
	if s.Semantic != nil {
		semScope, err := s.semanticScope(ctx, sc)
		if err != nil {
			return err
		}
		if err := s.Semantic.Delete(ctx, semScope, []string{semantic.PointID(semScope, semantic.KindFact, oldID)}); err != nil {
			return fmt.Errorf("engram: delete superseded fact vector: %w", err)
		}
	}
	return s.indexFact(ctx, sc, newFact)
}

// Delete removes the facts with the given ids from the facts tier and their
// Fact-kind vectors from the semantic tier (when configured) — closing
// ADR-0017's bug #2, the sleep cycle's dedup dropping a fact but leaving its
// vector behind. Unknown ids are ignored by both tiers.
func (s Store) Delete(ctx context.Context, sc facts.Scope, ids []string) error {
	if err := s.Facts.Delete(ctx, sc, ids); err != nil {
		return fmt.Errorf("engram: delete facts: %w", err)
	}
	if s.Semantic == nil || len(ids) == 0 {
		return nil
	}
	semScope, err := s.semanticScope(ctx, sc)
	if err != nil {
		return err
	}
	pointIDs := make([]string, len(ids))
	for i, id := range ids {
		pointIDs[i] = semantic.PointID(semScope, semantic.KindFact, id)
	}
	if err := s.Semantic.Delete(ctx, semScope, pointIDs); err != nil {
		return fmt.Errorf("engram: delete fact vectors: %w", err)
	}
	return nil
}

// indexFact embeds f.Content and upserts it as a semantic.KindFact vector, or
// no-ops when Semantic/Embedder are not configured or f does not clear the
// importance gate.
func (s Store) indexFact(ctx context.Context, sc facts.Scope, f facts.Fact) error {
	if s.Semantic == nil || s.Embedder == nil || f.Importance < s.MinImportance {
		return nil
	}
	vecs, err := semantic.EmbedPassage(ctx, s.Embedder, []string{f.Content})
	if err != nil {
		return fmt.Errorf("engram: embed fact: %w", err)
	}
	if len(vecs) == 0 {
		return nil
	}
	semScope, err := s.semanticScope(ctx, sc)
	if err != nil {
		return err
	}
	point := semantic.Point{
		ID:       semantic.PointID(semScope, semantic.KindFact, f.ID),
		TenantID: f.TenantID,
		UserID:   f.UserID,
		Kind:     semantic.KindFact,
		Content:  f.Content,
		Vector:   vecs[0],
		// CreatedAt is the write-time timestamp (ADR-0017 phase 3): a
		// re-indexed fact (Save/Supersede) gets a fresh CreatedAt on its
		// vector, consistent with PruneTurns only ever considering KindTurn
		// points — Fact-kind vectors are never pruned by age.
		CreatedAt: time.Now().UTC(),
	}
	if err := s.Semantic.Upsert(ctx, semScope, []semantic.Point{point}); err != nil {
		return fmt.Errorf("engram: upsert fact vector: %w", err)
	}
	return nil
}

// semanticScope derives the semantic Scope for sc's (tenant, user), routed
// through semantic.ScopeFromContext (ADR-0011 blessed constructor) rather
// than a bare semantic.Scope{...} literal. ctx is threaded through (rather
// than semantic.NewScope's internal context.Background()) so the validation
// honors the caller's cancellation, mirroring internal/consolidation and
// internal/sleepcycle.
func (s Store) semanticScope(ctx context.Context, sc facts.Scope) (semantic.Scope, error) {
	semScope, err := semantic.ScopeFromContext(tenancy.WithUser(tenancy.WithTenant(ctx, sc.Tenant()), sc.User()))
	if err != nil {
		// semScope is already ScopeFromContext's own zero value on this path;
		// no bare semantic.Scope{} literal needed here (scopecheck, ADR-0011).
		return semScope, fmt.Errorf("engram: invalid semantic scope: %w", err)
	}
	return semScope, nil
}
