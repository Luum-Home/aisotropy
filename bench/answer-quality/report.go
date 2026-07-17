package main

import (
	"fmt"
	"strings"

	"github.com/Luum-Home/aisotropy/internal/costmeter"
)

// toFive de-normalizes a [0,1] judge score back to the 1..5 human rubric range
// used across the report, so all three graded axes present uniformly regardless
// of the scale their criterion was defined on.
func toFive(norm float64) float64 { return norm*4 + 1 }

// estimateInfo is the a-priori, pre-run spend estimate. It is deliberately
// ROUGH (token counts vary with prompt length and model verbosity) and is
// printed only so an operator sees an order of magnitude BEFORE spending, next
// to the exact MEASURED totals after the run.
type estimateInfo struct {
	Questions         int
	TurnCalls         int
	JudgeCalls        int
	AssumedInPerCall  int
	AssumedOutPerCall int
}

// estimateFor computes the pre-run estimate for a question set: one turn call
// per question plus llmCriteriaFor(q) judge calls each.
func estimateFor(questions []question) estimateInfo {
	e := estimateInfo{
		Questions:         len(questions),
		TurnCalls:         len(questions),
		AssumedInPerCall:  400,
		AssumedOutPerCall: 150,
	}
	for _, q := range questions {
		e.JudgeCalls += llmCriteriaFor(q)
	}
	return e
}

// totalCalls is the estimated number of provider round-trips (turn + judge).
func (e estimateInfo) totalCalls() int { return e.TurnCalls + e.JudgeCalls }

// roughTokens is the ballpark token total the estimate implies.
func (e estimateInfo) roughTokens() int {
	return e.totalCalls() * (e.AssumedInPerCall + e.AssumedOutPerCall)
}

// renderEstimate is the block printed BEFORE the run.
func renderEstimate(e estimateInfo, servingReal, judgeReal bool) string {
	var b strings.Builder
	b.WriteString("PRE-RUN SPEND ESTIMATE (rough, order-of-magnitude only)\n")
	fmt.Fprintf(&b, "  questions          : %d\n", e.Questions)
	fmt.Fprintf(&b, "  provider calls     : ~%d  (%d turns + %d judge calls)\n", e.totalCalls(), e.TurnCalls, e.JudgeCalls)
	fmt.Fprintf(&b, "  rough token total  : ~%d  (assumes ~%d in + ~%d out per call)\n", e.roughTokens(), e.AssumedInPerCall, e.AssumedOutPerCall)
	if !servingReal || !judgeReal {
		b.WriteString("  NOTE               : no real provider key ⇒ STUB run — zero real API spend.\n")
	}
	b.WriteString("  (exact MEASURED spend is reported after the run)\n")
	return b.String()
}

// aggregate holds the run's headline numbers.
type aggregate struct {
	scored          int // questions with judge scores (no error)
	errored         int
	meanCorrectness float64
	meanHelpfulness float64
	meanFaithful    float64
	faithfulN       int
	refusals        int
	hallucinations  int
	meanLatencyMS   float64
}

// summarize folds the per-question results into the headline aggregate.
func summarize(rr *runResult) aggregate {
	var a aggregate
	var sumCorr, sumHelp, sumFaith, sumLat float64
	for _, r := range rr.Results {
		if r.Err != "" {
			a.errored++
			continue
		}
		a.scored++
		sumCorr += r.Scores.correctness
		sumHelp += r.Scores.helpfulness
		sumLat += float64(r.LatencyMS)
		if r.Scores.refused {
			a.refusals++
		}
		if r.Scores.faithfulnessScored {
			a.faithfulN++
			sumFaith += r.Scores.faithfulness
			if r.Scores.hallucinated {
				a.hallucinations++
			}
		}
	}
	if a.scored > 0 {
		a.meanCorrectness = toFive(sumCorr / float64(a.scored))
		a.meanHelpfulness = toFive(sumHelp / float64(a.scored))
		a.meanLatencyMS = sumLat / float64(a.scored)
	}
	if a.faithfulN > 0 {
		a.meanFaithful = toFive(sumFaith / float64(a.faithfulN))
	}
	return a
}

// render formats the full post-run report.
func render(rr *runResult) string {
	var b strings.Builder
	a := summarize(rr)

	b.WriteString("======================================================================\n")
	b.WriteString("  aisotropy — END-TO-END ANSWER-QUALITY EVAL\n")
	b.WriteString("======================================================================\n")
	fmt.Fprintf(&b, "question set : %s (%d questions)\n", rr.SetName, len(rr.Results))
	fmt.Fprintf(&b, "serving      : %s%s\n", rr.ServingProvider, realTag(rr.ServingReal))
	fmt.Fprintf(&b, "judge        : %s%s (provider-agnostic, eval.JudgeClientFromEnv)\n", rr.JudgeProvider, realTag(rr.JudgeReal))
	b.WriteString("\n")

	if !rr.ServingReal || !rr.JudgeReal {
		b.WriteString("!! STUB RUN — this proves the pipeline + judge WIRING end-to-end.\n")
		b.WriteString("!! The scores below are DETERMINISTIC STUB ARTIFACTS, NOT real answer\n")
		b.WriteString("!! quality. Set a provider key (OPENROUTER_API_KEY / OPENAI_API_KEY /\n")
		b.WriteString("!! ANTHROPIC_API_KEY) and re-run for measured numbers.\n\n")
	}

	if rr.Aborted {
		fmt.Fprintf(&b, "!! RUN ABORTED: %s\n\n", rr.AbortReason)
	}

	// Aggregate block.
	b.WriteString("AGGREGATE (means; graded axes on 1..5, higher is better)\n")
	fmt.Fprintf(&b, "  correctness      : %.2f / 5   (over %d scored)\n", a.meanCorrectness, a.scored)
	fmt.Fprintf(&b, "  helpfulness      : %.2f / 5\n", a.meanHelpfulness)
	if a.faithfulN > 0 {
		fmt.Fprintf(&b, "  faithfulness     : %.2f / 5   (over %d grounded questions)\n", a.meanFaithful, a.faithfulN)
	} else {
		b.WriteString("  faithfulness     : n/a       (no question carried grounding context)\n")
	}
	fmt.Fprintf(&b, "  refusal rate     : %s\n", pct(a.refusals, a.scored))
	if a.faithfulN > 0 {
		fmt.Fprintf(&b, "  hallucination    : %s   (grounded questions scoring faithfulness < %.2f)\n", pct(a.hallucinations, a.faithfulN), hallucinationThreshold)
	} else {
		b.WriteString("  hallucination    : n/a       (no grounded questions to check)\n")
	}
	fmt.Fprintf(&b, "  mean latency     : %.0f ms\n", a.meanLatencyMS)
	if a.errored > 0 {
		fmt.Fprintf(&b, "  errored          : %d question(s) failed the turn or judge (see per-question)\n", a.errored)
	}
	b.WriteString("\n")

	// Measured spend.
	grandIn := rr.TurnInTok + rr.JudgeInTok
	grandOut := rr.TurnOutTok + rr.JudgeOutTok
	b.WriteString("MEASURED TOKEN SPEND (provider-reported usage)\n")
	fmt.Fprintf(&b, "  turn (serving)   : in %d / out %d\n", rr.TurnInTok, rr.TurnOutTok)
	fmt.Fprintf(&b, "  judge            : in %d / out %d\n", rr.JudgeInTok, rr.JudgeOutTok)
	fmt.Fprintf(&b, "  grand total      : in %d / out %d  (%d tokens)\n", grandIn, grandOut, grandIn+grandOut)
	b.WriteString(renderCost(rr, grandIn, grandOut))
	b.WriteString("\n")

	// Per-question detail.
	b.WriteString("PER-QUESTION\n")
	fmt.Fprintf(&b, "  %-16s %5s %5s %6s %4s %5s %8s %9s\n", "id", "corr", "help", "faith", "ref", "hall", "lat(ms)", "tok(t+j)")
	for _, r := range rr.Results {
		if r.Err != "" {
			fmt.Fprintf(&b, "  %-16s  ERROR: %s\n", trunc(r.ID, 16), r.Err)
			continue
		}
		faith := "  n/a"
		hall := "  -"
		if r.Scores.faithfulnessScored {
			faith = fmt.Sprintf("%5.2f", toFive(r.Scores.faithfulness))
			hall = boolMark(r.Scores.hallucinated)
		}
		tok := r.TurnInTok + r.TurnOutTok + r.JudgeInTok + r.JudgeOutTok
		fmt.Fprintf(&b, "  %-16s %5.2f %5.2f %6s %4s %5s %8d %9d\n",
			trunc(r.ID, 16), toFive(r.Scores.correctness), toFive(r.Scores.helpfulness),
			faith, boolMark(r.Scores.refused), hall, r.LatencyMS, tok)
	}
	b.WriteString("\n")

	// Honesty footer: generic vs domain.
	b.WriteString("----------------------------------------------------------------------\n")
	b.WriteString("GENERIC vs DOMAIN — read this before trusting a number:\n")
	b.WriteString("  The bundled generic set is a WIRING SMOKE. Generic ≠ your domain.\n")
	b.WriteString("  The ONLY numbers that reflect production quality come from running\n")
	b.WriteString("  this harness on YOUR OWN domain Q&A: copy testdata/domain.example.json,\n")
	b.WriteString("  fill it with your real questions (and ideal answers / context docs),\n")
	b.WriteString("  and run with  -data your-domain.json .\n")
	b.WriteString("----------------------------------------------------------------------\n")
	return b.String()
}

// renderCost renders the estimated USD cost, or an n/a note when no pricing was
// supplied. Pricing is provider-specific and operator-supplied (there is no
// universal price), so cost is only shown when the operator sets it.
func renderCost(rr *runResult, grandIn, grandOut int64) string {
	if rr.InPricePer1M <= 0 && rr.OutPricePer1M <= 0 {
		return "  est. cost        : n/a  (set AISOTROPY_EVAL_IN_PRICE_PER_1M / _OUT_PRICE_PER_1M to estimate $)\n"
	}
	cost := costmeter.EstimateCostUSD(int(grandIn), int(grandOut), rr.InPricePer1M, rr.OutPricePer1M)
	return fmt.Sprintf("  est. cost        : $%.4f  (in $%.2f/1M, out $%.2f/1M)\n", cost, rr.InPricePer1M, rr.OutPricePer1M)
}

func realTag(real bool) string {
	if real {
		return "  [REAL provider]"
	}
	return "  [STUB — no key]"
}

func pct(n, total int) string {
	if total == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d/%d (%.0f%%)", n, total, 100*float64(n)/float64(total))
}

func boolMark(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
