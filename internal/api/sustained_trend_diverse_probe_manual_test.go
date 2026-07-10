//go:build manual

// A STRONGER longitudinal simulation for Bet 6 (KR8.1) that fixes a real
// limitation of TestLongitudinalDownwardTrend (longitudinal_trend_manual_test.go).
//
// THE LIMITATION IT ADDRESSES (stated honestly): the existing longitudinal test
// grows memory over steps but measures bpt over a FIXED, NARROW probe — a SINGLE
// held-out turn about ONE topic. A one-topic probe SATURATES the moment its one
// relevant fact is present: the bpt drop is almost entirely the empty->first-fact
// ANCHOR (step 0->1), and steps 1..N are a near-plateau (often non-monotone
// scorer noise). That proves the MECHANISM (memory lowers surprisal) but NOT
// SUSTAINED gradual learning — you cannot tell an anchor jump from a genuine
// downward curve with a probe that can only be "covered" or "not covered" once.
//
// THE STRONGER DESIGN — a DIVERSE probe over multiple topics:
//  1. A FIXED, DIVERSE probe set of K held-out user turns, each about a DISTINCT
//     topic of one tenant's world (finance-sync, HR-policy, travel-booking,
//     product-roadmap, legal-review; K=5). This probe set is CONSTANT across all
//     steps — the invariant that makes the per-step aggregate comparable.
//  2. Over K steps, consolidated memory (facts + semantic docs) grows to cover
//     ONE MORE topic per step: at step i, memory covers topics 0..i. There is NO
//     empty starting step, so there is NO anchor artifact — every step is the
//     same kind of "+1 topic" increment. No probe turn is ever seeded verbatim
//     (leakage guard): memory is ABOUT a topic, the probe ASKS about it.
//  3. At each step, aggregate bpt_with = MEAN bpt over the FULL fixed K-turn probe,
//     measured through the REAL runtime path — bitrate.Coordinator ->
//     BitrateHydrator (the exact hydrateSystemPrompt code, nil working memory so
//     only consolidated memory contributes) -> bitrate.Worker (real Python scorer
//     subprocess) -> aggregated here. Aggregate bpt_without (bare persona) is the
//     control and stays ~constant.
//
// WHY THIS DESCENDS GRADUALLY, NOT BY ANCHOR: the aggregate is a MEAN over K
// turns. At step i exactly i+1 of the K turns are "covered" (their topic is in
// memory) and get low bpt; the rest stay high. Each step flips exactly ONE more
// turn from high->low, so the mean falls by ~(high-low)/K PER STEP — a roughly
// linear descent across ALL steps, not a one-shot saturation. The SUSTAINED
// metric below measures exactly that: how much of the total drop happens AFTER
// the first step.
//
// HONESTY: this is STILL a CONTROLLED SIMULATION, not real longitudinal
// deployment traffic. There is no production tenant emitting turns over weeks;
// the accumulation schedule is authored, so the descent's shape reflects that
// schedule. It validates that the metric captures SUSTAINED coverage growth (the
// property the narrow probe could not show) and that the series persists/round-
// trips. It is NOT field behavior. If the diverse probe ALSO plateaus, this test
// is written to REPORT that (it prints the sustained metric and asserts only what
// the numbers support) rather than force a green.
//
// Prereqs are identical to live_lift_manual_test.go / longitudinal_trend_manual_
// test.go: a Python venv with torch + the scorer wrapper. Behind `manual`, so it
// is EXCLUDED from `go test ./...` and CI. Run:
//
//	AISOTROPY_BITRATE_BIN=/abs/path/wrapper.sh \
//	  go test -tags manual -run TestSustainedTrendDiverseProbe -v ./internal/api
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

// diverseProbeSampler is a bitrate.Sampler that returns the WHOLE fixed diverse
// probe set (K held-out turns spanning K topics) in one HeldOut call, so a single
// coordinator RunOnce scores every probe turn against the step's memory. The
// probe TEXTS are constant across steps (the comparability invariant); only the
// per-step SampleIDs advance so the scorer's _with/_without pairing is unique
// within a run. Unlike fixedSampler (one sample), this drives the AGGREGATE.
type diverseProbeSampler struct{ samples []bitrate.Sample }

func (d diverseProbeSampler) HeldOut(_ context.Context, _ int) ([]bitrate.Sample, error) {
	return d.samples, nil
}

// mean returns the arithmetic mean of xs (0 for an empty slice).
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

func TestSustainedTrendDiverseProbe(t *testing.T) {
	bin := os.Getenv("AISOTROPY_BITRATE_BIN")
	if bin == "" {
		t.Skip("AISOTROPY_BITRATE_BIN unset; set it to a scorer wrapper to run the sustained diverse-probe trend test")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("scorer binary %q not found: %v", bin, err)
	}

	ctx := context.Background()
	const (
		tenant  = "acme"
		user    = "alice"
		session = "sess-diverse-longitudinal"
	)

	// --- The FIXED, DIVERSE held-out probe set (K distinct topics). ----------
	// Each is a QUESTION about one topic of the tenant's world. CONSTANT across
	// all steps. None is ever seeded verbatim into memory (leakage guard in
	// seedTopic). topicProbe[j] is answered by topicMemory[j] below.
	topicProbe := []string{
		"When is the finance team weekly sync scheduled?",                       // 0 finance-sync
		"How many vacation days does the HR policy grant new engineers?",        // 1 HR-policy
		"Which airline is preferred when booking company travel?",               // 2 travel-booking
		"What is the next milestone on the product roadmap?",                    // 3 product-roadmap
		"Who must approve the legal review before a vendor contract is signed?", // 4 legal-review
	}
	K := len(topicProbe)

	// The per-topic consolidated memory revealed one topic at a time. topicFacts[j]
	// and topicDocs[j] are ABOUT topic j and answer topicProbe[j] WITHOUT restating
	// it verbatim. Keyword-distinct across topics so FTS5 / hash-embedding
	// retrieval routes each probe to its own topic's memory.
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

	// --- Consolidated memory stores (grown one topic per step). --------------
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

	// --- Durable bpt store: one AGGREGATE row per step; read back at the end. -
	bStore, err := bitrate.OpenStore(":memory:")
	if err != nil {
		t.Fatalf("open bitrate store: %v", err)
	}
	defer func() { _ = bStore.Close() }()

	hydrator := BitrateHydrator{Store: factsStore, Semantic: semStore, Embedder: embedder}

	// seedTopic folds topic j's fact + doc into consolidated memory, guarding
	// against leakage (no seed may equal any probe turn verbatim).
	seedTopic := func(j int) {
		for _, probe := range topicProbe {
			if strings.EqualFold(strings.TrimSpace(topicFacts[j]), strings.TrimSpace(probe)) ||
				strings.EqualFold(strings.TrimSpace(topicDocs[j]), strings.TrimSpace(probe)) {
				t.Fatalf("LEAKAGE: topic %d memory equals a probe turn verbatim", j)
			}
		}
		f := facts.Fact{
			ID:         fmt.Sprintf("fact-topic-%d", j),
			TenantID:   tenant,
			UserID:     user,
			Content:    topicFacts[j],
			Importance: 0.9,
		}
		if err := factsStore.Save(ctx, factsScope, f); err != nil {
			t.Fatalf("seed fact (topic %d): %v", j, err)
		}
		vecs, err := embedder.Embed(ctx, []string{topicDocs[j]})
		if err != nil || len(vecs) != 1 {
			t.Fatalf("embed doc (topic %d): %v (n=%d)", j, err, len(vecs))
		}
		src := fmt.Sprintf("doc-topic-%d", j)
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
			t.Fatalf("seed semantic point (topic %d): %v", j, err)
		}
	}

	// A deterministic, injected timeline: base + i*delta (one simulated week per
	// step). NOT time.Now() — the durable series must be ordered by step.
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	const delta = 7 * 24 * time.Hour

	aggWith := make([]float64, 0, K)    // per-step MEAN bpt_with over the K-turn probe
	aggWithout := make([]float64, 0, K) // per-step MEAN bpt_without (control; ~constant)

	// K steps: at step i, memory covers topics 0..i (one more topic per step).
	for i := 0; i < K; i++ {
		seedTopic(i) // reveal topic i BEFORE measuring this step

		// Build the FULL fixed diverse probe for this step. Same K texts every
		// step; per-step unique SampleIDs.
		samples := make([]bitrate.Sample, K)
		for j := 0; j < K; j++ {
			samples[j] = bitrate.Sample{
				TenantID:  tenant,
				UserID:    user,
				SessionID: session,
				SampleID:  fmt.Sprintf("probe-t%d-s%d", j, i),
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
			t.Fatalf("coordinator RunOnce (step %d): %v", i, err)
		}
		if len(ms) != K {
			t.Fatalf("step %d: expected %d measurements over the diverse probe, got %d", i, K, len(ms))
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

		// Persist ONE aggregate row per step to the DURABLE store with the
		// injected, ordered timestamp and a per-step distinct sample_id.
		agg := bitrate.Measurement{
			TenantID:   tenant,
			UserID:     user,
			MeasuredAt: base.Add(time.Duration(i) * delta),
			BptWith:    meanWith,
			BptWithout: meanWithout,
			Lift:       meanWithout - meanWith,
			NTokens:    nTokTotal,
			ModelID:    modelID,
			SampleID:   fmt.Sprintf("diverse-agg-step-%d", i),
		}
		if err := bStore.Save(ctx, agg); err != nil {
			t.Fatalf("durable save (step %d): %v", i, err)
		}

		t.Logf("step %d  topics_covered=%d/%d  agg_bpt_with=%.4f  agg_bpt_without=%.4f  agg_lift=%.4f  per_turn_with=%v",
			i, i+1, K, meanWith, meanWithout, meanWithout-meanWith, roundAll(withs))
	}

	// --- Headline KR8.1 downward-curve assertions (over the AGGREGATE). -------
	slope := leastSquaresSlope(aggWith)
	totalDrop := aggWith[0] - aggWith[K-1]
	t.Logf("AGG bpt_with sequence:    %v", roundAll(aggWith))
	t.Logf("AGG bpt_without sequence: %v  (control; should be ~constant)", roundAll(aggWithout))
	t.Logf("least-squares slope over AGG bpt_with = %.5f (want NEGATIVE)", slope)
	t.Logf("total drop bpt_with[0]-bpt_with[last] = %.5f", totalDrop)

	if aggWith[K-1] >= aggWith[0] {
		t.Fatalf("NO downward curve: agg bpt_with[last]=%.4f >= [first]=%.4f", aggWith[K-1], aggWith[0])
	}
	if slope >= 0 {
		t.Fatalf("agg bpt_with slope is not negative: %.5f", slope)
	}

	// --- SUSTAINED-descent metric: the whole point vs. the narrow-probe anchor.
	// Two complementary measures, both printed:
	//
	//  (A) postAnchorFraction = (bpt_with[1] - bpt_with[last]) / totalDrop.
	//      The share of the TOTAL descent that happens AFTER the first step. For a
	//      pure anchor artifact (all drop in step 0->1) this is ~0. For a genuine
	//      sustained descent it is a large fraction. GATE: >= sustainedFloor.
	//
	//  (B) monotone-non-increasing within tolerance: every later step is <= the
	//      previous plus a small epsilon (epsilon = monoTolFrac * |totalDrop|),
	//      i.e. no step RISES by more than a small fraction of the whole drop.
	//      This proves the descent is a curve, not one cliff plus noise.
	//
	// Thresholds are deliberately conservative so a PASS is meaningful but the
	// test is not gamed: with K=5 diverse topics and linear coverage the ideal
	// postAnchorFraction is ~ (K-2)/(K-1) = 0.75; sustainedFloor=0.30 leaves wide
	// margin for scorer noise while decisively excluding an anchor-only artifact
	// (which sits near 0).
	const (
		sustainedFloor = 0.30 // >= 30% of the drop must occur after step 0->1
		monoTolFrac    = 0.25 // a step may rise by at most 25% of |totalDrop|
	)
	firstStepDrop := aggWith[0] - aggWith[1]
	postAnchorDrop := aggWith[1] - aggWith[K-1]
	postAnchorFraction := postAnchorDrop / totalDrop

	// Monotonicity within tolerance.
	epsilon := monoTolFrac * totalDrop // totalDrop > 0 here (asserted above)
	maxRise := 0.0
	monotone := true
	for i := 1; i < K; i++ {
		rise := aggWith[i] - aggWith[i-1] // >0 means it went UP (bad)
		if rise > maxRise {
			maxRise = rise
		}
		if rise > epsilon {
			monotone = false
		}
	}

	t.Logf("SUSTAINED-DESCENT METRIC:")
	t.Logf("  first-step drop (0->1)       = %.5f", firstStepDrop)
	t.Logf("  post-anchor drop (1->last)   = %.5f", postAnchorDrop)
	t.Logf("  postAnchorFraction           = %.4f  (gate >= %.2f; anchor-artifact ~0, ideal ~0.75)",
		postAnchorFraction, sustainedFloor)
	t.Logf("  max per-step RISE            = %.5f  (tolerance epsilon = %.5f)", maxRise, epsilon)
	t.Logf("  monotone-non-increasing?     = %v", monotone)

	if postAnchorFraction < sustainedFloor {
		t.Fatalf("NOT SUSTAINED (plateau/anchor artifact): postAnchorFraction=%.4f < %.2f — "+
			"the descent is dominated by the first step, exactly the narrow-probe failure mode. "+
			"HONEST NEGATIVE FINDING: even a diverse probe saturates under this schedule.",
			postAnchorFraction, sustainedFloor)
	}
	if !monotone {
		t.Fatalf("NOT a smooth sustained curve: a step rose by %.5f > epsilon %.5f — "+
			"the aggregate is not monotone-non-increasing within tolerance.", maxRise, epsilon)
	}

	// --- Prove the AGGREGATE series persists and round-trips. ----------------
	series, err := bStore.Series(ctx, tenant, user)
	if err != nil {
		t.Fatalf("read series back: %v", err)
	}
	if len(series) != K {
		t.Fatalf("series round-trip: expected %d aggregate rows, got %d", K, len(series))
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
		t.Fatalf("read-back agg bpt_with slope is not negative: %.5f", got)
	}
	t.Logf("STORE ROUND-TRIP OK: read back %d ordered aggregate rows, bpt_with=%v", len(series), roundAll(readBack))

	// The store's TrendSlope is over LIFT (bpt_without - bpt_with). With a
	// ~constant control and a falling bpt_with, lift RISES: the positive mirror
	// of the downward aggregate curve.
	liftSlope, err := bStore.TrendSlope(ctx, tenant, user)
	if err != nil {
		t.Fatalf("store TrendSlope: %v", err)
	}
	t.Logf("Store.TrendSlope over LIFT = %.5f (POSITIVE mirror of the downward agg bpt_with curve)", liftSlope)

	latest, ok, err := bStore.Latest(ctx, tenant, user)
	if err != nil || !ok {
		t.Fatalf("Latest: ok=%v err=%v", ok, err)
	}
	if latest.SampleID != fmt.Sprintf("diverse-agg-step-%d", K-1) {
		t.Fatalf("Latest is not the final step: got %q", latest.SampleID)
	}
	t.Logf("LATEST OK: %s agg_bpt_with=%.4f measured_at=%s", latest.SampleID, latest.BptWith, latest.MeasuredAt)
}

// roundAll rounds each value to 4 decimals for readable log lines (display only;
// never used in an assertion).
func roundAll(xs []float64) []float64 {
	out := make([]float64, len(xs))
	for i, x := range xs {
		out[i] = float64(int64(x*10000+0.5)) / 10000
	}
	return out
}
