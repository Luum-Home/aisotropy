package scoring

import (
	"math"
	"testing"
)

func anchors(model, fw string, vals map[string]float64) []AnchorResult {
	out := make([]AnchorResult, 0, len(vals))
	for id, v := range vals {
		out = append(out, AnchorResult{Model: model, Framework: fw, Mode: "qa", TaskID: id, Value: v})
	}
	return out
}

func spread(n int, base, delta float64, model, fw string) []AnchorResult {
	out := make([]AnchorResult, 0, n)
	for i := 0; i < n; i++ {
		id := string(rune('a' + i))
		out = append(out, AnchorResult{Model: model, Framework: fw, Mode: "qa", TaskID: id, Value: base + delta})
	}
	return out
}

// TestEquate_MeasuresTheShift — the happy path the whole mechanism exists for: a
// candidate model uniformly worse on the anchors yields exactly that shift, and
// applying it puts a candidate score back on the baseline scale.
func TestEquate_MeasuresTheShift(t *testing.T) {
	var rs []AnchorResult
	rs = append(rs, spread(15, 1.0, 0, "base", "fw")...)
	rs = append(rs, spread(15, 0.8, 0, "cand", "fw")...)

	eq := Equate("base", "cand", rs)
	if !eq.Usable {
		t.Fatalf("expected usable, got: %s", eq.Reason)
	}
	if math.Abs(eq.Shift-(-0.2)) > 1e-9 {
		t.Errorf("Shift = %v, want -0.2", eq.Shift)
	}
	if eq.PairedItems != 15 {
		t.Errorf("PairedItems = %d, want 15", eq.PairedItems)
	}
	got, ok := eq.Apply(0.8)
	if !ok || math.Abs(got-1.0) > 1e-9 {
		t.Errorf("Apply(0.8) = %v, ok=%v; want 1.0, true", got, ok)
	}
}

// TestEquate_RefusesThinEvidence — the failure this guard exists for. A shift
// computed from a handful of items is noise with a decimal point, and publishing
// it as a correction launders a guess into a number.
func TestEquate_RefusesThinEvidence(t *testing.T) {
	var rs []AnchorResult
	rs = append(rs, spread(5, 1.0, 0, "base", "fw")...)
	rs = append(rs, spread(5, 0.5, 0, "cand", "fw")...)

	eq := Equate("base", "cand", rs)
	if eq.Usable {
		t.Error("5 paired items must NOT yield a usable correction")
	}
	v, ok := eq.Apply(0.5)
	if ok {
		t.Error("Apply must refuse when the estimate is unusable")
	}
	if v != 0.5 {
		t.Errorf("a refused Apply must return the value UNTOUCHED, got %v", v)
	}
}

// TestEquate_DetectsInteraction — the subtle one. When a model helps one
// framework and hurts another, the mean shift is near zero and looks harmless
// while hiding the most interesting result in the run. A single scalar cannot
// represent an interaction, so the estimate must refuse rather than average it
// away.
func TestEquate_DetectsInteraction(t *testing.T) {
	var rs []AnchorResult
	rs = append(rs, spread(8, 0.5, 0, "base", "alpha")...)
	rs = append(rs, spread(8, 0.5, 0, "base", "beta")...)
	rs = append(rs, spread(8, 0.9, 0, "cand", "alpha")...) // +0.4
	rs = append(rs, spread(8, 0.1, 0, "cand", "beta")...)  // -0.4

	eq := Equate("base", "cand", rs)
	if math.Abs(eq.Shift) > 1e-9 {
		t.Errorf("mean shift should cancel to ~0, got %v", eq.Shift)
	}
	if eq.Usable {
		t.Error("an interaction of ±0.4 must NOT be reported as a usable single-number correction")
	}
	if got := eq.PerFrameworkShift["alpha"]; math.Abs(got-0.4) > 1e-9 {
		t.Errorf("alpha shift = %v, want +0.4", got)
	}
	if got := eq.PerFrameworkShift["beta"]; math.Abs(got+0.4) > 1e-9 {
		t.Errorf("beta shift = %v, want -0.4", got)
	}
}

// TestEquate_RequiresIdenticalAnchors — unpaired items carry no information
// about a shift. If the anchor set drifted between runs, the honest answer is
// "this measured nothing", not a shift computed from whatever overlapped.
func TestEquate_RequiresIdenticalAnchors(t *testing.T) {
	rs := append(
		anchors("base", "fw", map[string]float64{"q1": 1, "q2": 1}),
		anchors("cand", "fw", map[string]float64{"q3": 0, "q4": 0})...,
	)
	eq := Equate("base", "cand", rs)
	if eq.PairedItems != 0 || eq.Usable {
		t.Errorf("disjoint anchor sets must yield 0 paired items and no correction, got %d/%v", eq.PairedItems, eq.Usable)
	}
}

// TestEquate_Deterministic — a correction that is not reproducible is not
// evidence. Same inputs in a different order must produce identical output.
func TestEquate_Deterministic(t *testing.T) {
	var fwd []AnchorResult
	fwd = append(fwd, spread(15, 1.0, 0, "base", "fw")...)
	fwd = append(fwd, spread(15, 0.7, 0, "cand", "fw")...)

	rev := make([]AnchorResult, len(fwd))
	for i, r := range fwd {
		rev[len(fwd)-1-i] = r
	}

	a, b := Equate("base", "cand", fwd), Equate("base", "cand", rev)
	if a.Shift != b.Shift || a.StdDev != b.StdDev || a.PairedItems != b.PairedItems || a.Usable != b.Usable {
		t.Errorf("input order changed the result:\n  %+v\n  %+v", a, b)
	}
}

// TestEquate_SameModelIsNotEquating — guards against a caller "correcting" a
// board against itself, which would silently report a real shift of zero as a
// validated finding.
func TestEquate_SameModelIsNotEquating(t *testing.T) {
	eq := Equate("m", "m", spread(20, 1.0, 0, "m", "fw"))
	if eq.Usable {
		t.Error("equating a model against itself must never be usable")
	}
}

// TestEquate_SameTaskDifferentModesAreDistinct pins the defect that a fixture
// would never have exposed: the arena runs qa-01-capital in `qa`, `structured`
// AND `stream`, three separate measurements sharing one task id. Keying on
// (framework, task) collapsed them to one and threw away two thirds of the
// paired items — pushing a real run toward the "too few anchors" refusal for a
// reason that had nothing to do with the evidence available.
func TestEquate_SameTaskDifferentModesAreDistinct(t *testing.T) {
	var rs []AnchorResult
	for _, mode := range []string{"qa", "structured", "stream"} {
		for i := 0; i < 5; i++ {
			id := string(rune('a' + i))
			rs = append(rs,
				AnchorResult{Model: "base", Framework: "fw", Mode: mode, TaskID: id, Value: 1.0},
				AnchorResult{Model: "cand", Framework: "fw", Mode: mode, TaskID: id, Value: 0.8},
			)
		}
	}
	eq := Equate("base", "cand", rs)
	if eq.PairedItems != 15 {
		t.Fatalf("PairedItems = %d, want 15 (5 ids x 3 modes) — modes are collapsing", eq.PairedItems)
	}
	if !eq.Usable {
		t.Errorf("15 paired items should clear the floor, got: %s", eq.Reason)
	}
}
