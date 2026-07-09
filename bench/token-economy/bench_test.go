package main

import (
	"bytes"
	"context"
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
	for i := 0; i < b.N; i++ {
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
	for i := 0; i < b.N; i++ {
		_ = RunStrategyB(conv)
	}
}

// BenchmarkGenerateConversation measures synthetic-conversation generation
// cost in isolation.
func BenchmarkGenerateConversation(b *testing.B) {
	cfg := DefaultConfig()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
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
		if err := wm.Append(ctx, benchKey, memory.Message{Role: "user", Content: turn.User}); err != nil {
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
