package main

import (
	"strings"
	"testing"

	"aisotropy-live-runner/scoring"
)

// This file is the offline (no adapter, no model call) proof that the
// rendered RESULTS.md labels `qa` (and `structured`/`stream`, which reuse
// the identical single-shot QA task list — see buildQAStyle in tasks.go,
// called for mode in {"qa","structured","stream"} at tasks.go's buildWork
// switch) as SMOKE-TEST coverage rather than a quality signal, and that the
// modes with real discriminating signal (memory, rag, tools) are named.
//
// Background: a calibration run with the model held constant found qa
// saturated at a perfect 1.0 for every framework, including on items
// deliberately written to be easy to get wrong — it measures the model
// (a constant here), not the framework. This test pins that finding in the
// rendered output so a future edit can't silently drop the caveat.

func TestWriteResults_QAIsLabeledSmokeTestNotQualitySignal(t *testing.T) {
	records := []record{
		{Framework: "aisotropy", Mode: "qa", ID: "qa-h01-bones", Kind: "qa",
			Det: scoring.Score{Value: 1, Pass: true, Detail: "exact match"}},
		{Framework: "aisotropy", Mode: "structured", ID: "qa-h02-canberra", Kind: "structured",
			Det: scoring.Score{Value: 1, Pass: true, Detail: "valid JSON"}, StructuredOK: true},
		{Framework: "aisotropy", Mode: "stream", ID: "qa-h03-iron", Kind: "stream",
			Det: scoring.Score{Value: 1, Pass: true, Detail: "streamed"}, TTFTMs: 120},
		{Framework: "aisotropy", Mode: "memory", ID: "mem-h01-overwrite", Kind: "memory",
			Det: scoring.Score{Value: 1, Pass: true, Detail: "recalled"}},
	}

	rep := reportData{
		Model:      "agnes-2.0-flash",
		Frameworks: []string{"aisotropy"},
		Modes:      []string{"qa", "structured", "stream", "memory"},
		Forms:      []string{"native"},
		Lane:       "real",
		Records:    records,
	}

	aggs := map[string]*modeAgg{}
	key := func(fw, mode string) string { return fw + "\x00" + mode }
	get := func(fw, mode string) *modeAgg {
		k := key(fw, mode)
		a := aggs[k]
		if a == nil {
			a = &modeAgg{}
			aggs[k] = a
		}
		return a
	}
	for _, r := range records {
		a := get(r.Framework, r.Mode)
		a.runs++
		a.detN++
		a.detValSum += r.Det.Value
		if r.Det.Pass {
			a.detPassN++
		}
		switch r.Kind {
		case "structured":
			a.structN++
			if r.StructuredOK {
				a.structOK++
			}
		case "stream":
			a.ttftSum += r.TTFTMs
			a.ttftN++
		}
	}

	var b strings.Builder
	writeQualityTable(&b, rep, aggs, get)
	out := b.String()

	// (1) The empirical-finding note is present, at the top of the
	// correctness section (before the per-mode table rows), not buried in a
	// bottom footnote.
	noteIdx := strings.Index(out, "QA is a smoke test, not a quality signal")
	if noteIdx == -1 {
		t.Fatalf("missing the QA-is-a-smoke-test empirical finding note; got:\n%s", out)
	}
	tableHeaderIdx := strings.Index(out, "| Framework | Mode | Deterministic score")
	if tableHeaderIdx == -1 || noteIdx > tableHeaderIdx {
		t.Errorf("smoke-test note must appear BEFORE the table header (noteIdx=%d, tableHeaderIdx=%d)", noteIdx, tableHeaderIdx)
	}

	// (2) The note names WHY: model held constant -> can't discriminate.
	for _, want := range []string{"model", "held constant", "adapter is wired correctly"} {
		if !strings.Contains(out, want) {
			t.Errorf("smoke-test note missing expected phrase %q; got:\n%s", want, out)
		}
	}

	// (3) structured/stream are explicitly named as reusing the QA task list
	// — VERIFIED true at tasks.go's buildWork switch (mode in {qa,structured,
	// stream} all call buildQAStyle over the same filterKind(tasks,
	// "single_shot_qa") set).
	if !strings.Contains(out, "structured") || !strings.Contains(out, "stream") {
		t.Errorf("smoke-test note must name structured and stream as sharing the QA task list; got:\n%s", out)
	}

	// (4) The modes with real discriminating signal are named.
	for _, want := range []string{"memory", "rag", "tools"} {
		if !strings.Contains(out, want) {
			t.Errorf("smoke-test note must name the discriminating mode %q; got:\n%s", want, out)
		}
	}

	// (5) The per-row Mode-signal cells for qa/structured/stream carry the
	// smoke-test marker AT THE ROW ITSELF (not only in the prose note above),
	// and the memory row is marked as discriminating instead.
	if !strings.Contains(out, "smoke test, not a quality signal") {
		t.Errorf("no per-row smoke-test marker found in the Mode-signal column; got:\n%s", out)
	}
	memRowIdx := strings.Index(out, "| aisotropy | memory |")
	if memRowIdx == -1 {
		t.Fatalf("memory row not found; got:\n%s", out)
	}
	memRowEnd := strings.Index(out[memRowIdx:], "\n")
	memRow := out[memRowIdx : memRowIdx+memRowEnd]
	if !strings.Contains(memRow, "discriminates") {
		t.Errorf("memory row does not mark itself as discriminating: %q", memRow)
	}

	// (6) qa/structured/stream rows are STILL rendered (the modes keep
	// running — only the framing/labels change, per the task constraint).
	for _, mode := range []string{"qa", "structured", "stream"} {
		if !strings.Contains(out, "| aisotropy | "+mode+" |") {
			t.Errorf("mode %s row missing from the rendered table — modes must keep running, only labels change; got:\n%s", mode, out)
		}
	}
}

// TestModeSignal_QAStructuredStreamCarrySmokeTestMarker is the narrow unit
// test on modeSignal directly (no full render), pinning exactly which modes
// get the smoke-test marker and which get the discriminates marker.
func TestModeSignal_QAStructuredStreamCarrySmokeTestMarker(t *testing.T) {
	smokeModes := []string{"qa", "structured", "stream"}
	for _, mode := range smokeModes {
		a := &modeAgg{structN: 1, structOK: 1, ttftN: 1, ttftSum: 100}
		got := modeSignal(mode, a)
		if !strings.Contains(got, "smoke test") {
			t.Errorf("modeSignal(%q, ...) = %q, want it to contain \"smoke test\"", mode, got)
		}
	}
	discriminatingModes := map[string]*modeAgg{
		"memory": {},
		"rag":    {groundedN: 2, retrievalHits: 2, trapN: 1, trapRefused: 1},
		"tools":  {toolN: 2, toolExec: 2, toolCorrect: 2},
	}
	for mode, a := range discriminatingModes {
		got := modeSignal(mode, a)
		if strings.Contains(got, "smoke test") {
			t.Errorf("modeSignal(%q, ...) = %q — a discriminating mode must not carry the smoke-test marker", mode, got)
		}
		if !strings.Contains(got, "discriminates") {
			t.Errorf("modeSignal(%q, ...) = %q, want it to contain \"discriminates\"", mode, got)
		}
	}
}
