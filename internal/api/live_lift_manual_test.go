//go:build manual

// End-to-end validation that Bet 6's bits-per-token instrumentation yields a
// REAL positive memory lift through the FULL runtime path — not just the
// standalone golden-input scorer. Behind the `manual` build tag, so it is
// EXCLUDED from `go test ./...` and CI: it needs a Python venv with torch and
// the scorer wrapper.
//
// This is a SEEDED demonstration (there is no live production tenant traffic):
// it seeds a tenant's consolidated memory (facts + semantic), reserves a
// held-out sample whose text relates to those facts but is NOT itself one of
// them (so the lift is real, not leakage), then drives the REAL runtime path —
// bitrate.Coordinator -> BitrateHydrator (the exact request-path
// hydrateSystemPrompt code) -> bitrate.Worker (the real Python scorer
// subprocess) -> bitrate.Store — and asserts a positive lift
// (bpt_with < bpt_without) that round-trips through the store.
//
// HONESTY: this proves the MECHANISM produces a real runtime lift on seeded
// consolidated memory. It does NOT prove the "downward trend over time on live
// tenant traffic" half of KR8.1 — that needs a real longitudinal deployment.
//
// Point AISOTROPY_BITRATE_BIN at an executable that forwards --input/--output
// to the scorer, e.g. a wrapper:
//
//	#!/bin/bash
//	exec /abs/path/spikes/compression/.venv/bin/python \
//	     /abs/path/spikes/compression/bitrate_scorer.py "$@"
//
// then run:
//
//	AISOTROPY_BITRATE_BIN=/abs/path/wrapper.sh \
//	  go test -tags manual -run TestLiveMemoryLiftEndToEnd -v ./internal/api
package api

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/bitrate"
	"github.com/Luum-Home/aisotropy/internal/facts"
	"github.com/Luum-Home/aisotropy/internal/semantic"
)

func TestLiveMemoryLiftEndToEnd(t *testing.T) {
	bin := os.Getenv("AISOTROPY_BITRATE_BIN")
	if bin == "" {
		t.Skip("AISOTROPY_BITRATE_BIN unset; set it to a scorer wrapper to run the live end-to-end lift test")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("scorer binary %q not found: %v", bin, err)
	}

	ctx := context.Background()
	const (
		tenant   = "acme"
		user     = "alice"
		session  = "sess-finance-1"
		sampleID = "held-out-finance-sync"
	)

	// The held-out sample: a plausible NEXT user turn on the seeded topic. It is
	// ABOUT the finance team / weekly sync (so consolidated memory should reduce
	// its surprisal) but is NOT itself one of the seeded facts (so the lift is
	// real signal, not the scorer reading back a fact it was handed verbatim).
	const heldOut = "When is the finance team weekly sync?"

	// --- 1. Facts tier: seed CONSOLIDATED facts about a specific topic. ------
	factsStore, err := facts.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open facts store: %v", err)
	}
	defer func() { _ = factsStore.Close() }()

	scope, err := facts.NewScope(tenant, user)
	if err != nil {
		t.Fatalf("facts scope: %v", err)
	}
	seeded := []string{
		"Alice manages the finance team at Acme.",
		"The finance team weekly sync is on Tuesdays at 3pm.",
		"The budget review happens quarterly and is led by Alice.",
		"Alice's finance team tracks the quarterly budget in a shared spreadsheet.",
	}
	for i, content := range seeded {
		f := facts.Fact{
			ID:         fmt.Sprintf("fact-%d", i),
			TenantID:   tenant,
			UserID:     user,
			Content:    content,
			Importance: 0.9,
		}
		if err := factsStore.Save(ctx, scope, f); err != nil {
			t.Fatalf("seed fact %d: %v", i, err)
		}
	}

	// --- 2. Semantic tier: seed a curated KindDocument point on the topic. ---
	// hydrateSystemPrompt's semantic search targets KindDocument + KindFact, so
	// a KindDocument point participates in the exact request-path retrieval.
	embedder := semantic.NewHashEmbedder(128)
	semStore := semantic.NewInMemoryStore(embedder.Dimension())
	defer func() { _ = semStore.Close() }()

	semScope, err := semantic.NewScope(tenant, user)
	if err != nil {
		t.Fatalf("semantic scope: %v", err)
	}
	doc := "Acme finance team operations: the weekly sync meeting is held every " +
		"Tuesday at 3pm, and the quarterly budget review is led by Alice."
	vecs, err := embedder.Embed(ctx, []string{doc})
	if err != nil || len(vecs) != 1 {
		t.Fatalf("embed doc: %v (n=%d)", err, len(vecs))
	}
	point := semantic.Point{
		ID:       semantic.PointID(semScope, semantic.KindDocument, "doc-finance"),
		TenantID: tenant,
		UserID:   user,
		Kind:     semantic.KindDocument,
		Content:  doc,
		Source:   "doc-finance",
		Vector:   vecs[0],
	}
	if err := semStore.Upsert(ctx, semScope, []semantic.Point{point}); err != nil {
		t.Fatalf("seed semantic point: %v", err)
	}

	// --- 3. Bitrate store: reserve the held-out sample. ----------------------
	bStore, err := bitrate.OpenStore(":memory:")
	if err != nil {
		t.Fatalf("open bitrate store: %v", err)
	}
	defer func() { _ = bStore.Close() }()
	if err := bStore.Reserve(ctx, tenant, user, session, sampleID, heldOut); err != nil {
		t.Fatalf("reserve held-out sample: %v", err)
	}

	// --- 4. Sanity: the REAL hydrator must fold the seeded memory into the ----
	//        "with" context, and leave "without" as the bare persona. This
	//        proves the two contexts differ by exactly the consolidated memory.
	hydrator := BitrateHydrator{Store: factsStore, Semantic: semStore, Embedder: embedder}
	ctxWith, err := hydrator.ContextWith(ctx, tenant, user, session, "", heldOut)
	if err != nil {
		t.Fatalf("ContextWith: %v", err)
	}
	ctxWithout, err := hydrator.ContextWithout(ctx, "")
	if err != nil {
		t.Fatalf("ContextWithout: %v", err)
	}
	if !strings.Contains(ctxWith, "finance team weekly sync") {
		t.Fatalf("hydrated context_with did not fold in the seeded facts:\n%s", ctxWith)
	}
	if strings.Contains(ctxWithout, "finance") {
		t.Fatalf("context_without leaked memory (should be bare persona):\n%s", ctxWithout)
	}
	t.Logf("context_with (%d chars):\n%s\n---\ncontext_without (%d chars):\n%s",
		len(ctxWith), ctxWith, len(ctxWithout), ctxWithout)

	// --- 5. Drive the REAL coordinator: Sampler -> Hydrator -> Worker -> Store.
	coord := bitrate.Coordinator{
		Sampler:     bStore,
		Hydrator:    hydrator,
		Runner:      bitrate.Worker{BinaryPath: bin},
		Store:       bStore,
		SampleBatch: 8,
	}
	measurements, err := coord.RunOnce(ctx)
	if err != nil {
		t.Fatalf("coordinator RunOnce: %v", err)
	}
	if len(measurements) != 1 {
		t.Fatalf("expected exactly 1 measurement, got %d: %+v", len(measurements), measurements)
	}
	m := measurements[0]
	t.Logf("LIVE MEMORY LIFT: bpt_with=%.4f  bpt_without=%.4f  lift=%.4f bits/tok  (model=%s, n_tokens=%d)",
		m.BptWith, m.BptWithout, m.Lift, m.ModelID, m.NTokens)

	// --- 6. Assert a REAL positive lift. -------------------------------------
	if m.BptWith >= m.BptWithout {
		t.Fatalf("NO positive lift: bpt_with=%.4f >= bpt_without=%.4f (consolidated memory did not reduce surprisal)",
			m.BptWith, m.BptWithout)
	}
	if m.Lift <= 0 {
		t.Fatalf("lift is not positive: %.4f", m.Lift)
	}

	// --- 7. Prove the FULL persistence path: read the measurement back. ------
	got, ok, err := bStore.Latest(ctx, tenant, user)
	if err != nil {
		t.Fatalf("read measurement back: %v", err)
	}
	if !ok {
		t.Fatal("measurement did not persist to the store")
	}
	if got.SampleID != sampleID {
		t.Fatalf("round-trip sample_id mismatch: got %q want %q", got.SampleID, sampleID)
	}
	if got.Lift != m.Lift || got.BptWith != m.BptWith || got.BptWithout != m.BptWithout {
		t.Fatalf("round-trip measurement mismatch: stored %+v vs computed %+v", got, m)
	}
	t.Logf("STORE ROUND-TRIP OK: read back lift=%.4f bpt_with=%.4f bpt_without=%.4f sample=%q model=%s",
		got.Lift, got.BptWith, got.BptWithout, got.SampleID, got.ModelID)
}
