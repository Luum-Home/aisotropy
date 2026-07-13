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

func main() {
	addr := flag.String("addr", ":18080", "HTTP listen address for the booted aisotropyd (the real /bitrate view)")
	httpProof := flag.Bool("http-proof", true, "boot the real aisotropyd and read the trend back over GET /health + GET /bitrate")
	flag.Parse()

	if code := run(*addr, *httpProof); code != 0 {
		os.Exit(code)
	}
}

func run(addr string, httpProof bool) int {
	ctx := context.Background()

	// --- Scorer gate: skip cleanly if the real scorer is unavailable. --------
	bin := os.Getenv("AISOTROPY_BITRATE_BIN")
	if bin == "" {
		fmt.Println("SKIP: AISOTROPY_BITRATE_BIN unset — point it at a scorer wrapper to run the harness.")
		fmt.Println(dockerHint)
		return 0
	}
	if _, err := os.Stat(bin); err != nil {
		fmt.Printf("SKIP: scorer binary %q not found: %v\n", bin, err)
		return 0
	}

	// --- Tier selection: report exactly which substrate this run exercises. ---
	rqlite := os.Getenv("RQLITE_URL")
	qdrant := os.Getenv("QDRANT_HOST")
	realFacts := rqlite != ""
	realSemantic := qdrant != ""
	realTiers := realFacts && realSemantic

	fmt.Println("== Bet-6 longitudinal harness (KR8.1 instrument) ==")
	fmt.Printf("scorer:   %s\n", bin)
	if realFacts {
		fmt.Printf("facts:    REAL rqlite @ %s\n", rqlite)
	} else {
		fmt.Println("facts:    in-process SQLite (fallback — set RQLITE_URL for the real tier)")
	}
	if realSemantic {
		fmt.Printf("semantic: REAL Qdrant @ %s:%s\n", qdrant, envOr("QDRANT_PORT", "6334"))
	} else {
		fmt.Println("semantic: in-process cosine (fallback — set QDRANT_HOST for the real tier)")
	}
	if realTiers {
		fmt.Println("mode:     PRODUCTION-LIKE (real networked memory tiers + real scorer + real /bitrate)")
	} else {
		fmt.Println("mode:     IN-PROCESS FALLBACK (Docker tiers not both set)")
		fmt.Println(dockerHint)
	}
	fmt.Println("NOTE: this is a SIMULATED evolving tenant (authored accumulation schedule),")
	fmt.Println("      NOT real users over weeks. It validates the production INSTRUMENT.")
	fmt.Println()

	// A shared on-disk data dir so the bpt store the harness WRITES is the exact
	// file the booted aisotropyd READS through GET /bitrate. Set before opening
	// any store or booting the App, since both resolve it from the environment.
	dataDir, err := os.MkdirTemp("", "bitrate-harness-")
	if err != nil {
		fmt.Printf("FAIL: scratch data dir: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dataDir) }()
	if err := os.Setenv("AISOTROPY_DATA_DIR", dataDir); err != nil {
		fmt.Printf("FAIL: set AISOTROPY_DATA_DIR: %v\n", err)
		return 1
	}

	// --- The FIXED, DIVERSE held-out probe set (K distinct topics). ----------
	// Each is a QUESTION about one topic of the tenant's world, CONSTANT across
	// all steps. topicProbe[j] is answered by topicFacts[j]/topicDocs[j] WITHOUT
	// restating it verbatim (leakage guard below).
	topicProbe := []string{
		"When is the finance team weekly sync scheduled?",
		"How many vacation days does the HR policy grant new engineers?",
		"Which airline is preferred when booking company travel?",
		"What is the next milestone on the product roadmap?",
		"Who must approve the legal review before a vendor contract is signed?",
	}
	topicFacts := []string{
		"The finance team weekly sync is held on Tuesdays at 3pm.",
		"New engineers at Acme receive 22 vacation days per year under the HR policy.",
		"The preferred airline for Acme company travel bookings is SkyJet.",
		"The next product roadmap milestone is the v2 launch in March.",
		"Vendor contracts require legal review approval from general counsel Dana before signing.",
	}
	topicDocs := []string{
		"Acme finance operations handbook: the weekly sync meeting recurs every Tuesday at 3pm.",
		"Acme HR policy handbook: engineering new hires accrue 22 vacation days annually.",
		"Acme travel booking guide: SkyJet is the designated preferred carrier for all company trips.",
		"Acme product roadmap overview: the upcoming milestone is the v2 launch scheduled for March.",
		"Acme legal review process: general counsel Dana signs off on every vendor contract before signing.",
	}
	K := len(topicProbe)

	// --- Real (or fallback) memory tiers, built from the SAME env the runtime
	// uses, so the hydration below is byte-for-byte the production retrieval. ---
	factsStore, err := facts.OpenFromEnv()
	if err != nil {
		fmt.Printf("FAIL: open facts store: %v\n", err)
		return 1
	}
	defer func() { _ = factsStore.Close() }()
	factsScope, err := facts.NewScope(tenant, user)
	if err != nil {
		fmt.Printf("FAIL: facts scope: %v\n", err)
		return 1
	}

	// EmbedderFromEnv is the exact embedder the booted App's ensureSemantic uses
	// (default HashEmbedder, dim 128), so the harness seeds Qdrant at the same
	// vector width the server later probes — no collection dimension conflict.
	embedder := semantic.EmbedderFromEnv()
	semStore, err := semantic.OpenFromEnv(ctx, embedder.Dimension())
	if err != nil {
		fmt.Printf("FAIL: open semantic store: %v\n", err)
		return 1
	}
	defer func() { _ = semStore.Close() }()
	semScope, err := semantic.NewScope(tenant, user)
	if err != nil {
		fmt.Printf("FAIL: semantic scope: %v\n", err)
		return 1
	}

	// A clean slate on the real tiers so a previous run's seeds do not skew this
	// curve. Best-effort, scoped to THIS (tenant,user).
	resetScope(ctx, factsStore, factsScope, semStore, semScope, K)

	// Durable bpt store: the file GET /bitrate serves. One AGGREGATE row per step.
	bStore, err := bitrate.OpenStore(filepath.Join(dataDir, "bitrate.db"))
	if err != nil {
		fmt.Printf("FAIL: open bitrate store: %v\n", err)
		return 1
	}

	hydrator := api.BitrateHydrator{Store: factsStore, Semantic: semStore, Embedder: embedder}

	// seedTopic folds topic j's fact + doc into consolidated memory, guarding
	// against leakage (no seed may equal any probe turn verbatim).
	seedTopic := func(j int) error {
		for _, probe := range topicProbe {
			if strings.EqualFold(strings.TrimSpace(topicFacts[j]), strings.TrimSpace(probe)) ||
				strings.EqualFold(strings.TrimSpace(topicDocs[j]), strings.TrimSpace(probe)) {
				return fmt.Errorf("LEAKAGE: topic %d memory equals a probe turn verbatim", j)
			}
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

	fmt.Println("-- per-step measurement (real coordinator -> hydrator -> scorer -> store) --")
	for i := 0; i < K; i++ {
		if err := seedTopic(i); err != nil {
			fmt.Printf("FAIL: %v\n", err)
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
			Runner:      bitrate.Worker{BinaryPath: bin},
			Store:       &captureStore{}, // throwaway; the durable aggregate write is below
			SampleBatch: K,
		}
		ms, err := coord.RunOnce(ctx)
		if err != nil {
			fmt.Printf("FAIL: coordinator RunOnce (step %d): %v\n", i, err)
			_ = bStore.Close()
			return 1
		}
		if len(ms) != K {
			fmt.Printf("FAIL: step %d: expected %d measurements over the diverse probe, got %d\n", i, K, len(ms))
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
			fmt.Printf("FAIL: durable save (step %d): %v\n", i, err)
			_ = bStore.Close()
			return 1
		}

		fmt.Printf("step %d  topics_covered=%d/%d  agg_bpt_with=%.4f  agg_bpt_without=%.4f  agg_lift=%.4f  model=%s  per_turn_with=%v\n",
			i, i+1, K, meanWith, meanWithout, meanWithout-meanWith, modelID, roundAll(withs))
	}
	fmt.Println()

	// --- Headline KR8.1 downward-curve assertions (over the AGGREGATE). -------
	slope := leastSquaresSlope(aggWith)
	totalDrop := aggWith[0] - aggWith[K-1]
	fmt.Println("-- KR8.1 downward curve --")
	fmt.Printf("AGG bpt_with:    %v\n", roundAll(aggWith))
	fmt.Printf("AGG bpt_without: %v  (control; ~constant bare persona)\n", roundAll(aggWithout))
	fmt.Printf("least-squares slope over AGG bpt_with = %.5f  (want NEGATIVE)\n", slope)
	fmt.Printf("total drop bpt_with[0]-bpt_with[last] = %.5f\n", totalDrop)

	ok := true
	if aggWith[K-1] >= aggWith[0] {
		fmt.Printf("FAIL: NO downward curve: agg bpt_with[last]=%.4f >= [first]=%.4f\n", aggWith[K-1], aggWith[0])
		ok = false
	}
	if slope >= 0 {
		fmt.Printf("FAIL: agg bpt_with slope is not negative: %.5f\n", slope)
		ok = false
	}

	// --- SUSTAINED-descent metric (curve, not a single anchor cliff). --------
	if K >= 3 && totalDrop > 0 {
		firstStepDrop := aggWith[0] - aggWith[1]
		postAnchorDrop := aggWith[1] - aggWith[K-1]
		postAnchorFraction := postAnchorDrop / totalDrop
		maxRise := 0.0
		for i := 1; i < K; i++ {
			if rise := aggWith[i] - aggWith[i-1]; rise > maxRise {
				maxRise = rise
			}
		}
		fmt.Println("-- sustained-descent metric --")
		fmt.Printf("first-step drop (0->1)     = %.5f\n", firstStepDrop)
		fmt.Printf("post-anchor drop (1->last) = %.5f\n", postAnchorDrop)
		fmt.Printf("postAnchorFraction         = %.4f  (anchor-artifact ~0, ideal ~%.2f)\n",
			postAnchorFraction, float64(K-2)/float64(K-1))
		fmt.Printf("max per-step RISE          = %.5f\n", maxRise)
	}
	fmt.Println()

	// --- Prove the AGGREGATE series persists and round-trips (the /bitrate data).
	series, err := bStore.Series(ctx, tenant, user)
	if err != nil {
		fmt.Printf("FAIL: read series back: %v\n", err)
		_ = bStore.Close()
		return 1
	}
	readBack := make([]float64, len(series))
	for i, m := range series {
		readBack[i] = m.BptWith
	}
	liftSlope, err := bStore.TrendSlope(ctx, tenant, user)
	if err != nil {
		fmt.Printf("FAIL: store TrendSlope: %v\n", err)
		_ = bStore.Close()
		return 1
	}
	summaries, err := bStore.AllSummaries(ctx)
	if err != nil {
		fmt.Printf("FAIL: store AllSummaries: %v\n", err)
		_ = bStore.Close()
		return 1
	}
	fmt.Println("-- durable store read-back (what GET /bitrate serves) --")
	fmt.Printf("Series bpt_with (oldest->newest): %v  (%d rows)\n", roundAll(readBack), len(series))
	fmt.Printf("Store.TrendSlope over LIFT = %.5f  (POSITIVE mirror of the downward bpt_with curve)\n", liftSlope)
	sumJSON, _ := json.MarshalIndent(summaries, "", "  ")
	fmt.Printf("AllSummaries:\n%s\n\n", sumJSON)

	// The booted App reopens this same on-disk file; close the harness's writer
	// handle first so there is a single writer at a time.
	if err := bStore.Close(); err != nil {
		fmt.Printf("WARN: bitrate store close: %v\n", err)
	}

	// --- HTTP proof: read the SAME trend back through the REAL aisotropyd. ----
	if httpProof {
		if err := serveAndProbe(addr, tenant, realTiers); err != nil {
			// A failed HTTP proof does NOT invalidate the curve (the store-level
			// read above already satisfies "read the trend back"); surface it but
			// keep the core verdict.
			fmt.Printf("WARN: HTTP proof did not complete: %v\n", err)
			fmt.Println("      (the store-level Series/TrendSlope read above still evidences the trend)")
		}
	}

	fmt.Println()
	if ok {
		fmt.Println("RESULT: PASS — downward bpt curve + negative slope captured through the real pipeline.")
		return 0
	}
	fmt.Println("RESULT: FAIL — the curve did not descend (see FAIL lines above).")
	return 1
}

// serveAndProbe boots the REAL aisotropyd (aisotropy.App) sharing this process's
// env (real tiers + the same on-disk bpt store), waits for liveness, then reads
// GET /health and GET /bitrate — the exact operator surface — and prints them.
func serveAndProbe(addr, tenantID string, realTiers bool) error {
	fmt.Println("-- HTTP proof: booting the real aisotropyd (aisotropy.App) --")
	appCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app := aisotropy.New(aisotropy.WithAddr(addr))
	runErr := make(chan error, 1)
	go func() { runErr <- app.Run(appCtx) }()

	base := "http://127.0.0.1" + addr
	client := &http.Client{Timeout: 5 * time.Second}

	// Poll liveness (/healthz is outside the OpenAPI contract, returns 200 ok).
	live := false
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
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
		time.Sleep(300 * time.Millisecond)
	}
	if !live {
		return fmt.Errorf("aisotropyd did not become live at %s within timeout", base)
	}
	fmt.Printf("GET /healthz -> 200 (aisotropyd live at %s)\n", base)

	// Readiness: probes the REAL rqlite + Qdrant when realTiers.
	healthBody, healthCode, err := getBody(client, base+"/health")
	if err != nil {
		return fmt.Errorf("GET /health: %w", err)
	}
	label := "in-process tiers"
	if realTiers {
		label = "REAL rqlite + Qdrant"
	}
	fmt.Printf("GET /health  -> %d (%s): %s\n", healthCode, label, compact(healthBody))

	// The trend, read back through the operator view.
	bitrateBody, bitrateCode, err := getBody(client, base+"/bitrate")
	if err != nil {
		return fmt.Errorf("GET /bitrate: %w", err)
	}
	fmt.Printf("GET /bitrate -> %d: %s\n", bitrateCode, compact(bitrateBody))
	if bitrateCode != http.StatusOK {
		return fmt.Errorf("/bitrate returned %d", bitrateCode)
	}
	if !strings.Contains(bitrateBody, tenantID) {
		return fmt.Errorf("/bitrate did not include tenant %q (the trend was not served)", tenantID)
	}
	fmt.Printf("OK: /bitrate served the %q trend through the real HTTP surface.\n", tenantID)

	// Graceful shutdown.
	cancel()
	select {
	case <-runErr:
	case <-time.After(10 * time.Second):
		fmt.Println("WARN: aisotropyd shutdown did not complete within 10s")
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

func getBody(c *http.Client, url string) (string, int, error) {
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

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
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
