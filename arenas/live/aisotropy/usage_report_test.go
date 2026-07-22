package main

import (
	"context"
	"testing"
	"time"

	aisotropy "github.com/Luum-Home/aisotropy/aisotropy"
)

// TestSetUsage_StubLanePinsMinusOne pins the stub lane's token semantics
// against a REAL, observed hazard (not a hypothetical): under ARENA_STUB=1 the
// App answers with aisotropy's native keyless StubClient, which attaches a
// DETERMINISTIC FAKE Usage to its terminal token (internal/llm/stub.go's
// stubUsageFor — deliberately non-zero so the product's own OTel/cost wiring
// can be tested). Since commit be945de that fake usage traverses the product's
// base-client usage meter, so App.Usage() in a stub run returns non-zero
// numbers. Reported as-is they would render as real-looking token counts on a
// stub board — a lane-honesty violation (.claude/rules/arena-full-matrix.md:
// stub tokens are provider-reported or -1, NEVER fabricated).
//
// The test drives one genuine offline stub turn (zero network), proves the
// meter really did observe the fake usage (the precondition that makes the
// gate load-bearing rather than vacuous), and asserts setUsage still reports
// -1/-1.
func TestSetUsage_StubLanePinsMinusOne(t *testing.T) {
	t.Setenv("ARENA_STUB", "1")
	t.Setenv("AISOTROPY_DATA_DIR", "") // in-memory working memory for this test

	agnes, err := newAgnesClient() // (nil, nil) + clears provider/backend env
	if err != nil {
		t.Fatalf("newAgnesClient in stub mode: %v", err)
	}
	app := newApp(agnes, "You are a helpful test assistant.")
	defer func() { _ = app.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := app.Turn(ctx, aisotropy.ChatInput{
		TenantID:  arenaTenant,
		UserID:    arenaUser,
		Prompt:    "What is the capital of France?",
		SessionID: "stub-usage-pin",
		ProductID: productID,
	}); err != nil {
		t.Fatalf("offline stub App.Turn: %v", err)
	}

	// Precondition: the product meter DID observe the stub's fabricated usage.
	// If this ever reads zero, the trap this test pins no longer exists and the
	// test (plus setUsage's stub gate rationale) should be revisited — fail
	// loudly rather than pass vacuously.
	if u := app.Usage(); u.InputTokens == 0 && u.OutputTokens == 0 {
		t.Fatalf("precondition failed: App.Usage() is zero after a stub turn — the stub client no longer reports fake usage into the meter, so this pin (and setUsage's stub gate comment) needs revisiting")
	}

	out := &result{}
	setUsage(out, app)
	if out.InputTokens != -1 || out.OutputTokens != -1 {
		t.Fatalf("stub lane reported tokens %d/%d, want -1/-1 — App.Usage()'s stub-fabricated numbers leaked into the arena output (lane-honesty violation: stub tokens are provider-reported or -1, never fabricated)", out.InputTokens, out.OutputTokens)
	}
}

// TestSetUsage_RealLaneZeroMeterMeansAbsent pins the real lane's "honest
// absence" mapping: an App whose meter observed nothing (no provider call ever
// reported a usage block — e.g. the call failed before any terminal token)
// reports -1/-1, exactly as the deleted decorator's sawUsage flag did. No
// model call is made: App.Usage() is boot-free and the App is never driven.
func TestSetUsage_RealLaneZeroMeterMeansAbsent(t *testing.T) {
	t.Setenv("ARENA_STUB", "") // real lane: the stub gate must NOT be what maps to -1 here

	app := aisotropy.New() // fresh App, meter observed nothing
	out := &result{}
	setUsage(out, app)
	if out.InputTokens != -1 || out.OutputTokens != -1 {
		t.Fatalf("zero-meter App reported tokens %d/%d, want -1/-1 (a provider never genuinely reports 0/0; zero means the provider never reported usage)", out.InputTokens, out.OutputTokens)
	}
}

// TestSetUsage_NilAppIsDefensiveMinusOne pins the defensive nil branch.
func TestSetUsage_NilAppIsDefensiveMinusOne(t *testing.T) {
	t.Setenv("ARENA_STUB", "")

	out := &result{}
	setUsage(out, nil)
	if out.InputTokens != -1 || out.OutputTokens != -1 {
		t.Fatalf("nil App reported tokens %d/%d, want -1/-1", out.InputTokens, out.OutputTokens)
	}
}
