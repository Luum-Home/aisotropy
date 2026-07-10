package bitrate

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// fakeSampler returns a fixed held-out set, ignoring the limit.
type fakeSampler struct{ samples []Sample }

func (f fakeSampler) HeldOut(_ context.Context, _ int) ([]Sample, error) { return f.samples, nil }

// fakeHydrator returns deterministic, distinct contexts so a leak between the
// with/without branches (or between tenants) would show up.
type fakeHydrator struct{ err error }

func (h fakeHydrator) ContextWith(_ context.Context, tenantID, userID, sessionID, productID, query string) (string, error) {
	if h.err != nil {
		return "", h.err
	}
	return "WITH:" + tenantID + ":" + userID + ":" + query, nil
}

func (h fakeHydrator) ContextWithout(_ context.Context, productID string) (string, error) {
	if h.err != nil {
		return "", h.err
	}
	return "BASE", nil
}

// fakeRunner returns a fixed bits/token per request id: 1.0 for a "_with" row,
// higher for a "_without" row, so the memory lift is positive and exact. It
// reads the request file only to echo back the ids that were actually sent.
type fakeRunner struct {
	withBPT    float64
	withoutBPT map[string]float64 // sampleID → without bpt
}

func (r fakeRunner) Score(_ context.Context, inputPath, _ string) ([]ScoreResult, error) {
	reqs, err := readRequests(inputPath)
	if err != nil {
		return nil, err
	}
	var out []ScoreResult
	for _, req := range reqs {
		bpt := r.withBPT
		if without, ok := trimSuffix(req.ID, "_without"); ok {
			bpt = r.withoutBPT[without]
		}
		out = append(out, ScoreResult{
			ID: req.ID, BitsTotal: bpt * 4, BitsPerToken: bpt, NTokens: 4,
			ModelID: "fake@pinned", Unit: "word",
		})
	}
	return out, nil
}

func TestCoordinatorComputesLiftSavesAndIsolates(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	samples := []Sample{
		{TenantID: "acme", UserID: "alice", SessionID: "s1", SampleID: "r1", Text: "hola"},
		{TenantID: "globex", UserID: "bob", SessionID: "s2", SampleID: "r2", Text: "buenas"},
	}
	runner := fakeRunner{withBPT: 1.0, withoutBPT: map[string]float64{"r1": 3.0, "r2": 4.5}}

	c := Coordinator{
		Sampler:  fakeSampler{samples: samples},
		Hydrator: fakeHydrator{},
		Runner:   runner,
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
	// lift = bpt_without - bpt_with. r1: 3.0-1.0=2.0; r2: 4.5-1.0=3.5.
	if got := byTenant["acme"].Lift; got != 2.0 {
		t.Fatalf("acme lift = %v, want 2.0", got)
	}
	if got := byTenant["globex"].Lift; got != 3.5 {
		t.Fatalf("globex lift = %v, want 3.5", got)
	}

	// Persisted and scoped: each tenant reads back its own measurement only.
	acme, ok, err := store.Latest(ctx, "acme", "alice")
	if err != nil || !ok {
		t.Fatalf("Latest acme: ok=%v err=%v", ok, err)
	}
	if acme.Lift != 2.0 || acme.BptWith != 1.0 || acme.BptWithout != 3.0 {
		t.Fatalf("acme measurement = %+v", acme)
	}
	// Isolation: acme/alice never sees globex/bob's stronger lift.
	if _, ok, _ := store.Latest(ctx, "acme", "bob"); ok {
		t.Fatal("cross-identity (acme,bob) must have no measurement")
	}
}

func TestCoordinatorNoSamplesIsNoOp(t *testing.T) {
	c := Coordinator{
		Sampler:  fakeSampler{samples: nil},
		Hydrator: fakeHydrator{},
		Runner:   fakeRunner{withBPT: 1.0},
		Store:    openTestStore(t),
	}
	ms, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(ms) != 0 {
		t.Fatalf("expected no measurements, got %d", len(ms))
	}
}

func TestCoordinatorSkipsUnhydratableSamples(t *testing.T) {
	// Every hydration fails → no requests, no measurements, but no error either
	// (a scheduled job stays robust to a bad session).
	c := Coordinator{
		Sampler:  fakeSampler{samples: []Sample{{TenantID: "acme", UserID: "alice", SampleID: "r1", Text: "x"}}},
		Hydrator: fakeHydrator{err: errors.New("boom")},
		Runner:   fakeRunner{withBPT: 1.0},
		Store:    openTestStore(t),
	}
	ms, err := c.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce should not fail the batch on hydration error: %v", err)
	}
	if len(ms) != 0 {
		t.Fatalf("expected no measurements, got %d", len(ms))
	}
}

// readRequests parses an NDJSON request file back into ScoreRequests — a
// test-only inverse of WriteNDJSON so the fake runner can echo real ids.
func readRequests(path string) ([]ScoreRequest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []ScoreRequest
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var req ScoreRequest
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, sc.Err()
}

func trimSuffix(s, suffix string) (string, bool) {
	if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
		return s[:len(s)-len(suffix)], true
	}
	return s, false
}
