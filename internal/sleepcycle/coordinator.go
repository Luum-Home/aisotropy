package sleepcycle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Luum-Home/aisotropy/internal/facts"
)

// FactStore is the subset of facts.Store the sleep cycle needs: a cross-tenant
// export for the batch input, and a scoped delete to apply the dedup's cleanup.
type FactStore interface {
	ExportAll(ctx context.Context) ([]facts.Fact, error)
	Delete(ctx context.Context, sc facts.Scope, ids []string) error
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
		// defensively — it simply won't be deduped this cycle.
		sc, err := facts.NewScope(f.TenantID, f.UserID)
		if err != nil {
			continue
		}
		byScope[sc] = append(byScope[sc], id)
	}
	for sc, ids := range byScope {
		if err := c.Store.Delete(ctx, sc, ids); err != nil {
			return summary, fmt.Errorf("sleepcycle: cleanup delete: %w", err)
		}
	}
	return summary, nil
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
