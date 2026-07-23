package main

import (
	"strings"
	"testing"

	"aisotropy-live-runner/scoring"
)

// TestDetScoreCell_ErrorsRenderInsideScoreCell is the regression test for the
// honesty defect described in RESULTS.md's genkit-go/structured row: a cell
// like "3/3 ✓" with Runs=6, Err=3 reads as a perfect score when half the
// invocations never produced an answer. The fix renders the error count
// INSIDE the score cell itself, not only in the far-right Err column.
func TestDetScoreCell_ErrorsRenderInsideScoreCell(t *testing.T) {
	cases := []struct {
		name string
		agg  modeAgg
		want string
	}{
		{
			name: "all scored pass, no errors",
			agg:  modeAgg{detN: 3, detPassN: 3, errors: 0},
			want: "3/3 ✓",
		},
		{
			name: "the genkit-go/structured case: 3 scored (all pass) + 3 errored of 6 runs",
			agg:  modeAgg{detN: 3, detPassN: 3, errors: 3},
			want: "3/3 ✓ (3 err)",
		},
		{
			name: "partial pass with errors",
			agg:  modeAgg{detN: 2, detPassN: 1, errors: 1},
			want: "1/2 ✗ (1 err)",
		},
		{
			name: "every run errored — nothing was ever scored",
			agg:  modeAgg{detN: 0, detPassN: 0, errors: 3},
			want: "— (3 err)",
		},
		{
			name: "no runs at all",
			agg:  modeAgg{detN: 0, detPassN: 0, errors: 0},
			want: "—",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := detScoreCell(&c.agg)
			if got != c.want {
				t.Errorf("detScoreCell(%+v) = %q, want %q", c.agg, got, c.want)
			}
			if c.agg.errors > 0 && !strings.Contains(got, "err") {
				t.Errorf("detScoreCell(%+v) = %q — error count must appear inside the score cell", c.agg, got)
			}
		})
	}
}

// TestWriteResults_ErroredRunsVisibleInScoreColumn drives the full renderer
// (writeResults -> writeQualityTable) with synthetic records reproducing the
// exact genkit-go/structured shape from the committed RESULTS.md (3 scored
// + 3 errored of 6 total runs) and asserts:
//  1. the score cell in the rendered markdown table carries the "(N err)"
//     marker, not just the far-right Runs/Err columns;
//  2. a run-level summary line appears near the top of the correctness
//     section, before the table;
//  3. a legend line explains the denominator.
func TestWriteResults_ErroredRunsVisibleInScoreColumn(t *testing.T) {
	records := []record{
		// 3 scored runs, all pass.
		{Framework: "genkit-go", Mode: "structured", ID: "s1", Kind: "structured",
			Det: scoring.Score{Value: 1, Pass: true, Detail: "valid JSON"}, StructuredOK: true},
		{Framework: "genkit-go", Mode: "structured", ID: "s2", Kind: "structured",
			Det: scoring.Score{Value: 1, Pass: true, Detail: "valid JSON"}, StructuredOK: true},
		{Framework: "genkit-go", Mode: "structured", ID: "s3", Kind: "structured",
			Det: scoring.Score{Value: 1, Pass: true, Detail: "valid JSON"}, StructuredOK: true},
		// 3 errored runs — retry exhaustion, never reached scoring.
		{Framework: "genkit-go", Mode: "structured", ID: "s4", Kind: "structured",
			RunErr: "invocation 1/1: adapter exec: context deadline exceeded"},
		{Framework: "genkit-go", Mode: "structured", ID: "s5", Kind: "structured",
			RunErr: "invocation 1/1: adapter exec: context deadline exceeded"},
		{Framework: "genkit-go", Mode: "structured", ID: "s6", Kind: "structured",
			RunErr: "invocation 1/1: adapter exec: context deadline exceeded"},
	}

	rep := reportData{
		Model:      "agnes-2.0-flash",
		Frameworks: []string{"genkit-go"},
		Modes:      []string{"structured"},
		Forms:      []string{"native"},
		Lane:       "real",
		Records:    records,
	}

	var b strings.Builder
	writeHeader(&b, rep)
	writeMatrixSection(&b, rep)

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
		if r.failed() {
			a.errors++
			continue
		}
		a.detN++
		a.detValSum += r.Det.Value
		if r.Det.Pass {
			a.detPassN++
		}
		if r.Kind == "structured" {
			a.structN++
			if r.StructuredOK {
				a.structOK++
			}
		}
	}
	writeQualityTable(&b, rep, aggs, get)

	out := b.String()

	// (1) the score cell itself carries the error marker.
	wantCell := "3/3 ✓ (3 err)"
	if !strings.Contains(out, wantCell) {
		t.Errorf("rendered table missing score cell %q; got:\n%s", wantCell, out)
	}
	// Guard against a regression that only updates the far-right Err column:
	// the literal row must have BOTH the in-cell marker and the raw Runs/Err.
	wantRow := "| genkit-go | structured | 3/3 ✓ (3 err) | 1.0 |"
	if !strings.Contains(out, wantRow) {
		t.Errorf("rendered table row does not match expected shape; want row starting %q, got:\n%s", wantRow, out)
	}

	// (2) run-level summary line appears, and appears BEFORE the table.
	summaryIdx := strings.Index(out, "**Run summary")
	tableIdx := strings.Index(out, wantCell)
	if summaryIdx == -1 {
		t.Fatalf("missing run-level summary line; got:\n%s", out)
	}
	if tableIdx == -1 || summaryIdx > tableIdx {
		t.Errorf("run summary must appear BEFORE the table's score cells (summaryIdx=%d, tableIdx=%d)", summaryIdx, tableIdx)
	}
	if !strings.Contains(out, "6 total runs") || !strings.Contains(out, "3 scored") || !strings.Contains(out, "3 errored") {
		t.Errorf("run summary missing expected counts (6 total / 3 scored / 3 errored); got:\n%s", out)
	}

	// (3) legend explains the denominator.
	if !strings.Contains(out, "Denominator legend") {
		t.Errorf("missing denominator legend under the table; got:\n%s", out)
	}
	if !strings.Contains(out, "infrastructure") {
		t.Errorf("legend must clarify an errored run is an infrastructure signal, not a framework-quality one; got:\n%s", out)
	}
}

// buildAggsFromRecords mirrors the aggregation loop in writeResults (the
// production loop is unexported and local to that function, so tests
// reproduce the same token/mode bookkeeping the way
// TestWriteResults_ErroredRunsVisibleInScoreColumn already does).
func buildAggsFromRecords(records []record) (map[string]*modeAgg, func(fw, mode string) *modeAgg) {
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
		if r.failed() {
			a.errors++
			continue
		}
		if r.HasTok {
			a.inSum += r.SumIn
			a.outSum += r.SumOut
			a.tokN++
		}
		a.detN++
		a.detValSum += r.Det.Value
		if r.Det.Pass {
			a.detPassN++
		}
	}
	return aggs, get
}

// TestFrameworkOverheadTable drives computeOverheadRows + the renderer with
// synthetic data for 3 frameworks on qa mode:
//   - "heavy": 200 avg input tok, slower bench p50, no TTFT bench cell this run
//   - "lean": 100 avg input tok (the minimum — every delta is measured off
//     this framework) plus a TTFT bench cell
//   - "noUsage": a qa record present but with NO usage data (HasTok=false,
//     i.e. the provider reported -1/absent) and no bench cell at all
//
// and asserts: ascending order by tokens, correct absolute+percent deltas,
// "noUsage" is listed (never silently dropped) as "—"/excluded rather than a
// fabricated 0, and an absent bench cell renders "—" rather than 0 or a
// fabricated value.
func TestFrameworkOverheadTable(t *testing.T) {
	records := []record{
		{Framework: "heavy", Mode: "qa", ID: "q1", Kind: "qa", HasTok: true, SumIn: 200, SumOut: 20,
			Det: scoring.Score{Value: 1, Pass: true, Detail: "exact match"}},
		{Framework: "lean", Mode: "qa", ID: "q1", Kind: "qa", HasTok: true, SumIn: 100, SumOut: 10,
			Det: scoring.Score{Value: 1, Pass: true, Detail: "exact match"}},
		{Framework: "noUsage", Mode: "qa", ID: "q1", Kind: "qa", HasTok: false,
			Det: scoring.Score{Value: 1, Pass: true, Detail: "exact match"}},
	}

	rep := reportData{
		Model:      "agnes-2.0-flash",
		Frameworks: []string{"heavy", "lean", "noUsage"}, // deliberately NOT pre-sorted by tokens
		Modes:      []string{"qa"},
		Forms:      []string{"native"},
		Lane:       "real",
		Records:    records,
		Benches: []benchCell{
			{Framework: "heavy", Form: "native", Mode: "qa", WarmP50: 80, WarmP95: 90, Iters: 3},
			{Framework: "lean", Form: "native", Mode: "qa", WarmP50: 50, WarmP95: 60, Iters: 3},
			{Framework: "lean", Form: "native", Mode: "stream", TTFTMs: 30},
			// "heavy" and "noUsage" deliberately have NO stream/TTFT bench
			// cell and "noUsage" has no bench cell at all this run.
		},
	}

	aggs, get := buildAggsFromRecords(records)

	rows := computeOverheadRows(rep, get)
	if len(rows) != 3 {
		t.Fatalf("computeOverheadRows returned %d rows, want 3; rows=%+v", len(rows), rows)
	}

	// Ascending order by avg input tokens: lean (100) then heavy (200), with
	// noUsage (no token data) sorted after every framework that has data.
	wantOrder := []string{"lean", "heavy", "noUsage"}
	for i, want := range wantOrder {
		if rows[i].Framework != want {
			t.Fatalf("row %d = %q, want %q (full order: %v)", i, rows[i].Framework, want, frameworkNames(rows))
		}
	}

	lean, heavy, noUsage := rows[0], rows[1], rows[2]

	// lean is the leanest framework — zero delta, and it's the bench/TTFT
	// baseline too.
	if !lean.HasTokens || lean.AvgIn != 100 || lean.DeltaTok != 0 || lean.DeltaPct != 0 {
		t.Errorf("lean row = %+v, want AvgIn=100 DeltaTok=0 DeltaPct=0", lean)
	}
	if !lean.HasBench || lean.BenchP50 != 50 || lean.BenchDelta != 0 {
		t.Errorf("lean bench = %+v, want BenchP50=50 BenchDelta=0 (fastest)", lean)
	}
	if !lean.HasTTFT || lean.TTFT != 30 || lean.TTFTDelta != 0 {
		t.Errorf("lean TTFT = %+v, want TTFT=30 TTFTDelta=0 (fastest, and only one with TTFT data)", lean)
	}

	// heavy: +100 tokens vs lean's 100 = +100%; bench +30ms vs lean's 50.
	if !heavy.HasTokens || heavy.AvgIn != 200 || heavy.DeltaTok != 100 || heavy.DeltaPct != 100 {
		t.Errorf("heavy row = %+v, want AvgIn=200 DeltaTok=100 DeltaPct=100", heavy)
	}
	if !heavy.HasBench || heavy.BenchP50 != 80 || heavy.BenchDelta != 30 {
		t.Errorf("heavy bench = %+v, want BenchP50=80 BenchDelta=30", heavy)
	}
	if heavy.HasTTFT {
		t.Errorf("heavy has no TTFT bench cell this run — HasTTFT must be false, got %+v", heavy)
	}

	// noUsage: no usage data -> excluded from token comparison entirely
	// (never fabricated as a 0-token row), and no bench cell -> HasBench/HasTTFT false.
	if noUsage.HasTokens {
		t.Errorf("noUsage.HasTokens = true, want false (record reported no usage this run)")
	}
	if noUsage.AvgIn != 0 || noUsage.DeltaTok != 0 {
		t.Errorf("noUsage numeric fields must stay their zero value (never a fabricated non-zero delta), got %+v", noUsage)
	}
	if noUsage.HasBench || noUsage.HasTTFT {
		t.Errorf("noUsage has no bench cell this run — HasBench/HasTTFT must both be false, got %+v", noUsage)
	}

	// Now render and check the markdown surface for the same guarantees.
	var b strings.Builder
	writeFrameworkOverheadTable(&b, rep, aggs, get)
	out := b.String()

	if !strings.Contains(out, "Framework overhead on identical work") {
		t.Fatalf("missing section heading; got:\n%s", out)
	}
	if !strings.Contains(out, "| lean | 100 | 0 (leanest) | 50 | 0 (fastest) | 30 | 0 (fastest) |") {
		t.Errorf("lean row not rendered as expected; got:\n%s", out)
	}
	if !strings.Contains(out, "| heavy | 200 | +100 (+100%) | 80 | +30 | — | — |") {
		t.Errorf("heavy row not rendered as expected; got:\n%s", out)
	}
	if !strings.Contains(out, "noUsage") || !strings.Contains(out, "excluded") {
		t.Errorf("noUsage must still be LISTED with an excluded/no-usage note, never silently dropped; got:\n%s", out)
	}
	// noUsage's row must never show a numeric 0 in the token/delta columns —
	// only the "—" placeholder.
	if strings.Contains(out, "| noUsage _(no qa usage data — excluded)_ | 0 |") {
		t.Errorf("noUsage row fabricated a 0 instead of \"—\"; got:\n%s", out)
	}
	if !strings.Contains(out, "noUsage _(no qa usage data — excluded)_ | — | — | — | — | — | — |") {
		t.Errorf("noUsage row must render every numeric column as \"—\"; got:\n%s", out)
	}

	// computeOverheadRows returns nil when qa mode isn't in play at all —
	// the section then renders nothing (no heading with an empty body).
	repNoQA := rep
	repNoQA.Modes = []string{"memory"}
	if rows := computeOverheadRows(repNoQA, get); rows != nil {
		t.Errorf("computeOverheadRows with no qa mode = %+v, want nil", rows)
	}
	var b2 strings.Builder
	writeFrameworkOverheadTable(&b2, repNoQA, aggs, get)
	if b2.Len() != 0 {
		t.Errorf("writeFrameworkOverheadTable must render nothing when qa mode is absent; got:\n%s", b2.String())
	}
}

func frameworkNames(rows []overheadRow) []string {
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.Framework
	}
	return names
}
