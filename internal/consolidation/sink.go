// Package consolidation wires the Transactional Outbox (internal/memory) to the
// long-term memory sinks (internal/facts, internal/semantic). It is the "Phase
// 2 worker body" of docs/architecture/consistency.md: it lives here, above all
// three tiers, so the memory package never imports facts/semantic and the
// dependency graph stays acyclic.
//
// The Sink is deliberately idempotent end to end: facts.Store.Save upserts by a
// content-derived id and semantic.Store.Upsert keys on a deterministic UUIDv5,
// so an at-least-once redelivery (a retry after a partial failure, or a
// cold-boot re-drain) overwrites in place and never duplicates.
package consolidation

import (
	"context"
	"fmt"
	"sync"

	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/memory"
	"github.com/Luum-Home/aisotropy/internal/semantic"
	"github.com/Luum-Home/aisotropy/internal/tenancy"
)

// Sink consolidates one drained outbox event into the facts (FTS5) and semantic
// (vector) tiers. It implements memory.ConsolidationSink. Any nil tier is
// skipped, so a deployment can run facts-only or semantic-only.
type Sink struct {
	Facts     facts.Store
	Extractor facts.Extractor
	Semantic  semantic.Store
	Embedder  semantic.Embedder
}

// Consolidate distils the event into facts and (if configured) embeds it into
// the semantic tier. It returns an error if either sink write fails, so the
// outbox leaves the event pending and retries — hence the mandatory
// idempotency of both writes.
func (s Sink) Consolidate(ctx context.Context, e memory.OutboxEvent) error {
	// Sync the two sinks IN PARALLEL (docs/architecture/consistency.md): they
	// are independent stores, so facts (FTS5) and semantic (vectors) proceed
	// concurrently. The event is only considered done when BOTH succeed — any
	// error leaves it pending for an idempotent retry — so we return the first
	// error observed.
	var (
		wg               sync.WaitGroup
		factsErr, semErr error
	)
	wg.Add(2)
	go func() { defer wg.Done(); factsErr = s.consolidateFacts(ctx, e) }()
	go func() { defer wg.Done(); semErr = s.consolidateSemantic(ctx, e) }()
	wg.Wait()

	if factsErr != nil {
		return factsErr
	}
	return semErr
}

func (s Sink) consolidateFacts(ctx context.Context, e memory.OutboxEvent) error {
	if s.Facts == nil || s.Extractor == nil {
		return nil
	}
	// Route identity through the blessed constructor (ADR-0011), threading ctx
	// (rather than facts.NewScope's internal context.Background()) so the
	// validation honors the caller's cancellation.
	scope, err := facts.ScopeFromContext(tenancy.WithUser(tenancy.WithTenant(ctx, e.Tenant), e.User))
	if err != nil {
		return fmt.Errorf("consolidation: invalid facts scope: %w", err)
	}
	for _, f := range s.Extractor.Extract(scope, facts.Turn{UserMessage: e.Content}) {
		if err := s.Facts.Save(ctx, scope, f); err != nil {
			return fmt.Errorf("consolidation: save fact: %w", err)
		}
	}
	return nil
}

func (s Sink) consolidateSemantic(ctx context.Context, e memory.OutboxEvent) error {
	if s.Semantic == nil || s.Embedder == nil {
		return nil
	}
	vecs, err := s.Embedder.Embed(ctx, []string{e.Content})
	if err != nil {
		return fmt.Errorf("consolidation: embed: %w", err)
	}
	if len(vecs) == 0 {
		return nil
	}
	// Route identity through the blessed constructor (ADR-0011), threading ctx
	// (rather than semantic.NewScope's internal context.Background()) so the
	// validation honors the caller's cancellation — mirroring
	// consolidateFacts above.
	scope, err := semantic.ScopeFromContext(tenancy.WithUser(tenancy.WithTenant(ctx, e.Tenant), e.User))
	if err != nil {
		return fmt.Errorf("consolidation: invalid semantic scope: %w", err)
	}
	point := semantic.Point{
		ID:       semantic.PointID(scope, e.ID),
		TenantID: e.Tenant,
		UserID:   e.User,
		Content:  e.Content,
		Vector:   vecs[0],
	}
	if err := s.Semantic.Upsert(ctx, scope, []semantic.Point{point}); err != nil {
		return fmt.Errorf("consolidation: upsert vector: %w", err)
	}
	return nil
}
