// Command bitrate-harness is the Bet-6 (KR8.1) LONGITUDINAL INSTRUMENT: it drives
// the full bits-per-token-per-tenant pipeline against a PRODUCTION-LIKE deployment
// and captures a downward bpt curve over an evolving multi-session tenant, then
// observes that curve back over the real HTTP surface (GET /health, GET /bitrate).
//
// WHAT IS DIFFERENT FROM THE IN-PROCESS SIMS (internal/api/*_manual_test.go):
// those tests exercise the coordinator->scorer->store path against the IN-PROCESS
// memory tiers (SQLite :memory: facts, in-memory cosine semantic). This harness
// runs the SAME pipeline against the REAL NETWORKED tiers the reference
// docker-compose stack provides — rqlite (facts, FTS5) + Qdrant (semantic, HNSW) —
// when they are reachable (RQLITE_URL / QDRANT_HOST set), and boots the REAL
// aisotropyd (the aisotropy.App builder) so the trend is read back through the
// same GET /bitrate handler an operator would hit. It is the last engineering
// step before genuine field data.
//
// HONESTY — THIS IS STILL A SIMULATED EVOLVING TENANT, NOT REAL USERS OVER WEEKS.
// The accumulation schedule (one diverse topic revealed per step) is authored, so
// the descent's shape reflects that schedule, exactly as the manual sims disclose.
// What this harness ADDS over the sims is the PRODUCTION SUBSTRATE: real rqlite +
// Qdrant + the real Python scorer subprocess + the real /bitrate HTTP view. It
// validates the INSTRUMENT end to end; KR8.1's real-field half stays open (the
// curve here is not drawn from real traffic).
//
// TIER SELECTION (mirrors the runtime's own env precedence):
//   - RQLITE_URL set   -> facts tier is the networked rqlite (else SQLite file)
//   - QDRANT_HOST set   -> semantic tier is the networked Qdrant (else in-memory)
//
// With neither set the harness runs on the IN-PROCESS fallbacks and says so, and
// prints the docker-compose invocation for the real run.
//
// The scorer is a subprocess named by AISOTROPY_BITRATE_BIN (a wrapper that execs
// spikes/compression/.venv/bin/python spikes/compression/bitrate_scorer.py). If it
// is unset or missing the harness SKIPS cleanly (exit 0) — it never fabricates a
// curve without a real scorer.
//
// RUN (real tiers, the intended production-like path):
//
//	docker compose up -d rqlite qdrant           # bring up the real memory tiers
//	cat > /tmp/bitrate-wrapper.sh <<'EOF'
//	#!/bin/bash
//	exec /ABS/spikes/compression/.venv/bin/python /ABS/spikes/compression/bitrate_scorer.py "$@"
//	EOF
//	chmod +x /tmp/bitrate-wrapper.sh
//	AISOTROPY_BITRATE_BIN=/tmp/bitrate-wrapper.sh \
//	  RQLITE_URL=http://localhost:4001 QDRANT_HOST=localhost QDRANT_PORT=6334 \
//	  go run ./cmd/bitrate-harness
//
// RUN (in-process fallback, no Docker): drop the RQLITE_URL/QDRANT_HOST vars.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Luum-Home/aisotropy/aisotropy"
	"github.com/Luum-Home/aisotropy/internal/api"
	"github.com/Luum-Home/aisotropy/internal/bitrate"
	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

const (
	tenant  = "acme"
	user    = "alice"
	session = "sess-harness-longitudinal"
)

// diverseProbeSampler returns the WHOLE fixed diverse probe set (K held-out turns
// spanning K topics) in one HeldOut call, so a single coordinator RunOnce scores
// every probe turn against the step's memory. This is the diverse-probe design
// (internal/api/sustained_trend_diverse_probe_manual_test.go) that produced a
// SUSTAINED descent rather than a one-shot anchor: the per-step aggregate is a
// MEAN over K turns, and exactly one more turn is "covered" per step.
type diverseProbeSampler struct{ samples []bitrate.Sample }

func (d diverseProbeSampler) HeldOut(_ context.Context, _ int) ([]bitrate.Sample, error) {
	return d.samples, nil
}

// --- Injectable seams --------------------------------------------------------
//
// Every real-world boundary the harness crosses — env, filesystem, the scorer
// subprocess, the memory tiers, and the HTTP proof against a booted aisotropyd —
// is reached only through deps below. Production wiring (defaultDeps) uses the
// real implementations; tests substitute fakes so the whole orchestration in
// run() is exercisable without Docker, a Python venv, or a built scorer binary.

// appRunner is the subset of *aisotropy.App the HTTP proof needs.
type appRunner interface {
	Run(ctx context.Context) error
}

// httpGetter is the subset of *http.Client serveAndProbe needs.
type httpGetter interface {
	Get(url string) (*http.Response, error)
}

// bitrateStore is the subset of *bitrate.Store run() uses directly (beyond the
// Coordinator's narrower MeasurementStore seam). *bitrate.Store satisfies it;
// tests wrap a real in-memory store to inject a targeted failure in exactly one
// method (e.g. Series) without disturbing the others.
type bitrateStore interface {
	Save(ctx context.Context, m bitrate.Measurement) error
	Series(ctx context.Context, tenant, user string) ([]bitrate.Measurement, error)
	TrendSlope(ctx context.Context, tenant, user string) (float64, error)
	AllSummaries(ctx context.Context) ([]bitrate.TenantSummary, error)
	Close() error
}

// topicProbe/topicFacts/topicDocs are the FIXED, DIVERSE held-out probe set (K
// distinct topics) and the memory that answers each one — package-level so
// tests can reference the exact same content a fake scorer keys off of,
// without duplicating (and risking drift from) the strings run() seeds.
// Each topicProbe[j] is answered by topicFacts[j]/topicDocs[j] WITHOUT
// restating it verbatim (leakage guard in seedTopic).
var (
	topicProbe = []string{
		"When is the finance team weekly sync scheduled?",
		"How many vacation days does the HR policy grant new engineers?",
		"Which airline is preferred when booking company travel?",
		"What is the next milestone on the product roadmap?",
		"Who must approve the legal review before a vendor contract is signed?",
	}
	topicFacts = []string{
		"The finance team weekly sync is held on Tuesdays at 3pm.",
		"New engineers at Acme receive 22 vacation days per year under the HR policy.",
		"The preferred airline for Acme company travel bookings is SkyJet.",
		"The next product roadmap milestone is the v2 launch in March.",
		"Vendor contracts require legal review approval from general counsel Dana before signing.",
	}
	topicDocs = []string{
		"Acme finance operations handbook: the weekly sync meeting recurs every Tuesday at 3pm.",
		"Acme HR policy handbook: engineering new hires accrue 22 vacation days annually.",
		"Acme travel booking guide: SkyJet is the designated preferred carrier for all company trips.",
		"Acme product roadmap overview: the upcoming milestone is the v2 launch scheduled for March.",
		"Acme legal review process: general counsel Dana signs off on every vendor contract before signing.",
	}
)

type deps struct {
	stdout io.Writer
	getenv func(string) string
	statFn func(string) error

	mkdirTemp func(dir, pattern string) (string, error)
	setenv    func(key, val string) error

	openFacts        func() (facts.Store, error)
	openSemantic     func(ctx context.Context, dim int) (semantic.Store, error)
	newEmbedder      func() semantic.Embedder
	openBitrateStore func(dsn string) (bitrateStore, error)
	newRunner        func(bin string) bitrate.Runner

	newApp        func(addr string) appRunner
	newHTTPClient func() httpGetter
	now           func() time.Time
	sleep         func(time.Duration)

	readyTimeout    time.Duration
	pollInterval    time.Duration
	shutdownTimeout time.Duration
}

func defaultDeps() deps {
	return deps{
		stdout:    os.Stdout,
		getenv:    os.Getenv,
		statFn:    func(p string) error { _, err := os.Stat(p); return err },
		mkdirTemp: os.MkdirTemp,
		setenv:    os.Setenv,
		openFacts: facts.OpenFromEnv,
		openSemantic: func(ctx context.Context, dim int) (semantic.Store, error) {
			return semantic.OpenFromEnv(ctx, dim)
		},
		newEmbedder: func() semantic.Embedder { return semantic.EmbedderFromEnv() },
		openBitrateStore: func(dsn string) (bitrateStore, error) {
			return bitrate.OpenStore(dsn)
		},
		newRunner: func(bin string) bitrate.Runner { return bitrate.Worker{BinaryPath: bin} },
		newApp: func(addr string) appRunner {
			return aisotropy.New(aisotropy.WithAddr(addr))
		},
		newHTTPClient:   func() httpGetter { return &http.Client{Timeout: 5 * time.Second} },
		now:             time.Now,
		sleep:           time.Sleep,
		readyTimeout:    20 * time.Second,
		pollInterval:    300 * time.Millisecond,
		shutdownTimeout: 10 * time.Second,
	}
}

func main() {
	if code := runMain(os.Args[1:], defaultDeps()); code != 0 {
		os.Exit(code)
	}
}

// runMain is main()'s body: flag parsing plus the run() dispatch, split out so
// tests can drive it with injected deps and captured args.
func runMain(args []string, d deps) int {
	fs := flag.NewFlagSet("bitrate-harness", flag.ContinueOnError)
	fs.SetOutput(d.stdout)
	addr := fs.String("addr", ":18080", "HTTP listen address for the booted aisotropyd (the real /bitrate view)")
	httpProof := fs.Bool("http-proof", true, "boot the real aisotropyd and read the trend back over GET /health + GET /bitrate")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return run(*addr, *httpProof, d)
}

// checkScorerGate resolves AISOTROPY_BITRATE_BIN via getenv and validates it
// exists via statFn. A non-empty skipMsg means the harness must SKIP cleanly
// (exit 0) rather than fabricate a curve without a real scorer.
func checkScorerGate(getenv func(string) string, statFn func(string) error) (bin, skipMsg string) {
	bin = getenv("AISOTROPY_BITRATE_BIN")
	if bin == "" {
		return "", "SKIP: AISOTROPY_BITRATE_BIN unset — point it at a scorer wrapper to run the harness.\n" + dockerHint
	}
	if err := statFn(bin); err != nil {
		return "", fmt.Sprintf("SKIP: scorer binary %q not found: %v", bin, err)
	}
	return bin, ""
}

// tierLines reports exactly which substrate this run exercises, mirroring the
// runtime's own env precedence (RQLITE_URL / QDRANT_HOST). Pure with respect to
// getenv, so every combination (neither/one/both set) is directly table-testable.
func tierLines(getenv func(string) string) (lines []string, realTiers bool) {
	rqlite := getenv("RQLITE_URL")
	qdrant := getenv("QDRANT_HOST")
	realFacts := rqlite != ""
	realSemantic := qdrant != ""
	realTiers = realFacts && realSemantic

	if realFacts {
		lines = append(lines, fmt.Sprintf("facts:    REAL rqlite @ %s", rqlite))
	} else {
		lines = append(lines, "facts:    in-process SQLite (fallback — set RQLITE_URL for the real tier)")
	}
	if realSemantic {
		lines = append(lines, fmt.Sprintf("semantic: REAL Qdrant @ %s:%s", qdrant, envOr(getenv, "QDRANT_PORT", "6334")))
	} else {
		lines = append(lines, "semantic: in-process cosine (fallback — set QDRANT_HOST for the real tier)")
	}
	if realTiers {
		lines = append(lines, "mode:     PRODUCTION-LIKE (real networked memory tiers + real scorer + real /bitrate)")
	} else {
		lines = append(lines, "mode:     IN-PROCESS FALLBACK (Docker tiers not both set)")
		lines = append(lines, strings.Split(strings.Trim(dockerHint, "\n"), "\n")...)
	}
	return lines, realTiers
}

// checkLeakage guards seedTopic against folding a probe turn verbatim into
// memory: if the scored text were already distilled into the tenant's memory,
// ContextWith would trivially predict it and the measured lift would be
// spuriously high and meaningless (see internal/bitrate/sampler.go ShouldSample
// doc for the same anti-leakage contract).
func checkLeakage(probe []string, candidate string) error {
	for _, p := range probe {
		if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(p)) {
			return fmt.Errorf("LEAKAGE: candidate memory equals a probe turn verbatim: %q", candidate)
		}
	}
	return nil
}

// curveVerdict computes the KR8.1 headline downward-curve check over the
// AGGREGATE bpt_with series: a negative least-squares slope and a strictly
// lower final value than the first. Pure over the already-measured series.
func curveVerdict(aggWith []float64) (slope, totalDrop float64, ok bool, failLines []string) {
	n := len(aggWith)
	slope = leastSquaresSlope(aggWith)
	if n == 0 {
		return slope, 0, false, failLines
	}
	totalDrop = aggWith[0] - aggWith[n-1]
	ok = true
	if aggWith[n-1] >= aggWith[0] {
		failLines = append(failLines, fmt.Sprintf("FAIL: NO downward curve: agg bpt_with[last]=%.4f >= [first]=%.4f", aggWith[n-1], aggWith[0]))
		ok = false
	}
	if slope >= 0 {
		failLines = append(failLines, fmt.Sprintf("FAIL: agg bpt_with slope is not negative: %.5f", slope))
		ok = false
	}
	return slope, totalDrop, ok, failLines
}

// sustainedMetric is the SUSTAINED-descent metric (curve, not a single anchor
// cliff): applicable only for K>=3 steps with an overall positive drop.
func sustainedMetric(aggWith []float64) (applicable bool, firstStepDrop, postAnchorDrop, postAnchorFraction, maxRise float64) {
	n := len(aggWith)
	if n < 3 {
		return false, 0, 0, 0, 0
	}
	totalDrop := aggWith[0] - aggWith[n-1]
	if totalDrop <= 0 {
		return false, 0, 0, 0, 0
	}
	firstStepDrop = aggWith[0] - aggWith[1]
	postAnchorDrop = aggWith[1] - aggWith[n-1]
	postAnchorFraction = postAnchorDrop / totalDrop
	for i := 1; i < n; i++ {
		if rise := aggWith[i] - aggWith[i-1]; rise > maxRise {
			maxRise = rise
		}
	}
	return true, firstStepDrop, postAnchorDrop, postAnchorFraction, maxRise
}

func run(addr string, httpProof bool, d deps) int {
	ctx := context.Background()

	// --- Scorer gate: skip cleanly if the real scorer is unavailable. --------
	bin, skipMsg := checkScorerGate(d.getenv, d.statFn)
	if skipMsg != "" {
		fmt.Fprintln(d.stdout, skipMsg)
		return 0
	}

	// --- Tier selection: report exactly which substrate this run exercises. ---
	tLines, realTiers := tierLines(d.getenv)
	fmt.Fprintln(d.stdout, "== Bet-6 longitudinal harness (KR8.1 instrument) ==")
	fmt.Fprintf(d.stdout, "scorer:   %s\n", bin)
	for _, l := range tLines {
		fmt.Fprintln(d.stdout, l)
	}
	fmt.Fprintln(d.stdout, "NOTE: this is a SIMULATED evolving tenant (authored accumulation schedule),")
	fmt.Fprintln(d.stdout, "      NOT real users over weeks. It validates the production INSTRUMENT.")
	fmt.Fprintln(d.stdout)

	// A shared on-disk data dir so the bpt store the harness WRITES is the exact
	// file the booted aisotropyd READS through GET /bitrate. Set before opening
	// any store or booting the App, since both resolve it from the environment.
	dataDir, err := d.mkdirTemp("", "bitrate-harness-")
	if err != nil {
		fmt.Fprintf(d.stdout, "FAIL: scratch data dir: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dataDir) }()
	if err := d.setenv("AISOTROPY_DATA_DIR", dataDir); err != nil {
		fmt.Fprintf(d.stdout, "FAIL: set AISOTROPY_DATA_DIR: %v\n", err)
		return 1
	}

	// The FIXED, DIVERSE held-out probe set (K distinct topics) is the
	// package-level topicProbe/topicFacts/topicDocs above: each topicProbe[j] is
	// a QUESTION about one topic of the tenant's world, CONSTANT across all
	// steps, answered by topicFacts[j]/topicDocs[j] WITHOUT restating it
	// verbatim (leakage guard below).
	K := len(topicProbe)

	// --- Real (or fallback) memory tiers, built from the SAME env the runtime
	// uses, so the hydration below is byte-for-byte the production retrieval. ---
	factsStore, err := d.openFacts()
	if err != nil {
		fmt.Fprintf(d.stdout, "FAIL: open facts store: %v\n", err)
		return 1
	}
	defer func() { _ = factsStore.Close() }()
	factsScope, err := facts.NewScope(tenant, user)
	if err != nil {
		fmt.Fprintf(d.stdout, "FAIL: facts scope: %v\n", err)
		return 1
	}

	// EmbedderFromEnv is the exact embedder the booted App's ensureSemantic uses
	// (default HashEmbedder, dim 128), so the harness seeds Qdrant at the same
	// vector width the server later probes — no collection dimension conflict.
	embedder := d.newEmbedder()
	semStore, err := d.openSemantic(ctx, embedder.Dimension())
	if err != nil {
		fmt.Fprintf(d.stdout, "FAIL: open semantic store: %v\n", err)
		return 1
	}
	defer func() { _ = semStore.Close() }()
	semScope, err := semantic.NewScope(tenant, user)
	if err != nil {
		fmt.Fprintf(d.stdout, "FAIL: semantic scope: %v\n", err)
		return 1
	}

	// A clean slate on the real tiers so a previous run's seeds do not skew this
	// curve. Best-effort, scoped to THIS (tenant,user).
	resetScope(ctx, factsStore, factsScope, semStore, semScope, K)

	// Durable bpt store: the file GET /bitrate serves. One AGGREGATE row per step.
	bStore, err := d.openBitrateStore(filepath.Join(dataDir, "bitrate.db"))
	if err != nil {
		fmt.Fprintf(d.stdout, "FAIL: open bitrate store: %v\n", err)
		return 1
	}

	hydrator := api.BitrateHydrator{Store: factsStore, Semantic: semStore, Embedder: embedder}

	// seedTopic folds topic j's fact + doc into consolidated memory, guarding
	// against leakage (no seed may equal any probe turn verbatim).
	seedTopic := func(j int) error {
		if err := checkLeakage(topicProbe, topicFacts[j]); err != nil {
			return err
		}
		if err := checkLeakage(topicProbe, topicDocs[j]); err != nil {
			return err
		}
		f := facts.Fact{
			ID:         fmt.Sprintf("harness-fact-topic-%d", j),
			TenantID:   tenant,
			UserID:     user,
			Content:    topicFacts[j],
			Importance: 0.9,
		}
		if err := factsStore.Save(ctx, factsScope, f); err != nil {
			return fmt.Errorf("seed fact (topic %d): %w", j, err)
		}
		vecs, err := embedder.Embed(ctx, []string{topicDocs[j]})
		if err != nil || len(vecs) != 1 {
			return fmt.Errorf("embed doc (topic %d): %v (n=%d)", j, err, len(vecs))
		}
		src := fmt.Sprintf("harness-doc-topic-%d", j)
		point := semantic.Point{
			ID:       semantic.PointID(semScope, semantic.KindDocument, src),
			TenantID: tenant,
			UserID:   user,
			Kind:     semantic.KindDocument,
			Content:  topicDocs[j],
			Source:   src,
			Vector:   vecs[0],
		}
		if err := semStore.Upsert(ctx, semScope, []semantic.Point{point}); err != nil {
			return fmt.Errorf("seed semantic point (topic %d): %w", j, err)
		}
		return nil
	}

	// A deterministic, injected timeline (one simulated week per step): the
	// durable series must be ordered by step, not wall-clock.
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	const delta = 7 * 24 * time.Hour

	aggWith := make([]float64, 0, K)
	aggWithout := make([]float64, 0, K)

	fmt.Fprintln(d.stdout, "-- per-step measurement (real coordinator -> hydrator -> scorer -> store) --")
	for i := 0; i < K; i++ {
		if err := seedTopic(i); err != nil {
			fmt.Fprintf(d.stdout, "FAIL: %v\n", err)
			_ = bStore.Close()
			return 1
		}

		samples := make([]bitrate.Sample, K)
		for j := 0; j < K; j++ {
			samples[j] = bitrate.Sample{
				TenantID:  tenant,
				UserID:    user,
				SessionID: session,
				SampleID:  fmt.Sprintf("harness-probe-t%d-s%d", j, i),
				Text:      topicProbe[j],
			}
		}

		coord := bitrate.Coordinator{
			Sampler:     diverseProbeSampler{samples: samples},
			Hydrator:    hydrator,
			Runner:      d.newRunner(bin),
			Store:       &captureStore{}, // throwaway; the durable aggregate write is below
			SampleBatch: K,
		}
		ms, err := coord.RunOnce(ctx)
		if err != nil {
			fmt.Fprintf(d.stdout, "FAIL: coordinator RunOnce (step %d): %v\n", i, err)
			_ = bStore.Close()
			return 1
		}
		if len(ms) != K {
			fmt.Fprintf(d.stdout, "FAIL: step %d: expected %d measurements over the diverse probe, got %d\n", i, K, len(ms))
			_ = bStore.Close()
			return 1
		}

		withs := make([]float64, len(ms))
		withouts := make([]float64, len(ms))
		var nTokTotal int
		modelID := ms[0].ModelID
		for k, m := range ms {
			withs[k] = m.BptWith
			withouts[k] = m.BptWithout
			nTokTotal += m.NTokens
		}
		meanWith := mean(withs)
		meanWithout := mean(withouts)
		aggWith = append(aggWith, meanWith)
		aggWithout = append(aggWithout, meanWithout)

		agg := bitrate.Measurement{
			TenantID:   tenant,
			UserID:     user,
			MeasuredAt: base.Add(time.Duration(i) * delta),
			BptWith:    meanWith,
			BptWithout: meanWithout,
			Lift:       meanWithout - meanWith,
			NTokens:    nTokTotal,
			ModelID:    modelID,
			SampleID:   fmt.Sprintf("harness-agg-step-%d", i),
		}
		if err := bStore.Save(ctx, agg); err != nil {
			fmt.Fprintf(d.stdout, "FAIL: durable save (step %d): %v\n", i, err)
			_ = bStore.Close()
			return 1
		}

		fmt.Fprintf(d.stdout, "step %d  topics_covered=%d/%d  agg_bpt_with=%.4f  agg_bpt_without=%.4f  agg_lift=%.4f  model=%s  per_turn_with=%v\n",
			i, i+1, K, meanWith, meanWithout, meanWithout-meanWith, modelID, roundAll(withs))
	}
	fmt.Fprintln(d.stdout)

	// --- Headline KR8.1 downward-curve assertions (over the AGGREGATE). -------
	slope, totalDrop, ok, failLines := curveVerdict(aggWith)
	fmt.Fprintln(d.stdout, "-- KR8.1 downward curve --")
	fmt.Fprintf(d.stdout, "AGG bpt_with:    %v\n", roundAll(aggWith))
	fmt.Fprintf(d.stdout, "AGG bpt_without: %v  (control; ~constant bare persona)\n", roundAll(aggWithout))
	fmt.Fprintf(d.stdout, "least-squares slope over AGG bpt_with = %.5f  (want NEGATIVE)\n", slope)
	fmt.Fprintf(d.stdout, "total drop bpt_with[0]-bpt_with[last] = %.5f\n", totalDrop)
	for _, l := range failLines {
		fmt.Fprintln(d.stdout, l)
	}

	// --- SUSTAINED-descent metric (curve, not a single anchor cliff). --------
	if applicable, firstStepDrop, postAnchorDrop, postAnchorFraction, maxRise := sustainedMetric(aggWith); applicable {
		n := len(aggWith)
		fmt.Fprintln(d.stdout, "-- sustained-descent metric --")
		fmt.Fprintf(d.stdout, "first-step drop (0->1)     = %.5f\n", firstStepDrop)
		fmt.Fprintf(d.stdout, "post-anchor drop (1->last) = %.5f\n", postAnchorDrop)
		fmt.Fprintf(d.stdout, "postAnchorFraction         = %.4f  (anchor-artifact ~0, ideal ~%.2f)\n",
			postAnchorFraction, float64(n-2)/float64(n-1))
		fmt.Fprintf(d.stdout, "max per-step RISE          = %.5f\n", maxRise)
	}
	fmt.Fprintln(d.stdout)

	// --- Prove the AGGREGATE series persists and round-trips (the /bitrate data).
	series, err := bStore.Series(ctx, tenant, user)
	if err != nil {
		fmt.Fprintf(d.stdout, "FAIL: read series back: %v\n", err)
		_ = bStore.Close()
		return 1
	}
	readBack := make([]float64, len(series))
	for i, m := range series {
		readBack[i] = m.BptWith
	}
	liftSlope, err := bStore.TrendSlope(ctx, tenant, user)
	if err != nil {
		fmt.Fprintf(d.stdout, "FAIL: store TrendSlope: %v\n", err)
		_ = bStore.Close()
		return 1
	}
	summaries, err := bStore.AllSummaries(ctx)
	if err != nil {
		fmt.Fprintf(d.stdout, "FAIL: store AllSummaries: %v\n", err)
		_ = bStore.Close()
		return 1
	}
	fmt.Fprintln(d.stdout, "-- durable store read-back (what GET /bitrate serves) --")
	fmt.Fprintf(d.stdout, "Series bpt_with (oldest->newest): %v  (%d rows)\n", roundAll(readBack), len(series))
	fmt.Fprintf(d.stdout, "Store.TrendSlope over LIFT = %.5f  (POSITIVE mirror of the downward bpt_with curve)\n", liftSlope)
	sumJSON, _ := json.MarshalIndent(summaries, "", "  ")
	fmt.Fprintf(d.stdout, "AllSummaries:\n%s\n\n", sumJSON)

	// The booted App reopens this same on-disk file; close the harness's writer
	// handle first so there is a single writer at a time.
	if err := bStore.Close(); err != nil {
		fmt.Fprintf(d.stdout, "WARN: bitrate store close: %v\n", err)
	}

	// --- HTTP proof: read the SAME trend back through the REAL aisotropyd. ----
	if httpProof {
		if err := serveAndProbe(d, addr, tenant, realTiers); err != nil {
			// A failed HTTP proof does NOT invalidate the curve (the store-level
			// read above already satisfies "read the trend back"); surface it but
			// keep the core verdict.
			fmt.Fprintf(d.stdout, "WARN: HTTP proof did not complete: %v\n", err)
			fmt.Fprintln(d.stdout, "      (the store-level Series/TrendSlope read above still evidences the trend)")
		}
	}

	fmt.Fprintln(d.stdout)
	if ok {
		fmt.Fprintln(d.stdout, "RESULT: PASS — downward bpt curve + negative slope captured through the real pipeline.")
		return 0
	}
	fmt.Fprintln(d.stdout, "RESULT: FAIL — the curve did not descend (see FAIL lines above).")
	return 1
}

// serveAndProbe boots the REAL aisotropyd (aisotropy.App) sharing this process's
// env (real tiers + the same on-disk bpt store), waits for liveness, then reads
// GET /health and GET /bitrate — the exact operator surface — and prints them.
func serveAndProbe(d deps, addr, tenantID string, realTiers bool) error {
	fmt.Fprintln(d.stdout, "-- HTTP proof: booting the real aisotropyd (aisotropy.App) --")
	appCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app := d.newApp(addr)
	runErr := make(chan error, 1)
	go func() { runErr <- app.Run(appCtx) }()

	base := "http://127.0.0.1" + addr
	client := d.newHTTPClient()

	// Poll liveness (/healthz is outside the OpenAPI contract, returns 200 ok).
	live := false
	deadline := d.now().Add(d.readyTimeout)
	for d.now().Before(deadline) {
		select {
		case e := <-runErr:
			return fmt.Errorf("server exited before ready: %v", e)
		default:
		}
		resp, err := client.Get(base + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				live = true
				break
			}
		}
		d.sleep(d.pollInterval)
	}
	if !live {
		return fmt.Errorf("aisotropyd did not become live at %s within timeout", base)
	}
	fmt.Fprintf(d.stdout, "GET /healthz -> 200 (aisotropyd live at %s)\n", base)

	// Readiness: probes the REAL rqlite + Qdrant when realTiers.
	healthBody, healthCode, err := getBody(client, base+"/health")
	if err != nil {
		return fmt.Errorf("GET /health: %w", err)
	}
	label := "in-process tiers"
	if realTiers {
		label = "REAL rqlite + Qdrant"
	}
	fmt.Fprintf(d.stdout, "GET /health  -> %d (%s): %s\n", healthCode, label, compact(healthBody))

	// The trend, read back through the operator view.
	bitrateBody, bitrateCode, err := getBody(client, base+"/bitrate")
	if err != nil {
		return fmt.Errorf("GET /bitrate: %w", err)
	}
	fmt.Fprintf(d.stdout, "GET /bitrate -> %d: %s\n", bitrateCode, compact(bitrateBody))
	if bitrateCode != http.StatusOK {
		return fmt.Errorf("/bitrate returned %d", bitrateCode)
	}
	if !strings.Contains(bitrateBody, tenantID) {
		return fmt.Errorf("/bitrate did not include tenant %q (the trend was not served)", tenantID)
	}
	fmt.Fprintf(d.stdout, "OK: /bitrate served the %q trend through the real HTTP surface.\n", tenantID)

	// Graceful shutdown.
	cancel()
	select {
	case <-runErr:
	case <-time.After(d.shutdownTimeout):
		fmt.Fprintln(d.stdout, "WARN: aisotropyd shutdown did not complete within the shutdown timeout")
	}
	return nil
}

// resetScope removes any facts/semantic seeds a previous harness run left in the
// real tiers for THIS (tenant,user), so the curve starts from an empty memory.
// Best-effort: a not-found or a store without the delete path is fine.
func resetScope(ctx context.Context, fs facts.Store, fsc facts.Scope, ss semantic.Store, ssc semantic.Scope, k int) {
	ids := make([]string, k)
	for j := 0; j < k; j++ {
		ids[j] = fmt.Sprintf("harness-fact-topic-%d", j)
		_, _ = ss.DeleteBySource(ctx, ssc, semantic.KindDocument, fmt.Sprintf("harness-doc-topic-%d", j))
	}
	_ = fs.Delete(ctx, fsc, ids)
}

// captureStore is a throwaway bitrate.MeasurementStore: the coordinator writes
// per-sample rows here (stamped time.Now()), and the harness re-persists ONE
// injected-timestamp aggregate row per step to the durable store instead.
type captureStore struct{ last bitrate.Measurement }

func (c *captureStore) Save(_ context.Context, m bitrate.Measurement) error {
	c.last = m
	return nil
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// leastSquaresSlope is the OLS slope of ys over x = 0,1,…,n-1 — the KR8.1 headline
// is the slope over bpt_with (the downward curve).
func leastSquaresSlope(ys []float64) float64 {
	n := len(ys)
	if n < 2 {
		return 0
	}
	var sumX, sumY, sumXY, sumXX float64
	for i, y := range ys {
		x := float64(i)
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	fn := float64(n)
	denom := fn*sumXX - sumX*sumX
	if denom == 0 {
		return 0
	}
	return (fn*sumXY - sumX*sumY) / denom
}

func roundAll(xs []float64) []float64 {
	out := make([]float64, len(xs))
	for i, x := range xs {
		out[i] = float64(int64(x*10000+0.5)) / 10000
	}
	return out
}

func getBody(c httpGetter, url string) (string, int, error) {
	resp, err := c.Get(url)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", resp.StatusCode, err
	}
	return string(b), resp.StatusCode, nil
}

// compact collapses a JSON body to a single readable line.
func compact(s string) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(s)); err != nil {
		return strings.TrimSpace(s)
	}
	return buf.String()
}

func envOr(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

const dockerHint = `
For the PRODUCTION-LIKE run against the real memory tiers:
  docker compose up -d rqlite qdrant
  AISOTROPY_BITRATE_BIN=/abs/wrapper.sh \
    RQLITE_URL=http://localhost:4001 QDRANT_HOST=localhost QDRANT_PORT=6334 \
    go run ./cmd/bitrate-harness
(the wrapper execs spikes/compression/.venv/bin/python spikes/compression/bitrate_scorer.py "$@")`
