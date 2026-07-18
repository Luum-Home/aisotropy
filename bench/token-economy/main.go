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

// runStrategyA/runStrategyB are package-level seams over RunStrategyA and
// RunStrategyB. run() calls through them (rather than the functions
// directly) so tests can substitute a fake that fails, or that returns a
// mismatched row count, to exercise run()'s two error-wrapping branches
// below without needing to make the real memory-backed RunStrategyA fail
// (which — driven by a real in-memory BadgerDB and a non-cancellable
// internal context — has no reachable failure path from a Config alone).
var (
	runStrategyA = RunStrategyA
	runStrategyB = RunStrategyB
)

// parseFlags parses the token-economy CLI flags out of args (excluding the
// program name, i.e. os.Args[1:]) into a Config seeded from DefaultConfig.
// It is factored out of main() so flag parsing — including a malformed flag
// value — is testable without driving the process's real os.Args or exit
// code: it uses a fresh, ContinueOnError FlagSet and returns any parse error
// to the caller instead of exiting directly.
func parseFlags(args []string) (Config, error) {
	fs := flag.NewFlagSet("token-economy", flag.ContinueOnError)
	seed := fs.Int64("seed", DefaultConfig().Seed, "PRNG seed for the synthetic conversation (determinism knob)")
	turns := fs.Int("turns", DefaultConfig().Turns, "number of user/assistant exchange pairs to synthesize")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	cfg := DefaultConfig()
	cfg.Seed = *seed
	cfg.Turns = *turns
	return cfg, nil
}

// exitFunc is a seam over os.Exit: main()'s two fatal paths call through it
// instead of os.Exit directly, so a test can substitute a non-terminating
// fake and observe that the intended exit code was requested, without
// killing the test binary. Production always uses the real os.Exit.
var exitFunc = os.Exit

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench/token-economy:", err)
		exitFunc(1)
		return
	}

	if err := run(os.Stdout, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "bench/token-economy:", err)
		exitFunc(1)
		return
	}
}

func run(w io.Writer, cfg Config) error {
	conv := GenerateConversation(cfg)

	ctx := context.Background()
	a, err := runStrategyA(ctx, conv)
	if err != nil {
		return fmt.Errorf("strategy A: %w", err)
	}
	b := runStrategyB(conv)

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
