package main

// matrix.go — the full-matrix completeness gate (arenas/live/FULL-MATRIX.md,
// arenas/live/MATRIX-SCHEMA.md).
//
// Each adapter ships arenas/live/<fw>/matrix.json declaring, per dimension it
// implements, the status of each (lane, form) cell: pass | fail |
// infeasible:<reason> | pending. This file discovers every such manifest,
// validates the status vocabulary, and FAILS the run if any cell is left
// "pending" (or carries an invalid status) — a silent blank is an arena
// defect, not a passable state. It also renders the full matrix into
// RESULTS.md so a gap is visible, not just machine-checked.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ---- status vocabulary ------------------------------------------------------

const (
	statusPass       = "pass"
	statusFail       = "fail"
	statusPending    = "pending"
	infeasiblePrefix = "infeasible:"
)

// validMatrixStatus reports whether s is one of the four allowed status
// forms: pass, fail, pending, or infeasible:<non-empty reason>. Anything else
// (empty string, "n/a", "skip", "todo", a bare "infeasible" with no reason,
// …) is invalid — the schema is deliberately closed so a typo cannot pass as
// a covered cell.
func validMatrixStatus(s string) bool {
	switch s {
	case statusPass, statusFail, statusPending:
		return true
	}
	if strings.HasPrefix(s, infeasiblePrefix) {
		return strings.TrimSpace(strings.TrimPrefix(s, infeasiblePrefix)) != ""
	}
	return false
}

// isFeasibleGap reports whether s is an infeasible cell, and if so, its
// reason text.
func infeasibleReason(s string) (string, bool) {
	if !strings.HasPrefix(s, infeasiblePrefix) {
		return "", false
	}
	return strings.TrimPrefix(s, infeasiblePrefix), true
}

// ---- manifest shape ---------------------------------------------------------

// matrixForms is one model-lane's status across the three execution forms.
type matrixForms struct {
	Native string `json:"native"`
	Docker string `json:"docker"`
	Wasm   string `json:"wasm"`
}

// cells returns the three (form, status) pairs in a fixed, documented order.
func (f matrixForms) cells() []struct{ form, status string } {
	return []struct{ form, status string }{
		{"native", f.Native},
		{"docker", f.Docker},
		{"wasm", f.Wasm},
	}
}

// matrixLanes is one dimension's status across both model lanes.
type matrixLanes struct {
	Stub matrixForms `json:"stub"`
	Real matrixForms `json:"real"`
}

// lanes returns the two (lane, matrixForms) pairs in a fixed, documented
// order.
func (l matrixLanes) lanes() []struct {
	lane  string
	forms matrixForms
} {
	return []struct {
		lane  string
		forms matrixForms
	}{
		{"stub", l.Stub},
		{"real", l.Real},
	}
}

// matrixDoc is one adapter's full-matrix manifest (arenas/live/<fw>/matrix.json).
type matrixDoc struct {
	Framework  string                 `json:"framework"`
	Dimensions map[string]matrixLanes `json:"dimensions"`

	sourcePath string // set by loadMatrixDocs, not part of the JSON contract
}

// ---- discovery + parsing -----------------------------------------------------

// loadMatrixDocs discovers every arenas/live/<fw>/matrix.json under liveDir and
// parses it. A directory with no matrix.json is simply absent from the
// result — matrix.json is opt-in per adapter until the per-adapter fill pass
// lands (see arenas/live/MATRIX-SCHEMA.md). A matrix.json that fails to parse,
// or whose "framework" field does not match its directory name, is a hard
// error: a malformed manifest is worse than a missing one because it can hide
// behind an apparent presence.
func loadMatrixDocs(liveDir string) ([]matrixDoc, error) {
	matches, err := filepath.Glob(filepath.Join(liveDir, "*", "matrix.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)

	var docs []matrixDoc
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", m, err)
		}
		var doc matrixDoc
		if err := json.Unmarshal(b, &doc); err != nil {
			return nil, fmt.Errorf("parse %s: %w", m, err)
		}
		doc.sourcePath = m
		wantName := filepath.Base(filepath.Dir(m))
		if doc.Framework == "" {
			return nil, fmt.Errorf("%s: missing required \"framework\" field", m)
		}
		if doc.Framework != wantName {
			return nil, fmt.Errorf("%s: \"framework\":%q does not match its directory %q", m, doc.Framework, wantName)
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

// ---- the completeness gate ---------------------------------------------------

// matrixGapKind distinguishes a still-open cell from a malformed one so the
// error report can lead with the more actionable class first.
type matrixGap struct {
	framework string
	dimension string
	lane      string
	form      string
	status    string
	invalid   bool // true when status fails validMatrixStatus (not just "pending")
}

// gateMatrices walks every discovered manifest and reports every cell that is
// still "pending" or carries a status outside the closed vocabulary. It
// returns nil only when EVERY cell of EVERY manifest is pass, fail, or a
// non-empty infeasible:<reason> — i.e. the full matrix, as far as manifests
// exist, has no blanks. An empty docs slice (no manifests found yet) passes
// trivially: matrix.json is opt-in until an adapter's fill pass lands, and the
// gate's job is "no blanks where a manifest claims coverage", not "every
// adapter must already have one".
func gateMatrices(docs []matrixDoc) error {
	var gaps []matrixGap
	for _, doc := range docs {
		dims := sortedDimNames(doc.Dimensions)
		for _, dim := range dims {
			ml := doc.Dimensions[dim]
			for _, l := range ml.lanes() {
				for _, c := range l.forms.cells() {
					if validMatrixStatus(c.status) && c.status != statusPending {
						continue
					}
					gaps = append(gaps, matrixGap{
						framework: doc.Framework,
						dimension: dim,
						lane:      l.lane,
						form:      c.form,
						status:    c.status,
						invalid:   c.status != statusPending && !validMatrixStatus(c.status),
					})
				}
			}
		}
	}
	if len(gaps) == 0 {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d matrix cell(s) are not covered (pending or invalid status) across %d manifest(s):\n", len(gaps), len(docs))
	for _, g := range gaps {
		label := "pending"
		if g.invalid {
			label = fmt.Sprintf("invalid status %q (want pass|fail|infeasible:<reason>|pending)", g.status)
		}
		fmt.Fprintf(&b, "  - %s / %s / %s / %s: %s\n", g.framework, g.dimension, g.lane, g.form, label)
	}
	b.WriteString("Fill every cell with pass, fail, or infeasible:<reason> — see arenas/live/MATRIX-SCHEMA.md. " +
		"A blank matrix cell is an arena defect (arenas/live/FULL-MATRIX.md \"no-blank-cells law\"), not a passable state.")
	return fmt.Errorf("%s", b.String())
}

func sortedDimNames(m map[string]matrixLanes) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- RESULTS.md rendering ----------------------------------------------------

// matrixCellGlyph renders one status for the RESULTS.md grid: pass/fail as
// glyphs, infeasible with its reason inline (truncated for table width), and
// pending flagged loudly (it should never survive to a report the gate
// allowed through, but the renderer must not assume the gate ran — e.g.
// -skip-matrix-gate — so it renders pending honestly too).
func matrixCellGlyph(status string) string {
	switch status {
	case statusPass:
		return sigPass
	case statusFail:
		return sigFail
	case statusPending:
		return "⧗ pending"
	}
	if reason, ok := infeasibleReason(status); ok {
		return "infeasible: " + tail(reason, 50)
	}
	if status == "" {
		return sigUnknown
	}
	return "invalid:" + tail(status, 30)
}

// writeMatrixSection renders the full-matrix coverage grid: one table per
// adapter, dimension rows × (lane, form) columns. A missing manifest is
// reported as a single "no matrix.json yet" line, never silently skipped —
// the section itself always states which adapters lack coverage manifests.
func writeMatrixSection(b *strings.Builder, rep reportData) {
	if len(rep.MatrixDocs) == 0 && len(rep.Frameworks) == 0 {
		return
	}
	b.WriteString("## Full-matrix coverage — `matrix.json` (adapter × dimension × lane × form)\n\n")
	b.WriteString("Per `arenas/live/FULL-MATRIX.md`: every (dimension, lane, form) cell is either a\n")
	b.WriteString("real result or an explicit `infeasible:<reason>` — never a silent blank. Source:\n")
	b.WriteString("each adapter's own `matrix.json` (schema: `arenas/live/MATRIX-SCHEMA.md`).\n\n")

	byFw := map[string]matrixDoc{}
	for _, d := range rep.MatrixDocs {
		byFw[d.Framework] = d
	}

	fws := rep.Frameworks
	if len(fws) == 0 {
		for _, d := range rep.MatrixDocs {
			fws = append(fws, d.Framework)
		}
		sort.Strings(fws)
	}

	for _, fw := range fws {
		doc, ok := byFw[fw]
		if !ok {
			fmt.Fprintf(b, "### %s\n\nno `matrix.json` yet — coverage not declared.\n\n", fw)
			continue
		}
		fmt.Fprintf(b, "### %s\n\n", fw)
		b.WriteString("| Dimension | stub/native | stub/docker | stub/wasm | real/native | real/docker | real/wasm |\n")
		b.WriteString("|---|---|---|---|---|---|---|\n")
		for _, dim := range sortedDimNames(doc.Dimensions) {
			ml := doc.Dimensions[dim]
			fmt.Fprintf(b, "| `%s` | %s | %s | %s | %s | %s | %s |\n",
				dim,
				matrixCellGlyph(ml.Stub.Native), matrixCellGlyph(ml.Stub.Docker), matrixCellGlyph(ml.Stub.Wasm),
				matrixCellGlyph(ml.Real.Native), matrixCellGlyph(ml.Real.Docker), matrixCellGlyph(ml.Real.Wasm),
			)
		}
		if len(doc.Dimensions) == 0 {
			b.WriteString("_(matrix.json present but declares zero dimensions)_\n")
		}
		b.WriteString("\n")
	}
}
