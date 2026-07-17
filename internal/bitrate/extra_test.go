package bitrate

// Error-path and edge-branch coverage for the bitrate pipeline: coordinator
// failure handling, the scheduler loop, store scope guards and the on-disk open
// path, and the NDJSON read/write error branches. All model-free — a fake Runner
// stands in for the scorer subprocess, exactly as the design intends.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace/noop"
)

// errSampler fails the held-out read.
type errSampler struct{}

func (errSampler) HeldOut(context.Context, int) ([]Sample, error) {
	return nil, errors.New("sampler down")
}

// errRunner fails the score subprocess.
type errRunner struct{}

func (errRunner) Score(context.Context, string, string) ([]ScoreResult, error) {
	return nil, errors.New("scorer exploded")
}

// withoutErrHydrator succeeds on the memory-rich context but fails the bare
// baseline — exercising the SECOND hydration branch in buildScoreRequests.
type withoutErrHydrator struct{}

func (withoutErrHydrator) ContextWith(context.Context, string, string, string, string, string) (string, error) {
	return "WITH", nil
}
func (withoutErrHydrator) ContextWithout(context.Context, string) (string, error) {
	return "", errors.New("no baseline persona")
}

// dropWithoutRunner scores the "_with" rows but omits every "_without" row, so
// each sample is missing half its pair — the saveMeasurements skip branch.
type dropWithoutRunner struct{}

func (dropWithoutRunner) Score(_ context.Context, inputPath, _ string) ([]ScoreResult, error) {
	reqs, err := readRequests(inputPath)
	if err != nil {
		return nil, err
	}
	var out []ScoreResult
	for _, req := range reqs {
		if strings.HasSuffix(req.ID, "_without") {
			continue // drop the baseline row
		}
		out = append(out, ScoreResult{ID: req.ID, BitsPerToken: 1, NTokens: 4, ModelID: "m@pinned"})
	}
	return out, nil
}

// errMeasurementStore fails on persist.
type errMeasurementStore struct{}

func (errMeasurementStore) Save(context.Context, Measurement) error {
	return errors.New("disk full")
}

func oneSample() []Sample {
	return []Sample{{TenantID: "acme", UserID: "alice", SessionID: "s1", SampleID: "r1", Text: "hola mundo"}}
}

func TestRunOnceSamplerError(t *testing.T) {
	c := Coordinator{Sampler: errSampler{}, Hydrator: fakeHydrator{}, Runner: fakeRunner{withBPT: 1}, Store: openTestStore(t)}
	if _, err := c.RunOnce(context.Background()); err == nil {
		t.Fatal("expected RunOnce to propagate the sampler error")
	}
}

func TestRunOnceRunnerError(t *testing.T) {
	c := Coordinator{
		Sampler:  fakeSampler{samples: oneSample()},
		Hydrator: fakeHydrator{},
		Runner:   errRunner{},
		Store:    openTestStore(t),
		Dir:      t.TempDir(),
	}
	if _, err := c.RunOnce(context.Background()); err == nil {
		t.Fatal("expected RunOnce to propagate the runner error")
	}
}

func TestRunOnceScratchDirError(t *testing.T) {
	// Point Dir at a regular FILE so os.MkdirTemp(base, …) fails.
	badBase := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(badBase, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Coordinator{
		Sampler:  fakeSampler{samples: oneSample()},
		Hydrator: fakeHydrator{},
		Runner:   fakeRunner{withBPT: 1, withoutBPT: map[string]float64{"r1": 3}},
		Store:    openTestStore(t),
		Dir:      badBase,
	}
	if _, err := c.RunOnce(context.Background()); err == nil {
		t.Fatal("expected RunOnce to fail when the scratch dir cannot be created")
	}
}

func TestRunOnceBaselineHydrationErrorDropsSample(t *testing.T) {
	// ContextWith succeeds but ContextWithout fails → the sample is dropped, the
	// batch has no usable requests, and RunOnce is a clean no-op (no error).
	c := Coordinator{
		Sampler:  fakeSampler{samples: oneSample()},
		Hydrator: withoutErrHydrator{},
		Runner:   fakeRunner{withBPT: 1},
		Store:    openTestStore(t),
		Dir:      t.TempDir(),
		Tracer:   noop.NewTracerProvider().Tracer("test"), // also covers tracer() non-nil branch
	}
	ms, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("baseline hydration failure must not fail the batch: %v", err)
	}
	if len(ms) != 0 {
		t.Fatalf("expected no measurements, got %d", len(ms))
	}
}

func TestRunOnceSkipsSampleMissingAPairedRow(t *testing.T) {
	c := Coordinator{
		Sampler:  fakeSampler{samples: oneSample()},
		Hydrator: fakeHydrator{},
		Runner:   dropWithoutRunner{},
		Store:    openTestStore(t),
		Dir:      t.TempDir(),
	}
	ms, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(ms) != 0 {
		t.Fatalf("a sample missing its baseline row must be skipped, got %d measurements", len(ms))
	}
}

func TestRunOnceSaveError(t *testing.T) {
	c := Coordinator{
		Sampler:  fakeSampler{samples: oneSample()},
		Hydrator: fakeHydrator{},
		Runner:   fakeRunner{withBPT: 1, withoutBPT: map[string]float64{"r1": 3}},
		Store:    errMeasurementStore{},
		Dir:      t.TempDir(),
	}
	if _, err := c.RunOnce(context.Background()); err == nil {
		t.Fatal("expected RunOnce to propagate the store Save error")
	}
}

func TestRunSchedulerTicksThenStops(t *testing.T) {
	got := make(chan int, 4)
	c := Coordinator{
		Sampler:  fakeSampler{samples: oneSample()},
		Hydrator: fakeHydrator{},
		Runner:   fakeRunner{withBPT: 1, withoutBPT: map[string]float64{"r1": 3}},
		Store:    noopMeasurementStore{},
		Dir:      t.TempDir(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.RunScheduler(ctx, time.Millisecond, func(ms []Measurement, err error) {
			if err == nil {
				got <- len(ms)
			}
		})
		close(done)
	}()
	// Wait for at least one pass, then stop the loop.
	select {
	case n := <-got:
		if n != 1 {
			t.Fatalf("scheduler pass produced %d measurements, want 1", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler never produced a result")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not stop on context cancellation")
	}
}

func TestRunSchedulerDefaultsIntervalAndStops(t *testing.T) {
	// interval <= 0 defaults to one hour; a pre-cancelled ctx returns immediately
	// via the ctx.Done() branch without ever ticking.
	c := Coordinator{Sampler: fakeSampler{samples: nil}, Hydrator: fakeHydrator{}, Runner: fakeRunner{}, Store: noopMeasurementStore{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { c.RunScheduler(ctx, 0, nil); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler with default interval did not honor a cancelled context")
	}
}

// ---- store branches --------------------------------------------------------

func TestStoreScopeGuardsRejectEmptyIdentity(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, _, err := s.Latest(ctx, "", ""); err == nil {
		t.Fatal("Latest with empty scope must be rejected")
	}
	if _, err := s.Series(ctx, "", ""); err == nil {
		t.Fatal("Series with empty scope must be rejected")
	}
	if _, err := s.TrendSlope(ctx, "", ""); err == nil {
		t.Fatal("TrendSlope with empty scope must be rejected")
	}
	if err := s.Reserve(ctx, "", "alice", "sess", "r", "t"); err == nil {
		t.Fatal("Reserve with empty tenant must be rejected")
	}
}

func TestHeldOutNonPositiveLimit(t *testing.T) {
	s := openTestStore(t)
	got, err := s.HeldOut(context.Background(), 0)
	if err != nil {
		t.Fatalf("HeldOut(0): %v", err)
	}
	if got != nil {
		t.Fatalf("HeldOut(0) should return nil, got %v", got)
	}
}

func TestOpenStoreOnDiskWALAndReuse(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "bitrate.db")
	s, err := OpenStore(dsn)
	if err != nil {
		t.Fatalf("OpenStore on-disk: %v", err)
	}
	// The on-disk path enables WAL and applies the schema; a Save proves it works.
	ctx := context.Background()
	if err := s.Save(ctx, Measurement{TenantID: "acme", UserID: "alice", SampleID: "r1", Lift: 1}); err != nil {
		t.Fatalf("Save on-disk: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestOpenStoreInMemoryWithQueryString(t *testing.T) {
	// A DSN that already carries a "?" must not have busy_timeout re-appended, and
	// a shared in-memory DSN still resolves to the single-connection in-memory path.
	s, err := OpenStore("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("OpenStore shared in-memory: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Save(context.Background(), Measurement{TenantID: "t", UserID: "u", SampleID: "s"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func TestOpenStoreWALErrorOnUnwritablePath(t *testing.T) {
	// A non-in-memory DSN under a directory that does not exist fails when the
	// WAL pragma tries to create the database file.
	_, err := OpenStore(filepath.Join("does", "not", "exist", "bitrate.db"))
	if err == nil {
		t.Fatal("expected OpenStore to fail for an unwritable on-disk path")
	}
}

func TestStoreOperationsFailOnClosedDB(t *testing.T) {
	// Every scoped query/exec runs against the underlying *sql.DB; once the store
	// is closed, the identity scope still validates (it is DB-free) but the
	// statement itself fails — exercising each method's DB-error branch.
	s, err := OpenStore(":memory:")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ctx := context.Background()
	if err := s.Reserve(ctx, "acme", "alice", "sess", "r1", "text"); err == nil {
		t.Error("Reserve on a closed store should fail")
	}
	if err := s.Save(ctx, Measurement{TenantID: "acme", UserID: "alice", SampleID: "r1"}); err == nil {
		t.Error("Save on a closed store should fail")
	}
	if _, err := s.HeldOut(ctx, 10); err == nil {
		t.Error("HeldOut on a closed store should fail")
	}
	if _, _, err := s.Latest(ctx, "acme", "alice"); err == nil {
		t.Error("Latest on a closed store should fail")
	}
	if _, err := s.Series(ctx, "acme", "alice"); err == nil {
		t.Error("Series on a closed store should fail")
	}
	if _, err := s.AllSummaries(ctx); err == nil {
		t.Error("AllSummaries on a closed store should fail")
	}
}

// ---- record / worker parsing branches --------------------------------------

func TestWriteNDJSONCreateError(t *testing.T) {
	err := WriteNDJSON(filepath.Join("no", "such", "dir", "out.ndjson"), []ScoreRequest{{ID: "x"}})
	if err == nil {
		t.Fatal("expected WriteNDJSON to fail creating the file")
	}
}

func TestReadResultsOpenError(t *testing.T) {
	if _, err := ReadResults(filepath.Join(t.TempDir(), "missing.ndjson")); err == nil {
		t.Fatal("expected ReadResults to fail opening a missing file")
	}
}

func TestReadResultsMalformedLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.ndjson")
	if err := os.WriteFile(p, []byte(`{"id":"ok","bits_per_token":1.0}`+"\n"+`{not json}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadResults(p); err == nil {
		t.Fatal("expected ReadResults to fail on a malformed NDJSON line")
	}
}

func TestParseSummaryDegradesGracefully(t *testing.T) {
	if s := parseSummary(""); s != (summary{}) {
		t.Fatalf("empty stdout should yield a zero summary, got %+v", s)
	}
	if s := parseSummary("   \n  \n"); s != (summary{}) {
		t.Fatalf("whitespace-only stdout should yield a zero summary, got %+v", s)
	}
	if s := parseSummary("garbage not json"); s != (summary{}) {
		t.Fatalf("unparseable summary should yield a zero summary, got %+v", s)
	}
	// The LAST non-empty line is the summary, even after leading log noise.
	s := parseSummary("loading model...\nwarming up\n" + `{"scored":3,"model_id":"m@pinned","backend":"LM_REAL"}`)
	if s.Scored != 3 || s.ModelID != "m@pinned" || s.Backend != "LM_REAL" {
		t.Fatalf("summary not parsed from last line: %+v", s)
	}
}

func TestWorkerRunOnceReadResultsError(t *testing.T) {
	// The scorer exits 0 but writes a malformed results file → runOnce fails at
	// ReadResults, and Score exhausts its attempts.
	stub := writeStub(t, "printf '{bad json}\\n' > \"$out\"\necho '{\"scored\":0}'\n")
	w := Worker{BinaryPath: stub, MaxAttempts: 2, InitialBackoff: time.Millisecond, Tracer: noop.NewTracerProvider().Tracer("test")}
	if _, err := w.Score(context.Background(), "in", filepath.Join(t.TempDir(), "o")); err == nil {
		t.Fatal("expected Score to fail when the scorer writes malformed results")
	}
}
