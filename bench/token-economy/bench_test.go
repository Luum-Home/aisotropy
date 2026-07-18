package main

import (
	"bytes"
	"context"
	"errors"
	"math/rand"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/memory"
)

// TestGenerateConversationDeterministic verifies that GenerateConversation
// is a pure function of its Config: the same seed and parameters always
// produce byte-identical output. This is the foundation of the whole
// benchmark's determinism guarantee.
func TestGenerateConversationDeterministic(t *testing.T) {
	cfg := DefaultConfig()
	a := GenerateConversation(cfg)
	b := GenerateConversation(cfg)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("GenerateConversation(%+v) is not deterministic:\nrun1[0]=%+v\nrun2[0]=%+v", cfg, a[0], b[0])
	}
	if len(a) != cfg.Turns {
		t.Fatalf("GenerateConversation returned %d turns, want %d", len(a), cfg.Turns)
	}
}

// TestGenerateConversationDifferentSeeds verifies distinct seeds actually
// produce distinct conversations, so determinism isn't hiding a generator
// that ignores its seed entirely.
func TestGenerateConversationDifferentSeeds(t *testing.T) {
	cfg1 := DefaultConfig()
	cfg2 := DefaultConfig()
	cfg2.Seed = cfg1.Seed + 1
	a := GenerateConversation(cfg1)
	b := GenerateConversation(cfg2)
	if reflect.DeepEqual(a, b) {
		t.Fatalf("different seeds (%d vs %d) produced identical conversations", cfg1.Seed, cfg2.Seed)
	}
}

// TestStrategyADeterministic verifies that driving the real
// internal/memory.WorkingMemory through the same synthetic conversation
// twice produces identical per-turn and cumulative token totals — i.e. the
// async condensation goroutine (coalesced via wm.Sync()) never introduces
// run-to-run variance in this harness's output.
func TestStrategyADeterministic(t *testing.T) {
	ctx := context.Background()
	conv := GenerateConversation(DefaultConfig())

	run1, err := RunStrategyA(ctx, conv)
	if err != nil {
		t.Fatalf("RunStrategyA (run 1): %v", err)
	}
	run2, err := RunStrategyA(ctx, conv)
	if err != nil {
		t.Fatalf("RunStrategyA (run 2): %v", err)
	}
	if !reflect.DeepEqual(run1, run2) {
		t.Fatalf("RunStrategyA is not deterministic across two runs of the same conversation")
	}
	if len(run1) != len(conv) {
		t.Fatalf("RunStrategyA returned %d rows, want %d", len(run1), len(conv))
	}
}

// TestStrategyBDeterministic mirrors TestStrategyADeterministic for the
// baseline strategy, which has no concurrency but should still be checked.
func TestStrategyBDeterministic(t *testing.T) {
	conv := GenerateConversation(DefaultConfig())
	run1 := RunStrategyB(conv)
	run2 := RunStrategyB(conv)
	if !reflect.DeepEqual(run1, run2) {
		t.Fatalf("RunStrategyB is not deterministic across two runs of the same conversation")
	}
}

// TestStrategyBGrowsWithHistory verifies the defining property of the
// classic full-history baseline: per-request cost strictly increases as the
// conversation grows (every prior turn is resent verbatim), unlike
// Strategy A's bounded window.
func TestStrategyBGrowsWithHistory(t *testing.T) {
	conv := GenerateConversation(DefaultConfig())
	results := RunStrategyB(conv)
	for i := 1; i < len(results); i++ {
		if results[i].PerRequest.ByBytes <= results[i-1].PerRequest.ByBytes {
			t.Fatalf("turn %d per-request cost (%d) did not increase over turn %d (%d); classic full-history baseline should grow monotonically",
				results[i].Turn, results[i].PerRequest.ByBytes, results[i-1].Turn, results[i-1].PerRequest.ByBytes)
		}
	}
}

// TestStrategyABounded verifies Strategy A grows markedly more slowly than
// Strategy B as the conversation lengthens. It is NOT flat: NoopSummarizer
// appends to the summary on every condensation round rather than
// re-compressing it (see TestNoopSummarizerSummaryGrowsUnbounded), so
// Strategy A's per-request cost does keep growing — just with a much
// smaller slope than Strategy B's unbounded verbatim full-history resend.
// We check this by comparing the per-request cost of the last turn against
// the per-request cost of the turn halfway through a long conversation —
// Strategy A's should not come close to doubling over that span, the way an
// unbounded linear-growth strategy's cost would.
func TestStrategyABounded(t *testing.T) {
	ctx := context.Background()
	cfg := DefaultConfig()
	cfg.Turns = 50
	conv := GenerateConversation(cfg)

	results, err := RunStrategyA(ctx, conv)
	if err != nil {
		t.Fatalf("RunStrategyA: %v", err)
	}

	mid := results[len(results)/2-1].PerRequest.ByBytes
	last := results[len(results)-1].PerRequest.ByBytes

	// Strategy B's cost roughly doubles between the midpoint and the end of
	// a linearly-growing history. Strategy A's window is bounded, so its
	// cost should not come close to doubling over the same span.
	if last > mid*2 {
		t.Fatalf("Strategy A per-request cost grew from %d (mid) to %d (last) — bounded-window context should not roughly double like an unbounded history would", mid, last)
	}
}

// TestRunProducesExpectedSections verifies the go run ./bench/token-economy
// output contains the sections the acceptance criteria and README promise:
// a per-turn table and a final "A vs B input-token reduction" line.
func TestRunProducesExpectedSections(t *testing.T) {
	var buf bytes.Buffer
	cfg := DefaultConfig()
	cfg.Turns = 50
	if err := run(&buf, cfg); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"turn 10:", "turn 25:", "turn 50:",
		"Final A vs B input-token reduction at turn 50",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("run() output missing expected section %q\n--- output ---\n%s", want, out)
		}
	}
}

// TestRunDeterministicOutput verifies two consecutive invocations of run()
// (the same function main() calls) produce byte-identical stdout — the
// acceptance criterion "two consecutive runs print identical numbers".
func TestRunDeterministicOutput(t *testing.T) {
	cfg := DefaultConfig()
	var buf1, buf2 bytes.Buffer
	if err := run(&buf1, cfg); err != nil {
		t.Fatalf("run (1): %v", err)
	}
	if err := run(&buf2, cfg); err != nil {
		t.Fatalf("run (2): %v", err)
	}
	if buf1.String() != buf2.String() {
		t.Fatalf("run() output differs across two consecutive invocations with the same Config")
	}
}

// BenchmarkStrategyA measures the cost of driving the real
// internal/memory.WorkingMemory through a 50-turn synthetic conversation.
func BenchmarkStrategyA(b *testing.B) {
	ctx := context.Background()
	conv := GenerateConversation(DefaultConfig())
	b.ResetTimer()
	for range b.N {
		if _, err := RunStrategyA(ctx, conv); err != nil {
			b.Fatalf("RunStrategyA: %v", err)
		}
	}
}

// BenchmarkStrategyB measures the cost of computing the classic
// full-history baseline over the same 50-turn synthetic conversation.
func BenchmarkStrategyB(b *testing.B) {
	conv := GenerateConversation(DefaultConfig())
	b.ResetTimer()
	for range b.N {
		_ = RunStrategyB(conv)
	}
}

// BenchmarkGenerateConversation measures synthetic-conversation generation
// cost in isolation.
func BenchmarkGenerateConversation(b *testing.B) {
	cfg := DefaultConfig()
	b.ResetTimer()
	for range b.N {
		_ = GenerateConversation(cfg)
	}
}

// TestNoopSummarizerSummaryStaysBounded pins the property that produces this
// benchmark's headline numbers: NoopSummarizer now re-condenses to a bounded
// size (memory.maxNoopSummaryLen) instead of appending a new snippet onto an
// ever-growing summary every round. So Strategy A's system prompt reaches a
// roughly constant summary-block size as the conversation grows, while Strategy
// B's full-history resend keeps growing linearly — which is what lifts the A-vs-B
// reduction to the ~70%+ range at depth. This test was the reverse assertion
// (unbounded growth) before the bound landed; it now guards against a
// regression back to the leak. A real memory.LLMSummarizer would re-compress
// similarly; this benchmark stays credential-free with the deterministic one.
func TestNoopSummarizerSummaryStaysBounded(t *testing.T) {
	ctx := context.Background()
	conv := GenerateConversation(DefaultConfig())
	wm, err := memory.OpenInMemory(0, memory.NoopSummarizer{})
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()

	// A generous ceiling well above the summarizer's internal cap: the point is
	// that the summary plateaus rather than growing with turn count. The old
	// unbounded behavior blew past this within ~30 turns.
	const boundedCeilingChars = 4096

	var checkpoints, maxLen int
	for i, turn := range conv {
		if err := wm.Append(ctx, benchKey, memory.Message{Role: roleUser, Content: turn.User}); err != nil {
			t.Fatal(err)
		}
		wm.Sync()
		if err := wm.Append(ctx, benchKey, memory.Message{Role: "assistant", Content: turn.Assistant}); err != nil {
			t.Fatal(err)
		}
		wm.Sync()
		if (i+1)%10 == 0 {
			summary, err := wm.Summary(ctx, benchKey)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("after turn %d: summary length = %d chars, summary tokens(bytes/4) = %d", i+1, len(summary), EstimateTokensByBytes(summary))
			if len(summary) > boundedCeilingChars {
				t.Errorf("after turn %d: summary length (%d) exceeded the bounded ceiling (%d) — the unbounded-growth leak may have regressed", i+1, len(summary), boundedCeilingChars)
			}
			if len(summary) > maxLen {
				maxLen = len(summary)
			}
			checkpoints++
		}
	}
	if checkpoints == 0 {
		t.Fatal("no checkpoints were reached — DefaultConfig().Turns is too small for this test")
	}
	t.Logf("summary plateaued at max %d chars across %d checkpoints", maxLen, checkpoints)
}

// TestRandomSentence_EmptyWhenZeroWords covers randomSentence's empty-output
// branch: a zero-word request (minWords == maxWords == 0) joins zero words
// into "", which must short-circuit rather than index sentence[:1].
func TestRandomSentence_EmptyWhenZeroWords(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	if got := randomSentence(rng, 0, 0); got != "" {
		t.Errorf("randomSentence(rng, 0, 0) = %q, want empty string", got)
	}
}

// TestReductionPercent_ZeroBaselineReturnsZero covers ReductionPercent's
// division-by-zero guard: an all-zero baseline (b == 0) must return 0 rather
// than divide by zero.
func TestReductionPercent_ZeroBaselineReturnsZero(t *testing.T) {
	if got := ReductionPercent(100, 0); got != 0 {
		t.Errorf("ReductionPercent(100, 0) = %v, want 0", got)
	}
}

// limitedErrCtx is a context.Context whose Err() returns nil for the first
// okCalls invocations and context.Canceled on every call after that.
// RunStrategyA drives memory.WorkingMemory.Summary, .Recent, and two
// .Append calls per turn, each of which checks ctx.Err() first — so this
// lets a test pick exactly which of those four ctx-gated calls fails,
// deterministically and without any real cancellation/timeout race.
type limitedErrCtx struct {
	context.Context
	calls   int
	okCalls int
}

func (c *limitedErrCtx) Err() error {
	c.calls++
	if c.calls > c.okCalls {
		return context.Canceled
	}
	return nil
}

// TestRunStrategyA_ContextErrorsPropagateAtEachStage covers RunStrategyA's
// four ctx-gated error-wrapping branches (read summary, read recent, append
// user, append assistant) — each guarded by an early ctx.Err() check inside
// the memory.WorkingMemory method it calls.
func TestRunStrategyA_ContextErrorsPropagateAtEachStage(t *testing.T) {
	conv := []Turn{{User: "hello there", Assistant: "hi, how can I help"}}

	cases := []struct {
		name    string
		okCalls int
		wantSub string
	}{
		{name: "read summary fails", okCalls: 0, wantSub: "read summary"},
		{name: "read recent fails", okCalls: 1, wantSub: "read recent"},
		{name: "append user fails", okCalls: 2, wantSub: "append user"},
		{name: "append assistant fails", okCalls: 3, wantSub: "append assistant"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &limitedErrCtx{Context: context.Background(), okCalls: tc.okCalls}
			if _, err := RunStrategyA(ctx, conv); err == nil {
				t.Fatalf("expected an error after %d ok ctx.Err() call(s)", tc.okCalls)
			} else if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// withStubStrategies swaps the runStrategyA/runStrategyB seams run() calls
// through, restoring the real RunStrategyA/RunStrategyB on cleanup. A nil
// replacement leaves that seam untouched.
func withStubStrategies(t *testing.T, a func(context.Context, []Turn) ([]RequestCost, error), b func([]Turn) []RequestCost) {
	t.Helper()
	origA, origB := runStrategyA, runStrategyB
	if a != nil {
		runStrategyA = a
	}
	if b != nil {
		runStrategyB = b
	}
	t.Cleanup(func() {
		runStrategyA = origA
		runStrategyB = origB
	})
}

// TestRun_StrategyAFailurePropagates covers run()'s "strategy A: %w"
// error-wrapping branch via the runStrategyA seam — the real RunStrategyA has
// no reachable failure path from a plain Config, so this substitutes a fake.
func TestRun_StrategyAFailurePropagates(t *testing.T) {
	wantErr := errors.New("boom")
	withStubStrategies(t, func(context.Context, []Turn) ([]RequestCost, error) {
		return nil, wantErr
	}, nil)

	var buf bytes.Buffer
	err := run(&buf, DefaultConfig())
	if err == nil {
		t.Fatal("expected an error when strategy A fails")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want it to wrap %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "strategy A:") {
		t.Errorf("error = %q, want the %q prefix", err.Error(), "strategy A:")
	}
}

// TestRun_RowCountMismatchIsAnInternalError covers run()'s defensive
// invariant check (strategy A and B must produce the same row count) via the
// runStrategyA/runStrategyB seams, since real conversations always keep the
// two strategies' row counts equal by construction.
func TestRun_RowCountMismatchIsAnInternalError(t *testing.T) {
	withStubStrategies(t,
		func(context.Context, []Turn) ([]RequestCost, error) {
			return []RequestCost{{Turn: 1}}, nil
		},
		func([]Turn) []RequestCost {
			return []RequestCost{{Turn: 1}, {Turn: 2}}
		},
	)

	var buf bytes.Buffer
	err := run(&buf, DefaultConfig())
	if err == nil {
		t.Fatal("expected an internal-error when strategy A/B row counts mismatch")
	}
	if !strings.Contains(err.Error(), "internal error") {
		t.Errorf("error = %q, want the %q prefix", err.Error(), "internal error")
	}
}

// TestRun_ShortConversationSkipsDepthSummaryEntries covers
// printDepthSummary's "continue" branches in both the per-request and
// cumulative loops: a conversation shorter than every reportDepths entry
// (10/25/50) has no turn to report at any of those depths.
func TestRun_ShortConversationSkipsDepthSummaryEntries(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Turns = 5 // shorter than every entry in reportDepths

	var buf bytes.Buffer
	if err := run(&buf, cfg); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := buf.String()

	for _, unwanted := range []string{"turn 10:", "turn 25:", "turn 50:"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("run() output unexpectedly contains %q for a %d-turn conversation:\n%s", unwanted, cfg.Turns, out)
		}
	}
	if !strings.Contains(out, "Final A vs B input-token reduction at turn 5") {
		t.Errorf("run() output missing the final-turn summary line:\n%s", out)
	}
}

// TestParseFlags covers the CLI flag surface main() delegates to: defaults,
// explicit overrides, and a malformed value returning an error rather than
// exiting the process (the old flag.Parse()-on-the-global-CommandLine
// behavior this was factored out of could not be driven from a test at all).
func TestParseFlags(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		cfg, err := parseFlags(nil)
		if err != nil {
			t.Fatalf("parseFlags(nil): %v", err)
		}
		if cfg != DefaultConfig() {
			t.Errorf("parseFlags(nil) = %+v, want DefaultConfig() = %+v", cfg, DefaultConfig())
		}
	})

	t.Run("overrides seed and turns", func(t *testing.T) {
		cfg, err := parseFlags([]string{"-seed=7", "-turns=3"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		want := DefaultConfig()
		want.Seed, want.Turns = 7, 3
		if cfg != want {
			t.Errorf("cfg = %+v, want %+v", cfg, want)
		}
	})

	t.Run("invalid flag value returns an error instead of exiting", func(t *testing.T) {
		if _, err := parseFlags([]string{"-turns=not-a-number"}); err == nil {
			t.Fatal("expected a parse error for a malformed -turns value")
		}
	})
}

// withFakeExit swaps the exitFunc seam for the duration of a test, recording
// every requested exit code instead of terminating the test binary, and
// restores the real os.Exit on cleanup.
func withFakeExit(t *testing.T) *[]int {
	t.Helper()
	var codes []int
	prev := exitFunc
	exitFunc = func(code int) { codes = append(codes, code) }
	t.Cleanup(func() { exitFunc = prev })
	return &codes
}

// TestMain_Direct drives the real main() end to end via the exitFunc and
// runStrategyA/runStrategyB seams, covering its flag-parsing, success, and
// both fatal (non-zero exit) paths without ever calling the real os.Exit or
// touching the process's real os.Args.
func TestMain_Direct(t *testing.T) {
	origArgs := os.Args
	t.Cleanup(func() { os.Args = origArgs })

	t.Run("success", func(t *testing.T) {
		os.Args = []string{"token-economy", "-turns=3", "-seed=99"}
		codes := withFakeExit(t)
		main()
		if len(*codes) != 0 {
			t.Errorf("exitFunc called with %v, want no fatal exit on success", *codes)
		}
	})

	t.Run("bad flag exits 1", func(t *testing.T) {
		os.Args = []string{"token-economy", "-turns=not-a-number"}
		codes := withFakeExit(t)
		main()
		if len(*codes) != 1 || (*codes)[0] != 1 {
			t.Errorf("exitFunc calls = %v, want exactly one call with code 1", *codes)
		}
	})

	t.Run("run failure exits 1", func(t *testing.T) {
		os.Args = []string{"token-economy", "-turns=3"}
		withStubStrategies(t, func(context.Context, []Turn) ([]RequestCost, error) {
			return nil, errors.New("boom")
		}, nil)
		codes := withFakeExit(t)
		main()
		if len(*codes) != 1 || (*codes)[0] != 1 {
			t.Errorf("exitFunc calls = %v, want exactly one call with code 1", *codes)
		}
	})
}
