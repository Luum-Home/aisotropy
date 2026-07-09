package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
)

// reportDepths are the conversation depths this harness always calls out
// explicitly in its summary, matching the acceptance criteria for this
// benchmark (turn 10/25/50).
var reportDepths = []int{10, 25, 50}

func main() {
	seed := flag.Int64("seed", DefaultConfig().Seed, "PRNG seed for the synthetic conversation (determinism knob)")
	turns := flag.Int("turns", DefaultConfig().Turns, "number of user/assistant exchange pairs to synthesize")
	flag.Parse()

	cfg := DefaultConfig()
	cfg.Seed = *seed
	cfg.Turns = *turns

	if err := run(os.Stdout, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "bench/token-economy:", err)
		os.Exit(1)
	}
}

func run(w io.Writer, cfg Config) error {
	conv := GenerateConversation(cfg)

	ctx := context.Background()
	a, err := RunStrategyA(ctx, conv)
	if err != nil {
		return fmt.Errorf("strategy A: %w", err)
	}
	b := RunStrategyB(conv)

	if len(a) != len(b) {
		return fmt.Errorf("internal error: strategy A produced %d rows, strategy B produced %d", len(a), len(b))
	}

	fmt.Fprintf(w, "Token-economy benchmark: %d turns, seed=%d\n", cfg.Turns, cfg.Seed)
	fmt.Fprintf(w, "Strategy A = aisotropy surgical context (real internal/memory.WorkingMemory, bounded window + NoopSummarizer)\n")
	fmt.Fprintf(w, "Strategy B = classic LangChain-style full-history ConversationBufferMemory baseline\n\n")

	printTable(w, a, b)
	fmt.Fprintln(w)
	printDepthSummary(w, a, b, cfg.Turns)

	return nil
}

// printTable prints the full per-turn comparison: per-request and
// cumulative token estimates for both strategies under both heuristics,
// plus the per-request reduction percentage under both heuristics.
func printTable(w io.Writer, a, b []RequestCost) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "turn\tA req (bytes/4)\tA req (words*1.33)\tB req (bytes/4)\tB req (words*1.33)\tA cum (bytes/4)\tB cum (bytes/4)\treduction% (bytes/4)\treduction% (words*1.33)")
	for i := range a {
		reductionBytes := ReductionPercent(a[i].PerRequest.ByBytes, b[i].PerRequest.ByBytes)
		reductionWords := ReductionPercent(a[i].PerRequest.ByWords, b[i].PerRequest.ByWords)
		fmt.Fprintf(tw, "%d\t%d\t%d\t%d\t%d\t%d\t%d\t%.1f%%\t%.1f%%\n",
			a[i].Turn,
			a[i].PerRequest.ByBytes, a[i].PerRequest.ByWords,
			b[i].PerRequest.ByBytes, b[i].PerRequest.ByWords,
			a[i].Cumulative.ByBytes, b[i].Cumulative.ByBytes,
			reductionBytes, reductionWords,
		)
	}
	_ = tw.Flush()
}

// printDepthSummary prints the headline numbers this harness's acceptance
// criteria and README call out explicitly: per-request and cumulative A-vs-B
// input-token reduction at turns 10/25/50 (or, for depths beyond
// len(conv), the last available turn), under both heuristics.
func printDepthSummary(w io.Writer, a, b []RequestCost, totalTurns int) {
	fmt.Fprintln(w, "A vs B input-token reduction by depth (per-request cost at that turn):")
	for _, depth := range reportDepths {
		idx := depth - 1
		if idx >= len(a) {
			continue
		}
		rBytes := ReductionPercent(a[idx].PerRequest.ByBytes, b[idx].PerRequest.ByBytes)
		rWords := ReductionPercent(a[idx].PerRequest.ByWords, b[idx].PerRequest.ByWords)
		fmt.Fprintf(w, "  turn %2d: bytes/4 heuristic=%6.1f%%   words*1.33 heuristic=%6.1f%%\n", depth, rBytes, rWords)
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "A vs B input-token reduction by depth (cumulative total through that turn):")
	for _, depth := range reportDepths {
		idx := depth - 1
		if idx >= len(a) {
			continue
		}
		rBytes := ReductionPercent(a[idx].Cumulative.ByBytes, b[idx].Cumulative.ByBytes)
		rWords := ReductionPercent(a[idx].Cumulative.ByWords, b[idx].Cumulative.ByWords)
		fmt.Fprintf(w, "  turn %2d: bytes/4 heuristic=%6.1f%%   words*1.33 heuristic=%6.1f%%\n", depth, rBytes, rWords)
	}

	if totalTurns >= 1 {
		last := totalTurns - 1
		rBytes := ReductionPercent(a[last].PerRequest.ByBytes, b[last].PerRequest.ByBytes)
		rWords := ReductionPercent(a[last].PerRequest.ByWords, b[last].PerRequest.ByWords)
		fmt.Fprintf(w, "\nFinal A vs B input-token reduction at turn %d (per-request): bytes/4=%.1f%%  words*1.33=%.1f%%\n",
			totalTurns, rBytes, rWords)
	}
}
