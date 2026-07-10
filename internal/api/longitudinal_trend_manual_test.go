//go:build manual

// End-to-end validation of the "DOWNWARD curve" half of KR8.1: as a tenant's
// CONSOLIDATED memory accumulates over time, Bet 6's bits-per-token metric on a
// FIXED held-out probe trends DOWNWARD. Behind the `manual` build tag, so it is
// EXCLUDED from `go test ./...` and CI: it needs a Python venv with torch and the
// scorer wrapper (identical prerequisites to live_lift_manual_test.go).
//
// HONESTY (this is a CONTROLLED SIMULATION, not real longitudinal traffic):
// there is no real production tenant generating turns over weeks. Instead a
// SINGLE tenant's consolidated memory (facts + semantic) is GROWN across T
// discrete measurement points, and at each point bpt_with is measured over the
// SAME fixed held-out probe through the REAL runtime path —
// bitrate.Coordinator -> BitrateHydrator (the exact hydrateSystemPrompt code,
// nil working memory so only consolidated memory contributes, no leakage) ->
// bitrate.Worker (the real Python scorer subprocess) -> bitrate.Store. This
// proves the METRIC captures "the system is learning the tenant" as a downward
// bpt curve when memory accumulates, and that the store persists/serves the
// series. It is NOT a substitute for a real longitudinal deployment: the
// accumulation schedule here is authored, and the trend's steepness depends on
// that schedule (see the report). It validates the trend MECHANISM and the
// store's series persistence, not field behavior.
//
// The scored MeasuredAt is injected (base + i*delta), NOT time.Now(): the
// coordinator stamps time.Now() on its own throwaway store, but the DURABLE
// series is written here with explicit, monotonically advancing timestamps so
// the simulated weeks are ordered deterministically.
//
// Point AISOTROPY_BITRATE_BIN at an executable that forwards --input/--output to
// the scorer (same wrapper as the live-lift test), e.g.:
//
//	#!/bin/bash
//	exec /abs/path/spikes/compression/.venv/bin/python \
//	     /abs/path/spikes/compression/bitrate_scorer.py "$@"
//
// then run:
//
//	AISOTROPY_BITRATE_BIN=/abs/path/wrapper.sh \
//	  go test -tags manual -run TestLongitudinalDownwardTrend -v ./internal/api
package api

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/bitrate"
	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

// fixedSampler is a bitrate.Sampler that always returns exactly one held-out
// sample — the FIXED probe for the current step. The probe TEXT is constant
// across steps (the invariant that makes the bpt series comparable); only the
// SampleID advances so each step lands as a distinct row in the durable series.
type fixedSampler struct{ sample bitrate.Sample }

func (f fixedSampler) HeldOut(_ context.Context, _ int) ([]bitrate.Sample, error) {
	return []bitrate.Sample{f.sample}, nil
}

// captureStore is a throwaway bitrate.MeasurementStore that just records the
// last measurement the coordinator computed. The coordinator stamps its own
// time.Now() here; the test then re-persists the measurement to the DURABLE
// store with an explicit, injected MeasuredAt so the series is deterministically
// ordered across the simulated timeline.
type captureStore struct{ last bitrate.Measurement }

func (c *captureStore) Save(_ context.Context, m bitrate.Measurement) error {
	c.last = m
	return nil
}

// leastSquaresSlope returns the OLS slope of ys over x = 0,1,…,n-1 — the same
// definition internal/bitrate.trendSlope uses, reimplemented here because that
// helper is unexported and computes the slope over LIFT, whereas the KR8.1
// headline is the slope over bpt_with (the DOWNWARD curve).
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

func TestLongitudinalDownwardTrend(t *testing.T) {
	bin := os.Getenv("AISOTROPY_BITRATE_BIN")
	if bin == "" {
		t.Skip("AISOTROPY_BITRATE_BIN unset; set it to a scorer wrapper to run the longitudinal downward-trend test")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("scorer binary %q not found: %v", bin, err)
	}

	ctx := context.Background()
	const (
		tenant = "acme"
		user   = "alice"
		steps  = 5
	)

	// The FIXED held-out probe: a plausible user turn about the tenant's domain
	// (finance-team logistics), constant across all steps. It is a QUESTION; the
	// seeded memory below is a set of STATEMENTS. None of the seeds equal it
	// verbatim (asserted in seedStep), so the falling bpt is real consolidated-
	// memory signal, not the scorer reading back the probe it was handed.
	const probe = "When is the finance team weekly sync?"

	// --- Consolidated memory stores (grown across steps). --------------------
	factsStore, err := facts.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open facts store: %v", err)
	}
	defer func() { _ = factsStore.Close() }()
	factsScope, err := facts.NewScope(tenant, user)
	if err != nil {
		t.Fatalf("facts scope: %v", err)
	}

	embedder := semantic.NewHashEmbedder(128)
	semStore := semantic.NewInMemoryStore(embedder.Dimension())
	defer func() { _ = semStore.Close() }()
	semScope, err := semantic.NewScope(tenant, user)
	if err != nil {
		t.Fatalf("semantic scope: %v", err)
	}

	// --- Durable bpt store: the series is read back from here at the end. -----
	bStore, err := bitrate.OpenStore(":memory:")
	if err != nil {
		t.Fatalf("open bitrate store: %v", err)
	}
	defer func() { _ = bStore.Close() }()

	hydrator := BitrateHydrator{Store: factsStore, Semantic: semStore, Embedder: embedder}

	// The monotonically GROWING accumulation schedule. Each step ADDS more
	// consolidated memory about the probe's domain (cumulative — earlier seeds
	// persist). Step 0 adds NOTHING: an empty tenant memory, so the probe is
	// maximally surprising (high bpt). Later steps enrich it, so bpt falls. The
	// schedule mirrors what hydrateSystemPrompt can retrieve (factsWindow=4,
	// semanticWindow=4), so the retrieved context genuinely grows step to step.
	factAdds := [steps][]string{
		{}, // step 0: empty memory
		{"The finance team weekly sync is on Tuesdays at 3pm."},
		{"Alice manages the finance team at Acme and leads the weekly sync."},
		{"The finance team weekly sync reviews budget logistics and shipping costs."},
		{"The finance team weekly sync agenda is set every Tuesday afternoon by Alice."},
	}
	docAdds := [steps][]string{
		{},
		{},
		{"Acme finance team operations: the weekly sync meeting is held every Tuesday at 3pm."},
		{},
		{"Finance team logistics playbook: weekly sync covers budget, shipping, and quarterly review."},
	}

	// seedStep folds one step's additions into the consolidated stores, guarding
	// against leakage (no seed may equal the probe verbatim).
	factSeq := 0
	docSeq := 0
	seedStep := func(i int) {
		for _, content := range factAdds[i] {
			if strings.EqualFold(strings.TrimSpace(content), probe) {
				t.Fatalf("LEAKAGE: step %d fact equals the probe verbatim: %q", i, content)
			}
			f := facts.Fact{
				ID:         fmt.Sprintf("fact-%d", factSeq),
				TenantID:   tenant,
				UserID:     user,
				Content:    content,
				Importance: 0.9,
			}
			if err := factsStore.Save(ctx, factsScope, f); err != nil {
				t.Fatalf("seed fact (step %d): %v", i, err)
			}
			factSeq++
		}
		for _, content := range docAdds[i] {
			if strings.EqualFold(strings.TrimSpace(content), probe) {
				t.Fatalf("LEAKAGE: step %d doc equals the probe verbatim: %q", i, content)
			}
			vecs, err := embedder.Embed(ctx, []string{content})
			if err != nil || len(vecs) != 1 {
				t.Fatalf("embed doc (step %d): %v (n=%d)", i, err, len(vecs))
			}
			src := fmt.Sprintf("doc-%d", docSeq)
			point := semantic.Point{
				ID:       semantic.PointID(semScope, semantic.KindDocument, src),
				TenantID: tenant,
				UserID:   user,
				Kind:     semantic.KindDocument,
				Content:  content,
				Source:   src,
				Vector:   vecs[0],
			}
			if err := semStore.Upsert(ctx, semScope, []semantic.Point{point}); err != nil {
				t.Fatalf("seed semantic point (step %d): %v", i, err)
			}
			docSeq++
		}
	}

	// A deterministic, injected timeline: base + i*delta. NOT time.Now() — the
	// durable series must be ordered by the simulated step, not wall-clock.
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	const delta = 7 * 24 * time.Hour // one simulated week per step

	bptWith := make([]float64, 0, steps)
	bptWithout := make([]float64, 0, steps)

	for i := 0; i < steps; i++ {
		// Grow consolidated memory BEFORE measuring, so step i sees the memory
		// accumulated through step i.
		seedStep(i)

		sampleID := fmt.Sprintf("probe-step-%d", i)
		coord := bitrate.Coordinator{
			Sampler: fixedSampler{sample: bitrate.Sample{
				TenantID:  tenant,
				UserID:    user,
				SessionID: "sess-longitudinal",
				SampleID:  sampleID,
				Text:      probe,
			}},
			Hydrator:    hydrator,
			Runner:      bitrate.Worker{BinaryPath: bin},
			Store:       &captureStore{}, // throwaway; durable write is below
			SampleBatch: 1,
		}
		ms, err := coord.RunOnce(ctx)
		if err != nil {
			t.Fatalf("coordinator RunOnce (step %d): %v", i, err)
		}
		if len(ms) != 1 {
			t.Fatalf("step %d: expected 1 measurement, got %d: %+v", i, len(ms), ms)
		}
		m := ms[0]

		// Persist to the DURABLE store with the injected, ordered timestamp and
		// the step's distinct sample_id.
		m.MeasuredAt = base.Add(time.Duration(i) * delta)
		if err := bStore.Save(ctx, m); err != nil {
			t.Fatalf("durable save (step %d): %v", i, err)
		}

		bptWith = append(bptWith, m.BptWith)
		bptWithout = append(bptWithout, m.BptWithout)
		t.Logf("step %d  memory=[facts:%d docs:%d]  bpt_with=%.4f  bpt_without=%.4f  lift=%.4f  (n_tok=%d model=%s)",
			i, factSeq, docSeq, m.BptWith, m.BptWithout, m.Lift, m.NTokens, m.ModelID)
	}

	// --- The KR8.1 downward-curve assertions. --------------------------------
	slope := leastSquaresSlope(bptWith)
	t.Logf("bpt_with sequence: %v", bptWith)
	t.Logf("bpt_without sequence (should be ~constant, bare persona): %v", bptWithout)
	t.Logf("least-squares slope over bpt_with = %.5f (want NEGATIVE = downward as memory accumulates)", slope)

	if bptWith[len(bptWith)-1] >= bptWith[0] {
		t.Fatalf("NO downward curve: bpt_with[last]=%.4f >= bpt_with[first]=%.4f",
			bptWith[len(bptWith)-1], bptWith[0])
	}
	if slope >= 0 {
		t.Fatalf("bpt_with slope is not negative: %.5f (memory accumulation did not lower surprisal)", slope)
	}

	// --- Prove the series PERSISTS and round-trips through the durable store. -
	series, err := bStore.Series(ctx, tenant, user)
	if err != nil {
		t.Fatalf("read series back: %v", err)
	}
	if len(series) != steps {
		t.Fatalf("series round-trip: expected %d measurements, got %d", steps, len(series))
	}
	readBack := make([]float64, len(series))
	for i, m := range series {
		readBack[i] = m.BptWith
		if i > 0 && !series[i].MeasuredAt.After(series[i-1].MeasuredAt) {
			t.Fatalf("series not strictly time-ordered at %d: %v <= %v",
				i, series[i].MeasuredAt, series[i-1].MeasuredAt)
		}
	}
	if got := leastSquaresSlope(readBack); got >= 0 {
		t.Fatalf("read-back bpt_with slope is not negative: %.5f", got)
	}
	t.Logf("STORE ROUND-TRIP OK: read back %d ordered measurements, bpt_with=%v", len(series), readBack)

	// The store's own TrendSlope is over LIFT (= bpt_without - bpt_with). Because
	// bpt_without is ~constant and bpt_with falls, lift RISES: the store's trend
	// is the POSITIVE mirror of the bpt_with downward curve — the same signal,
	// reported as the per-tenant success metric the ops /bitrate view surfaces.
	liftSlope, err := bStore.TrendSlope(ctx, tenant, user)
	if err != nil {
		t.Fatalf("store TrendSlope: %v", err)
	}
	t.Logf("Store.TrendSlope over LIFT = %.5f (POSITIVE mirror of the downward bpt_with curve)", liftSlope)

	latest, ok, err := bStore.Latest(ctx, tenant, user)
	if err != nil || !ok {
		t.Fatalf("Latest: ok=%v err=%v", ok, err)
	}
	if latest.SampleID != fmt.Sprintf("probe-step-%d", steps-1) {
		t.Fatalf("Latest is not the final step: got %q", latest.SampleID)
	}
	t.Logf("LATEST OK: %s bpt_with=%.4f measured_at=%s", latest.SampleID, latest.BptWith, latest.MeasuredAt)
}
