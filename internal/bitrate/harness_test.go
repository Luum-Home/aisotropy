package bitrate

// harness_test.go activates the Bet-6 bits/token MEASUREMENT PATH end-to-end
// with NO real model and NO Python/torch venv — the part that is runnable
// offline today (docs/research/bet-06-bits-per-token-metric.md).
//
// What is EXERCISED here (real code, real disk I/O, real arithmetic):
//   - Coordinator.RunOnce over a Hydrator whose two contexts are built the way
//     the request path builds them (memory-rich "with" vs bare-persona
//     "without");
//   - the NDJSON request contract (WriteNDJSON) the coordinator materializes for
//     the scorer subprocess, read back by the Runner exactly as the real Worker
//     reads it;
//   - the memory-LIFT computation (bpt_without − bpt_with) and its persistence
//     to the tenant-scoped Store, read back per (tenant, user).
//
// What is DELIBERATELY STUBBED (and cannot be real without a provider):
//   - the surprisal itself. A real bits/token needs a reference language model's
//     per-token logprobs (distilgpt2 in Phase 1, a Rust candle model in Phase 2),
//     invoked as a subprocess — see bitrate_manual_test.go (build tag `manual`,
//     AISOTROPY_BITRATE_BIN). Here surprisalRunner substitutes a DETERMINISTIC,
//     model-free estimator (a word is "cheap" when its conditioning context
//     already contains it, "expensive" otherwise). That estimator is a stand-in
//     for a model, NOT a measurement of one: the ABSOLUTE bits are meaningless.
//     Only the SIGN and DIRECTION of the lift — memory-rich context makes a
//     tenant's own text less surprising than bare persona — are what this harness
//     proves the pipeline can compute and persist. True per-tenant lift SCORING
//     (Class 2a) requires a real provider + eval and stays behind the `manual`
//     tag until then.

import (
	"context"
	"strings"
	"testing"
)

// harnessHydrator produces the two conditioning contexts the coordinator scores,
// the way the real internal/api Hydrator does but without any store: ContextWith
// returns a memory-rich context (persona + a "recalled" summary that echoes the
// sample's own subject matter, i.e. what tenant-scoped hydration would surface),
// and ContextWithout returns the bare persona baseline. The gap between them is
// exactly the signal Bet 6 measures.
type harnessHydrator struct {
	persona string
	// recall maps a sampleID to the memory text hydration would surface for it —
	// deliberately overlapping the sample's own text so a real model (and our
	// stand-in) would find that text less surprising with memory than without.
	recall map[string]string
}

func (h harnessHydrator) ContextWith(_ context.Context, _, _, _, _ /*productID*/, query string) (string, error) {
	// The real hydrator keys recall off tenant-scoped memory; here we key off the
	// query text so the harness stays a pure function of its inputs. We fold in
	// whichever recall snippet shares the most words with the query.
	best := ""
	bestHits := -1
	q := wordSet(query)
	for _, snippet := range h.recall {
		hits := 0
		for w := range wordSet(snippet) {
			if q[w] {
				hits++
			}
		}
		if hits > bestHits {
			bestHits, best = hits, snippet
		}
	}
	return h.persona + "\nRecalled context: " + best, nil
}

func (h harnessHydrator) ContextWithout(_ context.Context, _ /*productID*/ string) (string, error) {
	return h.persona, nil
}

// surprisalRunner is the model-free stand-in for the reference scorer. It reads
// the coordinator's NDJSON request file (via the same readRequests helper the
// fake runner uses) and assigns each token a surprisal that DEPENDS ON THE
// CONTEXT: a word already present in the conditioning context is "anticipated"
// (cheap), a word absent from it is "novel" (expensive). This reproduces the
// one property a real model has that makes the whole metric meaningful — richer
// context lowers surprisal — WITHOUT being a model. The bit values are a fixed
// two-level scale, not a probability estimate.
type surprisalRunner struct{}

const (
	// anticipatedBits ≈ cost of a token the context already predicts.
	anticipatedBits = 1.5
	// novelBits ≈ cost of an unpredicted token (~log2 of a small vocabulary).
	novelBits = 6.0
)

func (surprisalRunner) Score(_ context.Context, inputPath, _ string) ([]ScoreResult, error) {
	reqs, err := readRequests(inputPath)
	if err != nil {
		return nil, err
	}
	out := make([]ScoreResult, 0, len(reqs))
	for _, req := range reqs {
		bpt, total, n := scoreSurprisal(req.Text, req.Context)
		out = append(out, ScoreResult{
			ID:           req.ID,
			BitsTotal:    total,
			BitsPerToken: bpt,
			NTokens:      n,
			ModelID:      "surprisal-stand-in@harness",
			Unit:         "word",
		})
	}
	return out, nil
}

// scoreSurprisal is the deterministic estimator: total = Σ per-token bits,
// bpt = total/n. A token seen in context costs anticipatedBits, else novelBits.
func scoreSurprisal(text, ctx string) (bpt, total float64, n int) {
	ctxWords := wordSet(ctx)
	words := strings.Fields(text)
	if len(words) == 0 {
		return 0, 0, 0
	}
	for _, w := range words {
		if ctxWords[strings.ToLower(w)] {
			total += anticipatedBits
		} else {
			total += novelBits
		}
	}
	return total / float64(len(words)), total, len(words)
}

func wordSet(s string) map[string]bool {
	out := make(map[string]bool)
	for _, w := range strings.Fields(s) {
		out[strings.ToLower(w)] = true
	}
	return out
}

// TestBitrateMeasurementPathNoModel drives one full Coordinator pass with the
// model-free estimator and asserts the pipeline (a) computes a POSITIVE lift for
// every tenant whose memory anticipates its own text, (b) persists it under the
// correct (tenant, user) scope, and (c) does not leak one tenant's measurement
// to another. It proves the measurement PLUMBING is live offline; it does not —
// and cannot — validate a real bits/token number (see the file header).
func TestBitrateMeasurementPathNoModel(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	// Two tenants, each with a held-out sample and a recall snippet that
	// anticipates most of that sample's words (what real memory hydration would
	// surface). globex's recall overlaps its sample more tightly than acme's, so
	// globex should show the larger lift — a directional check, not a numeric one.
	samples := []Sample{
		{TenantID: "acme", UserID: "alice", SessionID: "s1", SampleID: "r1",
			Text: "the invoice total exceeded the monthly budget again"},
		{TenantID: "globex", UserID: "bob", SessionID: "s2", SampleID: "r2",
			Text: "deploy the payment service to the staging cluster"},
	}
	hy := harnessHydrator{
		persona: "You are a helpful assistant.",
		recall: map[string]string{
			// acme: partial overlap (missing "again", "exceeded").
			"r1": "prior turns discussed the invoice total and the monthly budget",
			// globex: near-total overlap.
			"r2": "earlier we agreed to deploy the payment service to the staging cluster",
		},
	}

	c := Coordinator{
		Sampler:  fakeSampler{samples: samples},
		Hydrator: hy,
		Runner:   surprisalRunner{},
		Store:    store,
		Dir:      t.TempDir(),
	}

	ms, err := c.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(ms) != 2 {
		t.Fatalf("measurements = %d, want 2", len(ms))
	}

	byTenant := map[string]Measurement{}
	for _, m := range ms {
		byTenant[m.TenantID] = m
	}

	// (a) Positive lift: memory-rich context is less surprising than bare persona.
	for tenant, m := range byTenant {
		if m.Lift <= 0 {
			t.Errorf("%s lift = %.3f, want > 0 (memory must reduce surprisal)", tenant, m.Lift)
		}
		if m.BptWithout <= m.BptWith {
			t.Errorf("%s bpt_without (%.3f) must exceed bpt_with (%.3f)", tenant, m.BptWithout, m.BptWith)
		}
		t.Logf("%s: bpt_with=%.3f bpt_without=%.3f lift=%.3f model=%s",
			tenant, m.BptWith, m.BptWithout, m.Lift, m.ModelID)
	}

	// Directional: globex's tighter recall yields a larger lift than acme's.
	if byTenant["globex"].Lift <= byTenant["acme"].Lift {
		t.Errorf("expected globex lift (%.3f) > acme lift (%.3f): tighter memory overlap should help more",
			byTenant["globex"].Lift, byTenant["acme"].Lift)
	}

	// (b) Persisted and (c) scoped: each identity reads back only its own.
	acme, ok, err := store.Latest(ctx, "acme", "alice")
	if err != nil || !ok {
		t.Fatalf("Latest(acme,alice): ok=%v err=%v", ok, err)
	}
	if acme.SampleID != "r1" {
		t.Errorf("acme latest sample = %q, want r1", acme.SampleID)
	}
	if _, ok, _ := store.Latest(ctx, "acme", "bob"); ok {
		t.Error("cross-identity (acme,bob) must have no measurement — isolation leak")
	}
	if _, ok, _ := store.Latest(ctx, "globex", "alice"); ok {
		t.Error("cross-identity (globex,alice) must have no measurement — isolation leak")
	}
}

// BenchmarkBitrateRunOncePass measures one full model-free measurement pass
// (hydrate both contexts → WriteNDJSON → estimator scores → lift → persist) over
// a small batch, so the coordinator/plumbing overhead is visible independently
// of any real scorer's model latency. It is NOT a measurement of scoring cost —
// a real reference model dominates that by orders of magnitude.
func BenchmarkBitrateRunOncePass(b *testing.B) {
	ctx := context.Background()
	samples := []Sample{
		{TenantID: "acme", UserID: "alice", SessionID: "s1", SampleID: "r1", Text: "the invoice total exceeded the monthly budget"},
		{TenantID: "globex", UserID: "bob", SessionID: "s2", SampleID: "r2", Text: "deploy the payment service to staging"},
	}
	hy := harnessHydrator{
		persona: "You are a helpful assistant.",
		recall: map[string]string{
			"r1": "prior turns discussed the invoice total and the monthly budget",
			"r2": "earlier we agreed to deploy the payment service to staging",
		},
	}
	dir := b.TempDir()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		c := Coordinator{
			Sampler:  fakeSampler{samples: samples},
			Hydrator: hy,
			Runner:   surprisalRunner{},
			Store:    noopMeasurementStore{},
			Dir:      dir,
		}
		if _, err := c.RunOnce(ctx); err != nil {
			b.Fatalf("RunOnce: %v", err)
		}
	}
}

// noopMeasurementStore is a persistence sink for the benchmark so it measures the
// coordinator/estimator path, not the store's write cost (covered by store_test).
type noopMeasurementStore struct{}

func (noopMeasurementStore) Save(context.Context, Measurement) error { return nil }
