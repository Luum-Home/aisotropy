package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/bitrate"
	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// ============================================================================
// Pure helpers: checkScorerGate, tierLines, checkLeakage, curveVerdict,
// sustainedMetric, mean, leastSquaresSlope, roundAll, compact, envOr.
// ============================================================================

func TestCheckScorerGate(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		bin, skip := checkScorerGate(func(string) string { return "" }, func(string) error { return nil })
		if bin != "" {
			t.Errorf("bin = %q, want empty", bin)
		}
		if !strings.Contains(skip, "AISOTROPY_BITRATE_BIN unset") || !strings.Contains(skip, "docker compose") {
			t.Errorf("skip = %q, want unset message + docker hint", skip)
		}
	})

	t.Run("set but missing on disk", func(t *testing.T) {
		getenv := func(k string) string {
			if k == "AISOTROPY_BITRATE_BIN" {
				return "/no/such/scorer"
			}
			return ""
		}
		bin, skip := checkScorerGate(getenv, func(string) error { return errors.New("not found") })
		if bin != "" {
			t.Errorf("bin = %q, want empty", bin)
		}
		if !strings.Contains(skip, `scorer binary "/no/such/scorer" not found`) {
			t.Errorf("skip = %q, want not-found message", skip)
		}
	})

	t.Run("ok", func(t *testing.T) {
		getenv := func(k string) string {
			if k == "AISOTROPY_BITRATE_BIN" {
				return "/bin/scorer"
			}
			return ""
		}
		bin, skip := checkScorerGate(getenv, func(string) error { return nil })
		if bin != "/bin/scorer" || skip != "" {
			t.Errorf("bin=%q skip=%q, want bin=/bin/scorer skip=empty", bin, skip)
		}
	})
}

func TestTierLines(t *testing.T) {
	t.Run("neither set: fallback", func(t *testing.T) {
		lines, realTiers := tierLines(func(string) string { return "" })
		if realTiers {
			t.Error("realTiers = true, want false")
		}
		joined := strings.Join(lines, "\n")
		for _, want := range []string{"in-process SQLite", "in-process cosine", "IN-PROCESS FALLBACK", "docker compose up"} {
			if !strings.Contains(joined, want) {
				t.Errorf("lines missing %q; got:\n%s", want, joined)
			}
		}
	})

	t.Run("only rqlite set: still fallback mode", func(t *testing.T) {
		env := map[string]string{"RQLITE_URL": "http://localhost:4001"}
		lines, realTiers := tierLines(func(k string) string { return env[k] })
		if realTiers {
			t.Error("realTiers = true, want false (semantic still fallback)")
		}
		joined := strings.Join(lines, "\n")
		if !strings.Contains(joined, "REAL rqlite @ http://localhost:4001") {
			t.Errorf("lines missing REAL rqlite line; got:\n%s", joined)
		}
		if !strings.Contains(joined, "in-process cosine") {
			t.Errorf("lines missing semantic fallback line; got:\n%s", joined)
		}
	})

	t.Run("both set with custom port: production-like", func(t *testing.T) {
		env := map[string]string{
			"RQLITE_URL":  "http://localhost:4001",
			"QDRANT_HOST": "localhost",
			"QDRANT_PORT": "9999",
		}
		lines, realTiers := tierLines(func(k string) string { return env[k] })
		if !realTiers {
			t.Fatal("realTiers = false, want true")
		}
		joined := strings.Join(lines, "\n")
		if !strings.Contains(joined, "REAL Qdrant @ localhost:9999") {
			t.Errorf("lines missing custom-port Qdrant line; got:\n%s", joined)
		}
		if !strings.Contains(joined, "PRODUCTION-LIKE") {
			t.Errorf("lines missing production-like mode line; got:\n%s", joined)
		}
		if strings.Contains(joined, "docker compose up") {
			t.Errorf("production-like mode should not print the docker hint; got:\n%s", joined)
		}
	})

	t.Run("qdrant set without explicit port: defaults to 6334", func(t *testing.T) {
		env := map[string]string{"QDRANT_HOST": "localhost"}
		lines, _ := tierLines(func(k string) string { return env[k] })
		joined := strings.Join(lines, "\n")
		if !strings.Contains(joined, "REAL Qdrant @ localhost:6334") {
			t.Errorf("lines missing default-port Qdrant line; got:\n%s", joined)
		}
	})
}

func TestCheckLeakage(t *testing.T) {
	probe := []string{"What time is the sync?", "How many days off?"}
	if err := checkLeakage(probe, "This is unrelated memory content."); err != nil {
		t.Errorf("checkLeakage(non-matching) = %v, want nil", err)
	}
	if err := checkLeakage(probe, "  what time is the sync?  "); err == nil {
		t.Error("checkLeakage(verbatim, case/space-insensitive match) = nil, want a LEAKAGE error")
	} else if !strings.Contains(err.Error(), "LEAKAGE") {
		t.Errorf("error = %v, want to mention LEAKAGE", err)
	}
}

func TestCurveVerdict(t *testing.T) {
	t.Run("empty series", func(t *testing.T) {
		slope, totalDrop, ok, fails := curveVerdict(nil)
		if slope != 0 || totalDrop != 0 || ok || len(fails) != 0 {
			t.Errorf("curveVerdict(nil) = slope=%v totalDrop=%v ok=%v fails=%v", slope, totalDrop, ok, fails)
		}
	})

	t.Run("descending series passes cleanly", func(t *testing.T) {
		_, totalDrop, ok, fails := curveVerdict([]float64{5, 4, 3, 2, 1})
		if !ok {
			t.Errorf("ok = false, want true; fails=%v", fails)
		}
		if len(fails) != 0 {
			t.Errorf("fails = %v, want none", fails)
		}
		if totalDrop != 4 {
			t.Errorf("totalDrop = %v, want 4", totalDrop)
		}
	})

	t.Run("ascending series fails both checks", func(t *testing.T) {
		_, _, ok, fails := curveVerdict([]float64{1, 2, 3})
		if ok {
			t.Error("ok = true, want false")
		}
		if len(fails) != 2 {
			t.Fatalf("fails = %v, want 2 messages (no-downward-curve AND non-negative-slope)", fails)
		}
		if !strings.Contains(fails[0], "NO downward curve") {
			t.Errorf("fails[0] = %q, want NO-downward-curve message", fails[0])
		}
		if !strings.Contains(fails[1], "slope is not negative") {
			t.Errorf("fails[1] = %q, want non-negative-slope message", fails[1])
		}
	})
}

func TestSustainedMetric(t *testing.T) {
	t.Run("fewer than 3 points: not applicable", func(t *testing.T) {
		for _, s := range [][]float64{nil, {1}, {2, 1}} {
			if applicable, _, _, _, _ := sustainedMetric(s); applicable {
				t.Errorf("sustainedMetric(%v) applicable = true, want false", s)
			}
		}
	})

	t.Run("flat/ascending series: not applicable (totalDrop <= 0)", func(t *testing.T) {
		if applicable, _, _, _, _ := sustainedMetric([]float64{1, 1, 1}); applicable {
			t.Error("flat series should not be applicable")
		}
		if applicable, _, _, _, _ := sustainedMetric([]float64{1, 2, 3}); applicable {
			t.Error("ascending series should not be applicable")
		}
	})

	t.Run("descending with an interior rise: computes all fields", func(t *testing.T) {
		applicable, firstStepDrop, postAnchorDrop, postAnchorFraction, maxRise := sustainedMetric([]float64{10, 9, 11, 5})
		if !applicable {
			t.Fatal("expected applicable = true")
		}
		if firstStepDrop != 1 {
			t.Errorf("firstStepDrop = %v, want 1", firstStepDrop)
		}
		if postAnchorDrop != 4 {
			t.Errorf("postAnchorDrop = %v, want 4 (9-5)", postAnchorDrop)
		}
		wantFraction := 4.0 / 5.0
		if postAnchorFraction != wantFraction {
			t.Errorf("postAnchorFraction = %v, want %v", postAnchorFraction, wantFraction)
		}
		if maxRise != 2 {
			t.Errorf("maxRise = %v, want 2 (11-9)", maxRise)
		}
	})
}

func TestMean(t *testing.T) {
	if mean(nil) != 0 {
		t.Error("mean(nil) != 0")
	}
	if got := mean([]float64{1, 2, 3}); got != 2 {
		t.Errorf("mean([1,2,3]) = %v, want 2", got)
	}
}

func TestLeastSquaresSlope(t *testing.T) {
	if got := leastSquaresSlope(nil); got != 0 {
		t.Errorf("leastSquaresSlope(nil) = %v, want 0", got)
	}
	if got := leastSquaresSlope([]float64{5}); got != 0 {
		t.Errorf("leastSquaresSlope(single) = %v, want 0", got)
	}
	// y = 3 - x: perfect line, slope -1.
	if got := leastSquaresSlope([]float64{3, 2, 1, 0}); got != -1 {
		t.Errorf("leastSquaresSlope([3,2,1,0]) = %v, want -1", got)
	}
}

func TestRoundAll(t *testing.T) {
	got := roundAll([]float64{1.00005, 2.00004, 0})
	want := []float64{1.0001, 2.0, 0}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("roundAll()[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestCompact(t *testing.T) {
	if got := compact(`{"a":  1,   "b": 2}`); got != `{"a":1,"b":2}` {
		t.Errorf("compact(valid json) = %q", got)
	}
	if got := compact("  not json  "); got != "not json" {
		t.Errorf("compact(invalid json) = %q, want trimmed original", got)
	}
}

func TestEnvOr(t *testing.T) {
	env := map[string]string{"SET": "value"}
	getenv := func(k string) string { return env[k] }
	if got := envOr(getenv, "SET", "default"); got != "value" {
		t.Errorf("envOr(set) = %q, want value", got)
	}
	if got := envOr(getenv, "UNSET", "default"); got != "default" {
		t.Errorf("envOr(unset) = %q, want default", got)
	}
}

// ============================================================================
// runMain: flag parsing wiring.
// ============================================================================

func testDeps(stdout io.Writer) deps {
	d := defaultDeps()
	d.stdout = stdout
	d.getenv = func(string) string { return "" } // scorer unset -> clean SKIP by default
	return d
}

func TestRunMain_FlagParseError(t *testing.T) {
	var buf bytes.Buffer
	code := runMain([]string{"-addr"}, testDeps(&buf)) // -addr with no value
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}

func TestRunMain_DelegatesToRun_SkipPath(t *testing.T) {
	var buf bytes.Buffer
	code := runMain(nil, testDeps(&buf))
	if code != 0 {
		t.Fatalf("code = %d, want 0 (clean SKIP)", code)
	}
	if !strings.Contains(buf.String(), "SKIP:") {
		t.Fatalf("output = %q, want a SKIP line", buf.String())
	}
}

// ============================================================================
// run(): scorer gate + FAIL branches, using in-memory fakes for every real-
// world boundary (env, filesystem, memory tiers, scorer, HTTP proof).
// ============================================================================

// scoredDeps returns a deps wired for a full happy-path run: real in-process
// facts/semantic/bitrate stores (no Docker), and a fake bitrate.Runner that
// scores deterministically from which topic's fact/doc text is present in the
// hydrated context — so the aggregate bpt_with curve genuinely descends as
// real (non-faked) hydration retrieves more revealed topics, without needing
// a real scorer subprocess or model.
func scoredDeps(t *testing.T, stdout io.Writer) deps {
	t.Helper()
	env := map[string]string{"AISOTROPY_BITRATE_BIN": "/fake/scorer"}
	d := defaultDeps()
	d.stdout = stdout
	d.getenv = func(k string) string { return env[k] }
	d.statFn = func(string) error { return nil }
	dataDir := t.TempDir()
	d.mkdirTemp = func(string, string) (string, error) { return dataDir, nil }
	d.setenv = func(string, string) error { return nil }
	d.openFacts = func() (facts.Store, error) { return facts.OpenSQLite(":memory:") }
	d.openSemantic = func(ctx context.Context, dim int) (semantic.Store, error) {
		return semantic.NewInMemoryStore(dim), nil
	}
	d.newEmbedder = func() semantic.Embedder { return semantic.NewHashEmbedder(128) }
	d.openBitrateStore = func(dsn string) (bitrateStore, error) { return bitrate.OpenStore(":memory:") }
	d.newRunner = func(string) bitrate.Runner { return topicAwareScorer{} }
	return d
}

// topicAwareScorer implements bitrate.Runner entirely in memory: no
// subprocess, no real infra. It reads the coordinator's NDJSON request file
// and scores each row by counting how many of the package-level topicFacts/
// topicDocs sentences appear verbatim in the row's hydrated Context — a
// direct, deterministic proxy for "how much of this tenant's world has been
// revealed", so the "_with" mean strictly decreases as seedTopic reveals one
// more topic per step (exactly the mechanism the harness measures) while
// "_without" (bare persona, no facts/docs) stays constant.
type topicAwareScorer struct{ err error }

func (s topicAwareScorer) Score(_ context.Context, inputPath, _ string) ([]bitrate.ScoreResult, error) {
	if s.err != nil {
		return nil, s.err
	}
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return nil, err
	}
	var results []bitrate.ScoreResult
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var req bitrate.ScoreRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			return nil, fmt.Errorf("topicAwareScorer: parse request: %w", err)
		}
		hits := 0
		for i := range topicFacts {
			if strings.Contains(req.Context, topicFacts[i]) || strings.Contains(req.Context, topicDocs[i]) {
				hits++
			}
		}
		bpt := 10.0 - float64(hits)*1.5
		results = append(results, bitrate.ScoreResult{
			ID:           req.ID,
			BitsTotal:    bpt * 4,
			BitsPerToken: bpt,
			NTokens:      4,
			ModelID:      "fake-topic-scorer",
			Unit:         "bits/token",
		})
	}
	return results, nil
}

func TestRun_ScorerGateSkip(t *testing.T) {
	var buf bytes.Buffer
	d := testDeps(&buf)
	code := run(":0", false, d)
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(buf.String(), "AISOTROPY_BITRATE_BIN unset") {
		t.Fatalf("output = %q, want scorer-unset SKIP", buf.String())
	}
}

func TestRun_ScratchDirFailure(t *testing.T) {
	var buf bytes.Buffer
	d := scoredDeps(t, &buf)
	d.mkdirTemp = func(string, string) (string, error) { return "", errors.New("disk full") }
	code := run(":0", false, d)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "FAIL: scratch data dir") {
		t.Fatalf("output = %q, want scratch-dir FAIL", buf.String())
	}
}

func TestRun_SetenvFailure(t *testing.T) {
	var buf bytes.Buffer
	d := scoredDeps(t, &buf)
	d.setenv = func(string, string) error { return errors.New("env full") }
	code := run(":0", false, d)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "FAIL: set AISOTROPY_DATA_DIR") {
		t.Fatalf("output = %q, want setenv FAIL", buf.String())
	}
}

func TestRun_OpenFactsFailure(t *testing.T) {
	var buf bytes.Buffer
	d := scoredDeps(t, &buf)
	d.openFacts = func() (facts.Store, error) { return nil, errors.New("facts boom") }
	code := run(":0", false, d)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "FAIL: open facts store") {
		t.Fatalf("output = %q, want open-facts FAIL", buf.String())
	}
}

func TestRun_OpenSemanticFailure(t *testing.T) {
	var buf bytes.Buffer
	d := scoredDeps(t, &buf)
	d.openSemantic = func(context.Context, int) (semantic.Store, error) { return nil, errors.New("semantic boom") }
	code := run(":0", false, d)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "FAIL: open semantic store") {
		t.Fatalf("output = %q, want open-semantic FAIL", buf.String())
	}
}

func TestRun_OpenBitrateStoreFailure(t *testing.T) {
	var buf bytes.Buffer
	d := scoredDeps(t, &buf)
	d.openBitrateStore = func(string) (bitrateStore, error) { return nil, errors.New("bitrate store boom") }
	code := run(":0", false, d)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "FAIL: open bitrate store") {
		t.Fatalf("output = %q, want open-bitrate-store FAIL", buf.String())
	}
}

// failingFactsStore wraps a real facts.Store and fails Save, delegating every
// other method — a targeted fault injector, not a hand-rolled reimplementation
// of the interface.
type failingFactsStore struct {
	facts.Store
	saveErr error
}

func (f failingFactsStore) Save(ctx context.Context, sc facts.Scope, fact facts.Fact) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	return f.Store.Save(ctx, sc, fact)
}

func TestRun_SeedFactSaveFailure(t *testing.T) {
	var buf bytes.Buffer
	d := scoredDeps(t, &buf)
	d.openFacts = func() (facts.Store, error) {
		real, err := facts.OpenSQLite(":memory:")
		if err != nil {
			return nil, err
		}
		return failingFactsStore{Store: real, saveErr: errors.New("fact save boom")}, nil
	}
	code := run(":0", false, d)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "seed fact (topic 0)") {
		t.Fatalf("output = %q, want seed-fact FAIL", buf.String())
	}
}

// failingEmbedder wraps a real Embedder and fails Embed, delegating Dimension.
type failingEmbedder struct {
	semantic.Embedder
	embedErr error
}

func (e failingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if e.embedErr != nil {
		return nil, e.embedErr
	}
	return e.Embedder.Embed(ctx, texts)
}

func TestRun_SeedEmbedFailure(t *testing.T) {
	var buf bytes.Buffer
	d := scoredDeps(t, &buf)
	d.newEmbedder = func() semantic.Embedder {
		return failingEmbedder{Embedder: semantic.NewHashEmbedder(128), embedErr: errors.New("embed boom")}
	}
	code := run(":0", false, d)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "embed doc (topic 0)") {
		t.Fatalf("output = %q, want embed FAIL", buf.String())
	}
}

// failingSemanticStore wraps a real semantic.Store and fails Upsert.
type failingSemanticStore struct {
	semantic.Store
	upsertErr error
}

func (s failingSemanticStore) Upsert(ctx context.Context, sc semantic.Scope, points []semantic.Point) error {
	if s.upsertErr != nil {
		return s.upsertErr
	}
	return s.Store.Upsert(ctx, sc, points)
}

func TestRun_SeedUpsertFailure(t *testing.T) {
	var buf bytes.Buffer
	d := scoredDeps(t, &buf)
	d.openSemantic = func(ctx context.Context, dim int) (semantic.Store, error) {
		return failingSemanticStore{Store: semantic.NewInMemoryStore(dim), upsertErr: errors.New("upsert boom")}, nil
	}
	code := run(":0", false, d)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "seed semantic point (topic 0)") {
		t.Fatalf("output = %q, want upsert FAIL", buf.String())
	}
}

func TestRun_CoordinatorRunOnceFailure(t *testing.T) {
	var buf bytes.Buffer
	d := scoredDeps(t, &buf)
	d.newRunner = func(string) bitrate.Runner { return topicAwareScorer{err: errors.New("scorer boom")} }
	code := run(":0", false, d)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "FAIL: coordinator RunOnce (step 0)") {
		t.Fatalf("output = %q, want RunOnce FAIL", buf.String())
	}
}

// shortScorer answers fewer rows than requested, tripping the
// "expected N measurements" guard in run()'s per-step loop.
type shortScorer struct{}

func (shortScorer) Score(_ context.Context, inputPath, _ string) ([]bitrate.ScoreResult, error) {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		return nil, nil
	}
	var req bitrate.ScoreRequest
	if err := json.Unmarshal([]byte(lines[0]), &req); err != nil {
		return nil, err
	}
	// Only ever answer the first row: every sample after the first is
	// dropped by the coordinator (missing "_without" or "_with" pairing),
	// so RunOnce returns fewer than K measurements.
	return []bitrate.ScoreResult{{ID: req.ID, BitsPerToken: 5, NTokens: 1, ModelID: "short"}}, nil
}

func TestRun_ShortMeasurementCount(t *testing.T) {
	var buf bytes.Buffer
	d := scoredDeps(t, &buf)
	d.newRunner = func(string) bitrate.Runner { return shortScorer{} }
	code := run(":0", false, d)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "expected 5 measurements") {
		t.Fatalf("output = %q, want short-measurement-count FAIL", buf.String())
	}
}

// closedBitrateStore wraps a real *bitrate.Store already Close()d, so any
// subsequent call fails deterministically — used to force the "durable save"
// FAIL branch inside the per-step loop without a custom fake Store type.
func closedBitrateStore(t *testing.T) bitrateStore {
	t.Helper()
	st, err := bitrate.OpenStore(":memory:")
	if err != nil {
		t.Fatalf("open bitrate store: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close bitrate store: %v", err)
	}
	return st
}

func TestRun_DurableSaveFailure(t *testing.T) {
	var buf bytes.Buffer
	d := scoredDeps(t, &buf)
	d.openBitrateStore = func(string) (bitrateStore, error) { return closedBitrateStore(t), nil }
	code := run(":0", false, d)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "FAIL: durable save (step 0)") {
		t.Fatalf("output = %q, want durable-save FAIL", buf.String())
	}
}

// failingSeriesStore wraps a real bitrateStore and fails exactly one
// post-loop read method, delegating everything else (including every Save()
// during the measurement loop, so the FAIL surfaces only at the targeted
// read).
type failingSeriesStore struct {
	bitrateStore
	failSeries       bool
	failTrendSlope   bool
	failAllSummaries bool
}

func (f failingSeriesStore) Series(ctx context.Context, tenant, user string) ([]bitrate.Measurement, error) {
	if f.failSeries {
		return nil, errors.New("series boom")
	}
	return f.bitrateStore.Series(ctx, tenant, user)
}

func (f failingSeriesStore) TrendSlope(ctx context.Context, tenant, user string) (float64, error) {
	if f.failTrendSlope {
		return 0, errors.New("trend slope boom")
	}
	return f.bitrateStore.TrendSlope(ctx, tenant, user)
}

func (f failingSeriesStore) AllSummaries(ctx context.Context) ([]bitrate.TenantSummary, error) {
	if f.failAllSummaries {
		return nil, errors.New("all summaries boom")
	}
	return f.bitrateStore.AllSummaries(ctx)
}

func TestRun_SeriesReadBackFailure(t *testing.T) {
	var buf bytes.Buffer
	d := scoredDeps(t, &buf)
	d.openBitrateStore = func(dsn string) (bitrateStore, error) {
		real, err := bitrate.OpenStore(":memory:")
		if err != nil {
			return nil, err
		}
		return failingSeriesStore{bitrateStore: real, failSeries: true}, nil
	}
	code := run(":0", false, d)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "FAIL: read series back") {
		t.Fatalf("output = %q, want series-read-back FAIL", buf.String())
	}
}

func TestRun_TrendSlopeFailure(t *testing.T) {
	var buf bytes.Buffer
	d := scoredDeps(t, &buf)
	d.openBitrateStore = func(dsn string) (bitrateStore, error) {
		real, err := bitrate.OpenStore(":memory:")
		if err != nil {
			return nil, err
		}
		return failingSeriesStore{bitrateStore: real, failTrendSlope: true}, nil
	}
	code := run(":0", false, d)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "FAIL: store TrendSlope") {
		t.Fatalf("output = %q, want TrendSlope FAIL", buf.String())
	}
}

func TestRun_AllSummariesFailure(t *testing.T) {
	var buf bytes.Buffer
	d := scoredDeps(t, &buf)
	d.openBitrateStore = func(dsn string) (bitrateStore, error) {
		real, err := bitrate.OpenStore(":memory:")
		if err != nil {
			return nil, err
		}
		return failingSeriesStore{bitrateStore: real, failAllSummaries: true}, nil
	}
	code := run(":0", false, d)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "FAIL: store AllSummaries") {
		t.Fatalf("output = %q, want AllSummaries FAIL", buf.String())
	}
}

// ============================================================================
// run(): full happy-path E2E — real in-process hydration/coordinator/store,
// fake topic-aware scorer, httpProof both on and off.
// ============================================================================

func TestRun_FullSuccess_NoHTTPProof(t *testing.T) {
	var buf bytes.Buffer
	d := scoredDeps(t, &buf)
	code := run(":0", false, d)
	out := buf.String()
	if code != 0 {
		t.Fatalf("code = %d, want 0 (PASS); output:\n%s", code, out)
	}
	for _, want := range []string{
		"== Bet-6 longitudinal harness",
		"-- per-step measurement",
		"step 0", "step 4",
		"-- KR8.1 downward curve --",
		"least-squares slope over AGG bpt_with",
		"-- sustained-descent metric --",
		"-- durable store read-back",
		"AllSummaries:",
		"RESULT: PASS",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; full output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "HTTP proof") {
		t.Errorf("output should not attempt the HTTP proof when httpProof=false; got:\n%s", out)
	}
	if strings.Contains(out, "FAIL:") {
		t.Errorf("a successful run should print no FAIL lines; got:\n%s", out)
	}
}

func TestRun_FullSuccess_WithHTTPProof(t *testing.T) {
	var buf bytes.Buffer
	d := scoredDeps(t, &buf)
	d.newApp = func(addr string) appRunner { return fakeApp{healthy: true} }
	d.newHTTPClient = func() httpGetter { return fakeHTTPClient{tenant: tenant} }
	code := run(":0", true, d)
	out := buf.String()
	if code != 0 {
		t.Fatalf("code = %d, want 0; output:\n%s", code, out)
	}
	if !strings.Contains(out, "OK: /bitrate served") {
		t.Errorf("output missing HTTP-proof success line; got:\n%s", out)
	}
	if strings.Contains(out, "WARN: HTTP proof") {
		t.Errorf("a successful HTTP proof should not warn; got:\n%s", out)
	}
}

func TestRun_HTTPProofFailureDoesNotOverrideCurveVerdict(t *testing.T) {
	var buf bytes.Buffer
	d := scoredDeps(t, &buf)
	d.newApp = func(addr string) appRunner { return fakeApp{startErr: errors.New("boom")} }
	code := run(":0", true, d)
	out := buf.String()
	if code != 0 {
		t.Fatalf("code = %d, want 0: a failed HTTP proof must not flip a passing curve to FAIL; output:\n%s", code, out)
	}
	if !strings.Contains(out, "WARN: HTTP proof did not complete") {
		t.Errorf("output missing HTTP-proof WARN; got:\n%s", out)
	}
	if !strings.Contains(out, "RESULT: PASS") {
		t.Errorf("output missing PASS result despite the HTTP proof failure; got:\n%s", out)
	}
}

// flatScorer always returns the same bpt regardless of context, so the curve
// never descends — exercises run()'s "RESULT: FAIL" exit path.
type flatScorer struct{}

func (flatScorer) Score(_ context.Context, inputPath, _ string) ([]bitrate.ScoreResult, error) {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return nil, err
	}
	var results []bitrate.ScoreResult
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var req bitrate.ScoreRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			return nil, err
		}
		results = append(results, bitrate.ScoreResult{ID: req.ID, BitsPerToken: 5, NTokens: 1, ModelID: "flat"})
	}
	return results, nil
}

func TestRun_FlatCurveResultsInFail(t *testing.T) {
	var buf bytes.Buffer
	d := scoredDeps(t, &buf)
	d.newRunner = func(string) bitrate.Runner { return flatScorer{} }
	code := run(":0", false, d)
	out := buf.String()
	if code != 1 {
		t.Fatalf("code = %d, want 1 (a flat curve must FAIL); output:\n%s", code, out)
	}
	if !strings.Contains(out, "RESULT: FAIL") {
		t.Errorf("output missing FAIL result; got:\n%s", out)
	}
}

// ============================================================================
// serveAndProbe: HTTP-proof orchestration, entirely faked (no real listener).
// ============================================================================

// fakeApp is a controllable appRunner: it either blocks until ctx.Done() (a
// well-behaved server) or returns startErr immediately (a server that dies on
// boot). healthy is read by fakeHTTPClient indirectly via the shared block
// channel semantics below (not used directly here).
type fakeApp struct {
	healthy  bool
	startErr error
	block    chan struct{} // when set, Run blocks on this until closed
}

func (f fakeApp) Run(ctx context.Context) error {
	if f.startErr != nil {
		return f.startErr
	}
	if f.block != nil {
		<-f.block
		return nil
	}
	<-ctx.Done()
	return nil
}

// fakeHTTPClient answers GET requests entirely in memory: healthz/health
// always 200; /bitrate is configurable per test (status, body, or an error).
type fakeHTTPClient struct {
	tenant          string
	failHealth      bool
	bitrateStatus   int
	bitrateBody     string
	bitrateErr      error
	healthzAlwaysOK bool // when false (zero value) still answers OK — see Get()
	neverLive       bool
}

func (c fakeHTTPClient) Get(url string) (*http.Response, error) {
	switch {
	case strings.HasSuffix(url, "/healthz"):
		if c.neverLive {
			return nil, errors.New("connection refused")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"status":"ok"}`))}, nil
	case strings.HasSuffix(url, "/health"):
		if c.failHealth {
			return nil, errors.New("health unreachable")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"status":"ok"}`))}, nil
	case strings.HasSuffix(url, "/bitrate"):
		if c.bitrateErr != nil {
			return nil, c.bitrateErr
		}
		status := c.bitrateStatus
		if status == 0 {
			status = http.StatusOK
		}
		body := c.bitrateBody
		if body == "" {
			body = fmt.Sprintf(`{"tenants":[{"tenant_id":%q}]}`, c.tenant)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	default:
		return nil, fmt.Errorf("unexpected URL %q", url)
	}
}

// fastServeDeps returns a deps with short-but-real ready/poll/shutdown
// durations, so the "never becomes live" and "shutdown timeout" paths are
// exercised deterministically without waiting the production 20s/10s
// defaults. Real time.Now/time.Sleep (not a fake clock) so the polling loop
// genuinely yields to the goroutine driving app.Run — a fake clock that never
// actually sleeps can race ahead of that goroutine's channel send.
func fastServeDeps(stdout io.Writer) deps {
	d := defaultDeps()
	d.stdout = stdout
	d.readyTimeout = 100 * time.Millisecond
	d.pollInterval = 5 * time.Millisecond
	d.shutdownTimeout = 20 * time.Millisecond
	return d
}

func TestServeAndProbe_HappyPath(t *testing.T) {
	var buf bytes.Buffer
	d := fastServeDeps(&buf)
	d.newApp = func(addr string) appRunner { return fakeApp{healthy: true} }
	d.newHTTPClient = func() httpGetter { return fakeHTTPClient{tenant: tenant} }
	if err := serveAndProbe(d, ":0", tenant, false); err != nil {
		t.Fatalf("serveAndProbe: %v; output:\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "OK: /bitrate served") {
		t.Errorf("output missing success line; got:\n%s", buf.String())
	}
}

func TestServeAndProbe_ServerExitsBeforeReady(t *testing.T) {
	var buf bytes.Buffer
	d := fastServeDeps(&buf)
	d.newApp = func(addr string) appRunner { return fakeApp{startErr: errors.New("boot failure")} }
	d.newHTTPClient = func() httpGetter { return fakeHTTPClient{neverLive: true} }
	err := serveAndProbe(d, ":0", tenant, false)
	if err == nil || !strings.Contains(err.Error(), "server exited before ready") {
		t.Fatalf("err = %v, want 'server exited before ready'", err)
	}
}

func TestServeAndProbe_NeverBecomesLive(t *testing.T) {
	var buf bytes.Buffer
	d := fastServeDeps(&buf)
	block := make(chan struct{})
	defer close(block)
	d.newApp = func(addr string) appRunner { return fakeApp{block: block} }
	d.newHTTPClient = func() httpGetter { return fakeHTTPClient{neverLive: true} }
	err := serveAndProbe(d, ":0", tenant, false)
	if err == nil || !strings.Contains(err.Error(), "did not become live") {
		t.Fatalf("err = %v, want 'did not become live'", err)
	}
}

func TestServeAndProbe_HealthError(t *testing.T) {
	var buf bytes.Buffer
	d := fastServeDeps(&buf)
	block := make(chan struct{})
	defer close(block)
	d.newApp = func(addr string) appRunner { return fakeApp{block: block} }
	d.newHTTPClient = func() httpGetter { return fakeHTTPClient{tenant: tenant, failHealth: true} }
	err := serveAndProbe(d, ":0", tenant, false)
	if err == nil || !strings.Contains(err.Error(), "GET /health") {
		t.Fatalf("err = %v, want a GET /health error", err)
	}
}

func TestServeAndProbe_BitrateError(t *testing.T) {
	var buf bytes.Buffer
	d := fastServeDeps(&buf)
	block := make(chan struct{})
	defer close(block)
	d.newApp = func(addr string) appRunner { return fakeApp{block: block} }
	d.newHTTPClient = func() httpGetter { return fakeHTTPClient{tenant: tenant, bitrateErr: errors.New("unreachable")} }
	err := serveAndProbe(d, ":0", tenant, false)
	if err == nil || !strings.Contains(err.Error(), "GET /bitrate") {
		t.Fatalf("err = %v, want a GET /bitrate error", err)
	}
}

func TestServeAndProbe_BitrateNon200(t *testing.T) {
	var buf bytes.Buffer
	d := fastServeDeps(&buf)
	block := make(chan struct{})
	defer close(block)
	d.newApp = func(addr string) appRunner { return fakeApp{block: block} }
	d.newHTTPClient = func() httpGetter {
		return fakeHTTPClient{tenant: tenant, bitrateStatus: http.StatusInternalServerError, bitrateBody: `{"error":"boom"}`}
	}
	err := serveAndProbe(d, ":0", tenant, false)
	if err == nil || !strings.Contains(err.Error(), "/bitrate returned 500") {
		t.Fatalf("err = %v, want '/bitrate returned 500'", err)
	}
}

func TestServeAndProbe_BitrateMissingTenant(t *testing.T) {
	var buf bytes.Buffer
	d := fastServeDeps(&buf)
	block := make(chan struct{})
	defer close(block)
	d.newApp = func(addr string) appRunner { return fakeApp{block: block} }
	d.newHTTPClient = func() httpGetter {
		return fakeHTTPClient{tenant: tenant, bitrateBody: `{"tenants":[]}`}
	}
	err := serveAndProbe(d, ":0", tenant, false)
	if err == nil || !strings.Contains(err.Error(), "did not include tenant") {
		t.Fatalf("err = %v, want 'did not include tenant'", err)
	}
}

func TestServeAndProbe_ShutdownTimeoutWarns(t *testing.T) {
	var buf bytes.Buffer
	d := fastServeDeps(&buf)
	block := make(chan struct{}) // Run never returns even after cancel(); unblocked at test end.
	defer close(block)
	d.newApp = func(addr string) appRunner { return fakeApp{block: block} }
	d.newHTTPClient = func() httpGetter { return fakeHTTPClient{tenant: tenant} }
	if err := serveAndProbe(d, ":0", tenant, true); err != nil {
		t.Fatalf("serveAndProbe: %v", err)
	}
	if !strings.Contains(buf.String(), "WARN: aisotropyd shutdown did not complete") {
		t.Errorf("output missing shutdown-timeout WARN; got:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "REAL rqlite + Qdrant") {
		t.Errorf("output missing realTiers=true label; got:\n%s", buf.String())
	}
}

// ============================================================================
// resetScope / captureStore / getBody: small direct-call units.
// ============================================================================

func TestResetScope_BestEffortNoPanic(t *testing.T) {
	ctx := context.Background()
	fs, err := facts.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close() }()
	fsc, err := facts.NewScope(tenant, user)
	if err != nil {
		t.Fatal(err)
	}
	ss := semantic.NewInMemoryStore(128)
	ssc, err := semantic.NewScope(tenant, user)
	if err != nil {
		t.Fatal(err)
	}

	// Seed one fact/point exactly as seedTopic would, then reset and verify
	// removal.
	if err := fs.Save(ctx, fsc, facts.Fact{ID: "harness-fact-topic-0", TenantID: tenant, UserID: user, Content: "x"}); err != nil {
		t.Fatal(err)
	}
	embedder := semantic.NewHashEmbedder(128)
	vecs, err := embedder.Embed(ctx, []string{"doc"})
	if err != nil {
		t.Fatal(err)
	}
	src := "harness-doc-topic-0"
	pt := semantic.Point{
		ID: semantic.PointID(ssc, semantic.KindDocument, src), TenantID: tenant, UserID: user,
		Kind: semantic.KindDocument, Content: "doc", Source: src, Vector: vecs[0],
	}
	if err := ss.Upsert(ctx, ssc, []semantic.Point{pt}); err != nil {
		t.Fatal(err)
	}

	resetScope(ctx, fs, fsc, ss, ssc, 1)

	facts, err := fs.Search(ctx, fsc, "x", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 0 {
		t.Errorf("fact still present after resetScope: %v", facts)
	}

	// Also prove the best-effort call is a no-op (no panic) on an already-empty
	// scope — the "previous run left nothing" case.
	resetScope(ctx, fs, fsc, ss, ssc, 1)
}

func TestCaptureStore_Save(t *testing.T) {
	cs := &captureStore{}
	m := bitrate.Measurement{TenantID: tenant, UserID: user, BptWith: 1.5}
	if err := cs.Save(context.Background(), m); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if cs.last != m {
		t.Errorf("last = %+v, want %+v", cs.last, m)
	}
}

func TestGetBody(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		c := fakeHTTPClient{tenant: tenant}
		body, code, err := getBody(c, "http://x/health")
		if err != nil || code != http.StatusOK || !strings.Contains(body, "ok") {
			t.Fatalf("getBody = %q, %d, %v", body, code, err)
		}
	})

	t.Run("get error", func(t *testing.T) {
		c := fakeHTTPClient{bitrateErr: errors.New("boom")}
		_, _, err := getBody(c, "http://x/bitrate")
		if err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("body read error", func(t *testing.T) {
		c := errorBodyClient{}
		_, _, err := getBody(c, "http://x/anything")
		if err == nil {
			t.Fatal("expected a body-read error")
		}
	})
}

// errorBodyClient returns a 200 response whose Body errors on Read, covering
// getBody's io.ReadAll error branch.
type errorBodyClient struct{}

func (errorBodyClient) Get(string) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(errReader{})}, nil
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read boom") }

// ============================================================================
// defaultDeps: the real (non-test) closures. None of these touch real network
// infra: with RQLITE_URL/QDRANT_HOST unset they resolve to the same in-process
// fallbacks the rest of the suite uses; aisotropy.New/http.Client construction
// have no side effects until Run()/Do() is called.
// ============================================================================

func TestDefaultDeps_ConstructsRealImplementations(t *testing.T) {
	t.Setenv("RQLITE_URL", "")
	t.Setenv("QDRANT_HOST", "")
	t.Setenv("AISOTROPY_FACTS_DB", "")
	t.Setenv("AISOTROPY_DATA_DIR", "")
	d := defaultDeps()

	tmp := t.TempDir() + "/present"
	if err := os.WriteFile(tmp, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.statFn(tmp); err != nil {
		t.Errorf("statFn(present) = %v, want nil", err)
	}
	if err := d.statFn(tmp + "-missing"); err == nil {
		t.Error("statFn(missing) = nil, want an error")
	}

	fs, err := d.openFacts()
	if err != nil {
		t.Fatalf("openFacts: %v", err)
	}
	defer func() { _ = fs.Close() }()

	ss, err := d.openSemantic(context.Background(), 128)
	if err != nil {
		t.Fatalf("openSemantic: %v", err)
	}
	defer func() { _ = ss.Close() }()

	emb := d.newEmbedder()
	if emb == nil || emb.Dimension() <= 0 {
		t.Errorf("newEmbedder() = %+v, want a usable embedder", emb)
	}

	bs, err := d.openBitrateStore(":memory:")
	if err != nil {
		t.Fatalf("openBitrateStore: %v", err)
	}
	defer func() { _ = bs.Close() }()

	if runner := d.newRunner("/some/bin"); runner == nil {
		t.Error("newRunner() = nil")
	}
	if app := d.newApp(":0"); app == nil {
		t.Error("newApp() = nil")
	}
	if client := d.newHTTPClient(); client == nil {
		t.Error("newHTTPClient() = nil")
	}
}

// ============================================================================
// seedTopic's leakage guard, exercised end-to-end through run(): a temporary,
// restored mutation of the package-level topic data (topicFacts/topicDocs are
// vars precisely so tests can do this) makes topic 0's memory equal its own
// probe verbatim, tripping checkLeakage from inside seedTopic.
// ============================================================================

func TestRun_SeedTopicLeakageOnFact(t *testing.T) {
	orig := topicFacts[0]
	topicFacts[0] = topicProbe[0]
	defer func() { topicFacts[0] = orig }()

	var buf bytes.Buffer
	d := scoredDeps(t, &buf)
	code := run(":0", false, d)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "FAIL: LEAKAGE") {
		t.Fatalf("output = %q, want a LEAKAGE FAIL", buf.String())
	}
}

func TestRun_SeedTopicLeakageOnDoc(t *testing.T) {
	orig := topicDocs[0]
	topicDocs[0] = topicProbe[0]
	defer func() { topicDocs[0] = orig }()

	var buf bytes.Buffer
	d := scoredDeps(t, &buf)
	code := run(":0", false, d)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "FAIL: LEAKAGE") {
		t.Fatalf("output = %q, want a LEAKAGE FAIL", buf.String())
	}
}

// ============================================================================
// The natural end-of-run bStore.Close() WARN branch: a store whose Close()
// fails even though every prior read succeeded.
// ============================================================================

type failCloseStore struct {
	bitrateStore
}

func (f failCloseStore) Close() error {
	_ = f.bitrateStore.Close()
	return errors.New("close boom")
}

func TestRun_FinalCloseWarning(t *testing.T) {
	var buf bytes.Buffer
	d := scoredDeps(t, &buf)
	d.openBitrateStore = func(dsn string) (bitrateStore, error) {
		real, err := bitrate.OpenStore(":memory:")
		if err != nil {
			return nil, err
		}
		return failCloseStore{bitrateStore: real}, nil
	}
	code := run(":0", false, d)
	if code != 0 {
		t.Fatalf("code = %d, want 0 (a close warning must not fail the run); output:\n%s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "WARN: bitrate store close: close boom") {
		t.Fatalf("output = %q, want the close WARN", buf.String())
	}
}
