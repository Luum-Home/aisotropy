package sleepcycle

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/Luum-Home/aisotropy/internal/engram"
	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/semantic"
	"github.com/Luum-Home/aisotropy/internal/tenancy"
)

// FactStore is the subset of facts.Store the sleep cycle needs for the batch
// input: a cross-tenant export of every current fact.
type FactStore interface {
	ExportAll(ctx context.Context) ([]facts.Fact, error)
}

// Runner runs one dedup pass over an NDJSON input, writing a Parquet output and
// returning the Summary. *Worker (the real DataFusion subprocess) satisfies it;
// tests inject a fake.
type Runner interface {
	Run(ctx context.Context, inputPath, outputPath string) (Summary, error)
}

// Coordinator drives one full sleep-cycle consolidation: export the facts, run
// the DataFusion dedup in the Rust worker, then delete the dropped duplicates
// from the live store — closing the loop the request path can't afford
// (docs/architecture/consistency.md, docs/components/datafusion-sleep-cycle.md).
type Coordinator struct {
	Store  FactStore
	Runner Runner
	// Engram applies the dedup cleanup: it removes each dropped fact AND its
	// Fact-kind vector (ADR-0017 phase 2), so a deduped fact never leaves a
	// stale vector behind in the semantic tier. Fact-kind vector mutation
	// MUST go through this facade, never a direct facts.Store.Delete — a nil
	// Semantic/Embedder on Engram still deletes the fact rows (facts-only).
	Engram engram.Store
	// Semantic is the semantic store the Turn-kind pruner (ADR-0017 phase 3)
	// runs PruneTurns against, once per active (tenant,user) scope each
	// pass. Nil disables Turn pruning entirely — a deployment with no
	// semantic tier configured, or one that has not opted in.
	Semantic semantic.Store
	// TurnMaxAge is the age-TTL policy PruneTurns applies: a KindTurn point
	// older than TurnMaxAge is evicted. <= 0 disables the age policy.
	TurnMaxAge time.Duration
	// TurnMaxCount is the per-(tenant,user) cap PruneTurns applies: only the
	// TurnMaxCount most-recent KindTurn points survive. <= 0 disables the
	// count policy.
	TurnMaxCount int
	// Dir is the scratch directory for the NDJSON/Parquet artifacts; "" uses
	// the OS temp dir. Each run gets its own subdir, removed afterward.
	Dir string
}

// RunOnce performs a single consolidation pass and returns the worker's Summary
// (zero-valued when there is nothing to consolidate).
func (c Coordinator) RunOnce(ctx context.Context) (Summary, error) {
	all, err := c.Store.ExportAll(ctx)
	if err != nil {
		return Summary{}, fmt.Errorf("sleepcycle: export: %w", err)
	}
	if len(all) == 0 {
		return Summary{}, nil
	}

	base := c.Dir
	if base == "" {
		base = os.TempDir()
	}
	work, err := os.MkdirTemp(base, "sleepcycle-")
	if err != nil {
		return Summary{}, fmt.Errorf("sleepcycle: scratch dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(work) }()

	input := filepath.Join(work, "facts.ndjson")
	output := filepath.Join(work, "deduped.parquet")

	recs := make([]Record, len(all))
	byID := make(map[string]facts.Fact, len(all))
	for i, f := range all {
		recs[i] = Record{
			ID: f.ID, TenantID: f.TenantID, UserID: f.UserID, Content: f.Content,
			Importance: f.Importance, CreatedAt: f.CreatedAt.UnixMilli(), UpdatedAt: f.UpdatedAt.UnixMilli(),
		}
		byID[f.ID] = f
	}
	if err := WriteNDJSON(input, recs); err != nil {
		return Summary{}, err
	}

	summary, err := c.Runner.Run(ctx, input, output)
	if err != nil {
		return Summary{}, err
	}

	// Apply the cleanup: delete each dropped id within its own (tenant,user)
	// scope, so the isolation gate holds for the write.
	byScope := make(map[facts.Scope][]string)
	for _, id := range summary.DroppedIDs {
		f, ok := byID[id]
		if !ok {
			continue // an id we didn't export; ignore defensively
		}
		// Route identity through the blessed constructor (ADR-0011): validate
		// the stored tenant/user via internal/tenancy rather than building a
		// bare facts.Scope literal. A malformed stored identity is skipped
		// defensively — it simply won't be deduped this cycle. ctx is threaded
		// through (rather than facts.NewScope's internal context.Background())
		// so the validation honors the caller's cancellation.
		sc, err := facts.ScopeFromContext(tenancy.WithUser(tenancy.WithTenant(ctx, f.TenantID), f.UserID))
		if err != nil {
			continue
		}
		byScope[sc] = append(byScope[sc], id)
	}
	for sc, ids := range byScope {
		// Route the dedup cleanup through the engram facade, not a bare
		// facts.Store.Delete, so the dropped fact's Fact-kind vector is
		// removed in the same operation (ADR-0017 bug #2).
		if err := c.Engram.Delete(ctx, sc, ids); err != nil {
			return summary, fmt.Errorf("sleepcycle: cleanup delete: %w", err)
		}
	}

	// Turn-kind eviction (ADR-0017 phase 3): bound raw-turn vectors for every
	// active (tenant,user) scope discovered in this pass's fact export. Runs
	// regardless of whether the dedup pass above dropped anything — a quiet
	// fact tier can still have unbounded Turn growth from live chat traffic.
	c.pruneTurns(ctx, all)

	return summary, nil
}

// pruneTurns evicts KindTurn semantic vectors (ADR-0017 phase 3) for every
// distinct (tenant,user) scope present in rows, applying the configured
// age-TTL/count-cap policy via Semantic.PruneTurns. A nil Semantic store or a
// fully-disabled policy (TurnMaxAge<=0 AND TurnMaxCount<=0) is a no-op.
// Best-effort per scope: a malformed stored identity or a single scope's
// prune failure is logged and does not abort the pass for the other scopes —
// mirroring the defensive-skip behavior of the dedup cleanup above.
//
// Scope discovery is derived from facts.Store.ExportAll, so it is
// necessarily limited to tenants/users that currently have at least one
// fact; a scope with live chat turns but zero current facts is invisible to
// this pass (a documented limitation, not an isolation gap — it only means
// that scope's Turn pruning waits for a pass where it has a fact).
func (c Coordinator) pruneTurns(ctx context.Context, rows []facts.Fact) {
	if c.Semantic == nil || (c.TurnMaxAge <= 0 && c.TurnMaxCount <= 0) {
		return
	}
	seen := make(map[string]struct{}, len(rows))
	for _, f := range rows {
		key := f.TenantID + "\x00" + f.UserID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		sc, err := semantic.ScopeFromContext(tenancy.WithUser(tenancy.WithTenant(ctx, f.TenantID), f.UserID))
		if err != nil {
			continue // malformed stored identity: skip defensively, as the dedup cleanup above does
		}
		if _, err := c.Semantic.PruneTurns(ctx, sc, c.TurnMaxAge, c.TurnMaxCount); err != nil {
			slog.ErrorContext(ctx, "sleepcycle: turn pruning failed", "tenant_id", f.TenantID, "user_id", f.UserID, "err", err)
		}
	}
}

// PruneTurnsOnce runs one Turn-eviction pass on its own — WITHOUT the DataFusion
// dedup, so it needs no Runner / Rust worker binary: it discovers active
// (tenant,user) scopes via ExportAll and prunes each scope's KindTurn vectors
// past TurnMaxAge/TurnMaxCount. Turn bounding is a pure-Go semantic-tier
// maintenance concern (ADR-0017 Phase 3), so it MUST be runnable independent of
// the dormant-by-default sleep cycle — otherwise KindTurn vectors grow unbounded
// on any deployment without AISOTROPY_SLEEPCYCLE_BIN. A no-op when Semantic is
// nil or no bound is configured.
func (c Coordinator) PruneTurnsOnce(ctx context.Context) error {
	if c.Semantic == nil || (c.TurnMaxAge <= 0 && c.TurnMaxCount <= 0) {
		return nil
	}
	rows, err := c.Store.ExportAll(ctx)
	if err != nil {
		return fmt.Errorf("sleepcycle: prune export: %w", err)
	}
	c.pruneTurns(ctx, rows)
	return nil
}

// RunTurnPruneScheduler calls PruneTurnsOnce every interval until ctx is
// cancelled. onErr (if non-nil) receives each pass's error. It is independent of
// RunScheduler / the Rust dedup worker, so Turn bounding stays active even when
// the DataFusion sleep cycle is disabled. The caller owns the goroutine via ctx.
func (c Coordinator) RunTurnPruneScheduler(ctx context.Context, interval time.Duration, onErr func(error)) {
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.PruneTurnsOnce(ctx); err != nil && onErr != nil {
				onErr(err)
			}
		}
	}
}

// RunScheduler calls RunOnce every interval until ctx is cancelled. onResult (if
// non-nil) receives each pass's Summary and error, for logging/metrics. The
// caller owns the goroutine's lifetime via ctx.
func (c Coordinator) RunScheduler(ctx context.Context, interval time.Duration, onResult func(Summary, error)) {
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s, err := c.RunOnce(ctx)
			if onResult != nil {
				onResult(s, err)
			}
		}
	}
}
