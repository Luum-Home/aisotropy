package sleepcycle

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace/noop"

	"github.com/Luum-Home/aisotropy/internal/engram"
	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// errFactStore is a FactStore whose ExportAll always fails, exercising the
// export error paths of RunOnce and PruneTurnsOnce without a real store.
type errFactStore struct{ err error }

func (e errFactStore) ExportAll(context.Context) ([]facts.Fact, error) { return nil, e.err }

// errSemanticPruner is a semantic.Store double whose PruneTurns fails, so the
// best-effort logging branch in pruneTurns is exercised.
type errSemanticPruner struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (e *errSemanticPruner) Upsert(context.Context, semantic.Scope, []semantic.Point) error {
	panic("Upsert not expected")
}
func (e *errSemanticPruner) Search(context.Context, semantic.Scope, []float32, int, ...semantic.Kind) ([]semantic.ScoredPoint, error) {
	panic("Search not expected")
}
func (e *errSemanticPruner) Delete(context.Context, semantic.Scope, []string) error {
	panic("Delete not expected")
}
func (e *errSemanticPruner) DeleteBySource(context.Context, semantic.Scope, semantic.Kind, string) (int, error) {
	panic("DeleteBySource not expected")
}
func (e *errSemanticPruner) PruneTurns(context.Context, semantic.Scope, time.Duration, int) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	return 0, e.err
}
func (e *errSemanticPruner) Close() error { return nil }

func (e *errSemanticPruner) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func TestRunOnceExportError(t *testing.T) {
	want := errors.New("boom export")
	c := Coordinator{Store: errFactStore{err: want}, Runner: fakeRunner{}}
	if _, err := c.RunOnce(context.Background()); err == nil || !errors.Is(err, want) {
		t.Fatalf("RunOnce error = %v, want wrap of %v", err, want)
	}
}

func TestRunOnceRunnerError(t *testing.T) {
	fs, err := facts.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	ctx := context.Background()
	sc, _ := facts.NewScope("acme", "alice")
	if err := fs.Save(ctx, sc, facts.Fact{ID: "f1", TenantID: "acme", UserID: "alice", Content: "x", Importance: 0.5}); err != nil {
		t.Fatal(err)
	}
	want := errors.New("worker exploded")
	c := Coordinator{Store: fs, Engram: engram.Store{Facts: fs}, Runner: fakeRunner{err: want}}
	if _, err := c.RunOnce(ctx); err == nil || !errors.Is(err, want) {
		t.Fatalf("RunOnce error = %v, want %v", err, want)
	}
}

// TestRunOnceCleanupDeleteError makes Engram.Delete fail by pointing the facade
// at a closed SQLite store while a separate in-memory store drives the export,
// verifying RunOnce surfaces the cleanup error.
func TestRunOnceCleanupDeleteError(t *testing.T) {
	closed, err := facts.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close() // Delete on this store will fail.

	c := Coordinator{
		Store:  oneRowStore{f: facts.Fact{ID: "drop", TenantID: "acme", UserID: "alice", Content: "x"}},
		Engram: engram.Store{Facts: closed},
		Runner: fakeRunner{summary: Summary{RowsIn: 1, DroppedIDs: []string{"drop"}}},
	}
	if _, err := c.RunOnce(context.Background()); err == nil {
		t.Fatal("expected cleanup delete error on closed store")
	}
}

// oneRowStore exports exactly one fact, letting us reach the cleanup phase.
type oneRowStore struct{ f facts.Fact }

func (o oneRowStore) ExportAll(context.Context) ([]facts.Fact, error) {
	return []facts.Fact{o.f}, nil
}

func TestPruneTurnsOnceNoOpWhenDisabled(t *testing.T) {
	sem := &fakeSemanticPruner{}
	// Nil semantic: no-op, no export.
	c := Coordinator{Store: errFactStore{err: errors.New("must not be called")}}
	if err := c.PruneTurnsOnce(context.Background()); err != nil {
		t.Fatalf("PruneTurnsOnce nil-semantic = %v, want nil", err)
	}
	// Semantic set but policy disabled: still no-op.
	c.Semantic = sem
	if err := c.PruneTurnsOnce(context.Background()); err != nil {
		t.Fatalf("PruneTurnsOnce disabled-policy = %v, want nil", err)
	}
	if len(sem.calls) != 0 {
		t.Fatalf("PruneTurns called %d times, want 0", len(sem.calls))
	}
}

func TestPruneTurnsOnceExportError(t *testing.T) {
	want := errors.New("export failed")
	sem := &fakeSemanticPruner{}
	c := Coordinator{Store: errFactStore{err: want}, Semantic: sem, TurnMaxCount: 10}
	if err := c.PruneTurnsOnce(context.Background()); err == nil || !errors.Is(err, want) {
		t.Fatalf("PruneTurnsOnce error = %v, want wrap of %v", err, want)
	}
}

func TestPruneTurnsOnceRuns(t *testing.T) {
	fs, err := facts.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	ctx := context.Background()
	sc, _ := facts.NewScope("acme", "alice")
	if err := fs.Save(ctx, sc, facts.Fact{ID: "f1", TenantID: "acme", UserID: "alice", Content: "x", Importance: 0.5}); err != nil {
		t.Fatal(err)
	}
	sem := &fakeSemanticPruner{}
	c := Coordinator{Store: fs, Semantic: sem, TurnMaxCount: 100}
	if err := c.PruneTurnsOnce(ctx); err != nil {
		t.Fatalf("PruneTurnsOnce: %v", err)
	}
	if len(sem.calls) != 1 || sem.calls[0].tenant != "acme" {
		t.Fatalf("PruneTurns calls = %+v, want one acme call", sem.calls)
	}
}

// TestPruneTurnsLogsPerScopeError verifies a single scope's PruneTurns failure
// is logged and does not abort the pass (best-effort). Two active scopes, both
// failing, must both be attempted.
func TestPruneTurnsLogsPerScopeError(t *testing.T) {
	fs, err := facts.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	ctx := context.Background()
	for _, id := range []struct{ tenant, user, fid string }{
		{"acme", "alice", "f1"},
		{"acme", "bob", "f2"},
	} {
		sc, _ := facts.NewScope(id.tenant, id.user)
		if err := fs.Save(ctx, sc, facts.Fact{ID: id.fid, TenantID: id.tenant, UserID: id.user, Content: "x", Importance: 0.5}); err != nil {
			t.Fatal(err)
		}
	}
	sem := &errSemanticPruner{err: errors.New("prune blew up")}
	c := Coordinator{Store: fs, Semantic: sem, TurnMaxAge: time.Hour}
	if err := c.PruneTurnsOnce(ctx); err != nil {
		t.Fatalf("PruneTurnsOnce should swallow per-scope errors, got %v", err)
	}
	if sem.callCount() != 2 {
		t.Fatalf("PruneTurns attempted %d scopes, want 2 (best-effort continues)", sem.callCount())
	}
}

func TestRunSchedulerTicksThenCancels(t *testing.T) {
	fs, err := facts.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	ctx := context.Background()
	sc, _ := facts.NewScope("acme", "alice")
	if err := fs.Save(ctx, sc, facts.Fact{ID: "f1", TenantID: "acme", UserID: "alice", Content: "x", Importance: 0.5}); err != nil {
		t.Fatal(err)
	}
	c := Coordinator{Store: fs, Engram: engram.Store{Facts: fs}, Runner: fakeRunner{summary: Summary{RowsIn: 1, RowsOut: 1}}}

	runCtx, cancel := context.WithCancel(ctx)
	results := make(chan struct {
		s   Summary
		err error
	}, 4)
	done := make(chan struct{})
	go func() {
		c.RunScheduler(runCtx, 5*time.Millisecond, func(s Summary, err error) {
			results <- struct {
				s   Summary
				err error
			}{s, err}
		})
		close(done)
	}()

	select {
	case r := <-results:
		if r.err != nil {
			t.Fatalf("scheduler pass err = %v", r.err)
		}
		if r.s.RowsIn != 1 {
			t.Fatalf("scheduler summary = %+v", r.s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler never fired a pass")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not stop after cancel")
	}
}

func TestRunTurnPruneSchedulerTicksAndReportsError(t *testing.T) {
	want := errors.New("export died")
	sem := &fakeSemanticPruner{}
	c := Coordinator{Store: errFactStore{err: want}, Semantic: sem, TurnMaxCount: 10}

	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 4)
	done := make(chan struct{})
	go func() {
		c.RunTurnPruneScheduler(ctx, 5*time.Millisecond, func(err error) { errs <- err })
		close(done)
	}()

	select {
	case err := <-errs:
		if !errors.Is(err, want) {
			t.Fatalf("onErr got %v, want wrap of %v", err, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("turn-prune scheduler never reported the export error")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("turn-prune scheduler did not stop after cancel")
	}
}

// TestSchedulersDefaultIntervalAndImmediateCancel exercises the <=0 interval
// default branch and the ctx-already-cancelled early return of both schedulers
// without waiting an hour.
func TestSchedulersDefaultIntervalAndImmediateCancel(t *testing.T) {
	c := Coordinator{Store: errFactStore{err: errors.New("unused")}, Runner: fakeRunner{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// interval <= 0 hits the "default to time.Hour" branch; the cancelled ctx
	// makes the select return immediately without ever ticking.
	c.RunScheduler(ctx, 0, nil)
	c.RunTurnPruneScheduler(ctx, -1, nil)
}

func TestWriteNDJSONCreateError(t *testing.T) {
	// A path under a non-existent directory cannot be created.
	err := WriteNDJSON("/nonexistent-dir-xyz/does/not/exist.ndjson", []Record{{ID: "a"}})
	if err == nil {
		t.Fatal("expected create error for unwritable path")
	}
}

func TestParseSummaryEdgeCases(t *testing.T) {
	if _, err := parseSummary("   \n  \n"); err == nil {
		t.Fatal("expected error for empty stdout")
	}
	if _, err := parseSummary("this is not json"); err == nil {
		t.Fatal("expected parse error for non-JSON summary line")
	}
}

// TestPruneTurnsSkipsMalformedIdentity drives PruneTurnsOnce with an exported
// fact carrying an empty user id, which semantic.ScopeFromContext rejects — the
// scope is skipped defensively and PruneTurns is never called for it.
func TestPruneTurnsSkipsMalformedIdentity(t *testing.T) {
	sem := &fakeSemanticPruner{}
	c := Coordinator{
		Store:        oneRowStore{f: facts.Fact{ID: "bad", TenantID: "acme", UserID: "has:reserved", Content: "x"}},
		Semantic:     sem,
		TurnMaxCount: 10,
	}
	if err := c.PruneTurnsOnce(context.Background()); err != nil {
		t.Fatalf("PruneTurnsOnce: %v", err)
	}
	if len(sem.calls) != 0 {
		t.Fatalf("PruneTurns called %d times for a malformed identity, want 0", len(sem.calls))
	}
}

func TestWorkerTracerHonorsInjected(t *testing.T) {
	injected := noop.NewTracerProvider().Tracer("test-injected")
	w := Worker{Tracer: injected}
	if got := w.tracer(); got != injected {
		t.Fatalf("tracer() returned %v, want injected tracer", got)
	}
	// Default (nil) path returns a non-nil no-op tracer.
	if (Worker{}).tracer() == nil {
		t.Fatal("nil-Tracer path returned nil tracer")
	}
}
