package scoring

import (
	"fmt"
	"math"
	"sort"
)

// Anchor-item equating — turning "these two boards are not comparable" into
// "these two boards are comparable, with a MEASURED correction".
//
// THE PROBLEM. A board measures (framework × model × workload). Change the model
// and every absolute number moves, so two boards run on different providers
// cannot be compared — which in practice means a provider switch throws away the
// history. Worse, it invites the lazy repair: comparing them anyway and
// attributing the difference to whatever story is convenient. On 2026-07-27 a
// board changed provider AND changed several adapters in the same step; when
// numbers moved there was no way to say which change moved them.
//
// THE FIX, borrowed from psychometrics. Standardised tests face exactly this
// when they publish a new form: scores must stay comparable across forms of
// different difficulty. The solution is COMMON-ITEM EQUATING — a fixed subset of
// items (the anchors) appears in both forms, and the observed shift ON THE
// ANCHORS estimates the difficulty difference, which is then used to place both
// forms on one scale.
//
// Here the "forms" are models. Run the same fixed anchor items under model A and
// model B and the mean shift on those items IS the model effect, measured rather
// than assumed. It costs a few dozen calls instead of a board.
//
// WHAT THIS DOES NOT CLAIM. Equating estimates a shift; it does not make two
// models equivalent. The estimate is only trustworthy when the anchors were run
// under conditions identical in everything but the model, when enough anchors
// resolved, and when the frameworks do not disagree wildly about the direction of
// the shift (an interaction — the model helps one framework and hurts another —
// which no single scalar can represent). All three are checked below and
// reported, because an unusable correction presented as usable is worse than
// none: it launders a guess into a number.
//
// Purity: this file imports only fmt, math and sort — no network, no clock, no
// randomness — so it is covered by scripts/arena-determinism-guard.sh like every
// other scorer (.claude/rules/arena-deterministic-judging.md).

// AnchorResult is one anchor item's outcome for one framework under one model.
//
// Mode is part of the identity, not decoration. The same task id runs in several
// modes — qa-01-capital is exercised by `qa`, `structured` AND `stream` — so a
// key of (framework, task) collapses three independent measurements into
// whichever arrived last, silently discarding two thirds of the evidence and
// shrinking the paired-item count toward the refusal floor. Found by running the
// extractor against a real board and noticing the per-framework counts exceeded
// the anchor-set size; no unit test would have shown it, because a fixture would
// have used distinct ids.
type AnchorResult struct {
	Model     string
	Framework string
	Mode      string
	TaskID    string
	Value     float64 // the deterministic scorer's Value for that cell
}

// Equating is the measured relationship between two models on the anchor set.
type Equating struct {
	BaselineModel  string
	CandidateModel string

	// PairedItems is the number of (framework, mode, task) cells present under
	// BOTH models. Only paired cells can carry information about the shift.
	PairedItems int
	// Shift is mean(candidate) - mean(baseline) over paired items. Positive means
	// the candidate model scored higher on the anchors.
	Shift float64
	// StdDev of the per-item shifts — the spread. A large spread with a small
	// shift means the models differ per item, not in level.
	StdDev float64
	// PerFrameworkShift lets a reader see an interaction directly.
	PerFrameworkShift map[string]float64

	// Usable is false when the estimate must not be applied. Reason says why.
	Usable bool
	Reason string
}

// minAnchorPairs is the floor below which a shift is noise dressed as a number.
//
// Chosen, not derived: with fewer than 12 paired items a single disagreeing item
// moves the mean by more than 8 percentage points, which is larger than most
// real between-model differences on this suite. Raise it if the anchor set grows;
// never lower it to make a thin run look conclusive.
const minAnchorPairs = 12

// maxFrameworkSpread caps how much the frameworks may disagree about the shift
// before a single scalar stops describing them. Above this, the model helps some
// frameworks and hurts others — an interaction, not a level difference — and one
// number would hide exactly the thing a reader needs to see.
const maxFrameworkSpread = 0.35

// Equate computes the model effect from anchor results gathered under two models.
//
// Deterministic by construction: results are matched on (framework, mode, taskID) and
// every aggregate walks a sorted key list, so the same inputs always produce a
// byte-identical Equating regardless of input order.
func Equate(baselineModel, candidateModel string, results []AnchorResult) Equating {
	eq := Equating{
		BaselineModel:     baselineModel,
		CandidateModel:    candidateModel,
		PerFrameworkShift: map[string]float64{},
	}
	if baselineModel == candidateModel {
		eq.Reason = "baseline and candidate are the same model — there is nothing to equate"
		return eq
	}

	type key struct{ fw, mode, task string }
	base := map[key]float64{}
	cand := map[key]float64{}
	for _, r := range results {
		k := key{r.Framework, r.Mode, r.TaskID}
		switch r.Model {
		case baselineModel:
			base[k] = r.Value
		case candidateModel:
			cand[k] = r.Value
		}
	}

	// Sorted key walk: determinism, and it makes the per-framework grouping
	// independent of map iteration order.
	var keys []key
	for k := range base {
		if _, ok := cand[k]; ok {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].fw != keys[j].fw {
			return keys[i].fw < keys[j].fw
		}
		if keys[i].mode != keys[j].mode {
			return keys[i].mode < keys[j].mode
		}
		return keys[i].task < keys[j].task
	})

	eq.PairedItems = len(keys)
	if eq.PairedItems == 0 {
		eq.Reason = "no anchor item was run under both models — the anchor set must be IDENTICAL across runs, or it measures nothing"
		return eq
	}

	deltas := make([]float64, 0, len(keys))
	byFW := map[string][]float64{}
	for _, k := range keys {
		d := cand[k] - base[k]
		deltas = append(deltas, d)
		byFW[k.fw] = append(byFW[k.fw], d)
	}

	sum := 0.0
	for _, d := range deltas {
		sum += d
	}
	eq.Shift = sum / float64(len(deltas))

	variance := 0.0
	for _, d := range deltas {
		variance += (d - eq.Shift) * (d - eq.Shift)
	}
	eq.StdDev = math.Sqrt(variance / float64(len(deltas)))

	var fwNames []string
	for fw := range byFW {
		fwNames = append(fwNames, fw)
	}
	sort.Strings(fwNames)
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, fw := range fwNames {
		s := 0.0
		for _, d := range byFW[fw] {
			s += d
		}
		m := s / float64(len(byFW[fw]))
		eq.PerFrameworkShift[fw] = m
		lo, hi = math.Min(lo, m), math.Max(hi, m)
	}

	switch {
	case eq.PairedItems < minAnchorPairs:
		eq.Reason = fmt.Sprintf("only %d paired anchor item(s); %d are required before a shift is more signal than noise",
			eq.PairedItems, minAnchorPairs)
	case len(fwNames) > 1 && hi-lo > maxFrameworkSpread:
		eq.Reason = fmt.Sprintf("frameworks disagree about the shift (range %.2f across %d frameworks, limit %.2f) — "+
			"the models INTERACT with the frameworks rather than differing in level, and one scalar would hide it. "+
			"Report the per-framework shifts instead of correcting with a single number",
			hi-lo, len(fwNames), maxFrameworkSpread)
	default:
		eq.Usable = true
		eq.Reason = fmt.Sprintf("measured on %d paired anchor items across %d framework(s)", eq.PairedItems, len(fwNames))
	}
	return eq
}

// Apply corrects a candidate-model score onto the baseline model's scale.
//
// Refuses to correct with an unusable estimate: it returns the value untouched
// and ok=false, so a caller cannot accidentally publish a laundered number. The
// result is clamped to [0,1] because the scorers' Values live there and a
// correction must not invent a score outside the measurable range.
func (e Equating) Apply(candidateValue float64) (corrected float64, ok bool) {
	if !e.Usable {
		return candidateValue, false
	}
	return math.Min(1, math.Max(0, candidateValue-e.Shift)), true
}
