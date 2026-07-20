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
