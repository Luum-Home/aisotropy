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
	passPrefix       = "pass:"
)

// validMatrixStatus reports whether s is one of the allowed status forms:
// pass, fail, pending, infeasible:<non-empty reason>, or
// pass:<non-empty provenance>. Anything else (empty string, "n/a", "skip",
// "todo", a bare "infeasible" with no reason, …) is invalid — the schema is
// deliberately closed so a typo cannot pass as a covered cell.
//
// pass:<provenance> exists because "it passed" can be true at more than one
// level, and collapsing those to one glyph publishes a claim the run did not
// make. memory_cross_session is the case that forced it: recalling a fact
// across two sessions of the same user requires a USER axis, and for five of
// the eight adapters the framework supplies none — the adapter synthesizes it
// at app level. Three of those five synthesized it and passed; two refused on
// fairness grounds and failed. Same framework capability, opposite board
// outcome, decided by which adapter author had scruples. Labelling the pass
// with where the axis CAME FROM lets every adapter run the dimension while the
// cell still tells a reader whether the framework guaranteed the scope or the
// adapter hand-rolled it. See docs/architecture/arena-fairness-protocol.md §4.
func validMatrixStatus(s string) bool {
	switch s {
	case statusPass, statusFail, statusPending:
		return true
	}
	if strings.HasPrefix(s, infeasiblePrefix) {
		return strings.TrimSpace(strings.TrimPrefix(s, infeasiblePrefix)) != ""
	}
	if strings.HasPrefix(s, passPrefix) {
		return strings.TrimSpace(strings.TrimPrefix(s, passPrefix)) != ""
	}
	return false
}

// passProvenance reports whether s is a labelled pass, and if so, its
// provenance text. A labelled pass IS a pass everywhere it is counted or
// gated: validMatrixStatus accepts it, so gateMatrices treats it as a filled
// cell exactly like a bare pass. The label rides along for the renderer; it
// never demotes the cell.
func passProvenance(s string) (string, bool) {
	if !strings.HasPrefix(s, passPrefix) {
		return "", false
	}
	return strings.TrimPrefix(s, passPrefix), true
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

	// Candidate marks a WORK-IN-PROGRESS adapter whose matrix is legitimately
	// incomplete because verifying some cells is blocked on something external
	// (e.g. a real-lane cell that needs the provider, but the free-tier quota is
	// exhausted; or a docker/native-image build not yet exercised). A candidate
	// is EXCLUDED from the completeness gate — but it is NOT a silent omission:
	// gateMatrices' caller logs every candidate loudly, and it renders labeled
	// "CANDIDATE" in RESULTS.md. This is the honest middle path between faking a
	// `pass` on an unverified cell and dropping the adapter entirely — it keeps
	// the no-blank-cells law's INTENT (no SILENT gaps) while letting a genuine
	// WIP land. Clear the flag once every cell is a real result.
	Candidate bool `json:"candidate,omitempty"`

	// CandidateReason states WHY this adapter is still a candidate, in its own
	// words. Required whenever Candidate is true.
	//
	// The runner used to print a guessed reason ("real lane / build not yet
	// verified — e.g. quota-blocked") for every candidate, and the guess was
	// wrong for the only adapter that carries the flag: spring-ai is a candidate
	// because its 17 DOCKER cells were never run, which nobody would infer from
	// a message about quota. A warning that invents its own reason teaches
	// readers to ignore warnings — and this one is the single line standing
	// between "these cells are labelled CANDIDATE" and someone reading them as
	// results.
	CandidateReason string `json:"candidate_reason,omitempty"`

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
// candidateFrameworks returns the names of every matrix marked candidate=true,
// so the caller can log the completeness-gate exclusion loudly (never silent).
func candidateFrameworks(docs []matrixDoc) []string {
	var out []string
	for _, doc := range docs {
		if doc.Candidate {
			out = append(out, doc.Framework)
		}
	}
	sort.Strings(out)
	return out
}

// candidateWarnings renders one "framework (declared reason)" line per candidate.
//
// Separate from candidateFrameworks on purpose: that one answers "which adapters
// are exempt" (used by the gate), this one answers "and why, in their own words"
// (used by the operator-facing warning). Folding the reason into the first would
// have made a gate's data structure carry presentation — and did, briefly, until
// its own test caught it.
func candidateWarnings(docs []matrixDoc) []string {
	var out []string
	for _, doc := range docs {
		if !doc.Candidate {
			continue
		}
		reason := strings.TrimSpace(doc.CandidateReason)
		if reason == "" {
			// Never invent one. An unexplained exclusion from the completeness
			// gate is the no-blank-cells law defeated one level up: the adapter
			// is exempt and nobody can say from what.
			reason = "NO candidate_reason DECLARED — add one to " + doc.sourcePath
		}
		out = append(out, doc.Framework+" ("+reason+")")
	}
	sort.Strings(out)
	return out
}

func gateMatrices(docs []matrixDoc) error {
	var gaps []matrixGap
	for _, doc := range docs {
		if doc.Candidate {
			continue // WIP candidate — excluded from completeness (logged by the caller)
		}
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

// undeclaredDimensions reports, per framework, the dimensions that OTHER
// manifests declare and this one does not mention at all.
//
// THE LOOPHOLE THIS CLOSES. gateMatrices asks "is every cell you DECLARED
// filled?" — nothing asked "did you declare every dimension?". So a manifest
// could earn a clean gate by simply never mentioning a dimension, and the
// no-blank-cells law (arenas/live/FULL-MATRIX.md: every cell is a result or an
// explicit infeasible:<reason>, never a silent blank) would not notice: the
// blank is not an empty cell, it is an absent ROW.
//
// Two live instances found it, and the asymmetry runs in both directions:
//   - spring-ai declares 11 dimensions where its peers declare 16 — five
//     competitor rows silently missing.
//   - wasm_tool is declared by aisotropy ALONE. That is OUR differentiator
//     dimension, and every competitor is silently absent from it rather than
//     carrying an explicit infeasible:<reason>. A reader sees one ✓ and no
//     rows, which reads as "untested" when the truth is a per-framework
//     structural blocker that we know and could state.
//
// The second one is why this is a WARNING the run always prints rather than
// something left to a reviewer's memory: the arena's whole claim is being
// apples-to-apples, and a gap that flatters the house is the one nobody else
// is motivated to find.
//
// Deliberately NOT a hard gate failure. A `pending` cell is a promise unkept
// and blocks. An undeclared dimension may be genuinely out of an adapter's
// scope, and deciding which it is needs the operator and a TRUE reason —
// inventing eight infeasibility reasons to silence a warning would be the
// fabrication the law exists to prevent.
func undeclaredDimensions(docs []matrixDoc) map[string][]string {
	union := map[string]bool{}
	for _, doc := range docs {
		for dim := range doc.Dimensions {
			union[dim] = true
		}
	}
	out := map[string][]string{}
	for _, doc := range docs {
		var missing []string
		for dim := range union {
			if _, ok := doc.Dimensions[dim]; !ok {
				missing = append(missing, dim)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			out[doc.Framework] = missing
		}
	}
	return out
}

// undeclaredWarnings renders one line per framework with undeclared dimensions.
func undeclaredWarnings(docs []matrixDoc) []string {
	byFw := undeclaredDimensions(docs)
	fws := make([]string, 0, len(byFw))
	for fw := range byFw {
		fws = append(fws, fw)
	}
	sort.Strings(fws)
	out := make([]string, 0, len(fws))
	for _, fw := range fws {
		out = append(out, fmt.Sprintf("%s does not declare: %s", fw, strings.Join(byFw[fw], ", ")))
	}
	return out
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
	// A labelled pass renders as a pass PLUS its provenance, never as a bare
	// glyph — the label is the whole point: it is what stops "passed" from
	// implying the framework guaranteed something the adapter supplied.
	if prov, ok := passProvenance(status); ok {
		return sigPass + " " + tail(prov, 50)
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

// ---- declared-infeasible lookup ----------------------------------------------

// declaredInfeasible maps framework -> dimension -> reason for every dimension a
// manifest declares INFEASIBLE in all of its declared cells.
//
// WHY THE RUNNER MUST CONSULT THIS BEFORE BUILDING WORK. The correctness pass
// used to build every mode for every framework and let the adapter sort it out.
// For a mode an adapter does not implement, the adapter exits non-zero with
// `unknown mode "isolation_forgetful"`, which the runner records as an ERRORED
// cell — indistinguishable, in the report, from an adapter that tried and broke.
// Observed on the 2026-07-27 board: 4 such cells across eino and genkit-go
// (ai-sdk too, once its manifest is filled), inflating the error rate with
// something that is not a failure at all.
//
// That is precisely the blank the no-blank-cells law exists to prevent, arriving
// through the back door: the manifests already SAID these cells were infeasible,
// with a reason, and the runner ignored them and generated an error instead. A
// declaration nobody reads is not a declaration.
//
// The three frameworks in question are infeasible for the same, code-verified
// reason, not for lack of adapter work: `isolation_forgetful` omits the user
// scope at the FRAMEWORK's own API boundary to see whether the framework
// enforces it. Where the scope is hand-rolled adapter glue — eino
// (mode_scoped_memory.go: "Eino has NO native multi-tenant memory isolation
// primitive"), genkit-go (rqlite_client.go: "Genkit-Go has NO native
// multi-tenant memory-isolation primitive"), ai-sdk (index.mjs: "HAND-ROLLED
// compound scope") — there is no framework boundary to omit anything at, and a
// blank user id merely misses OUR OWN lookup. Running it would measure the
// adapter and report it as the framework. Pydantic AI already declared exactly
// this; the other three simply had not.
//
// Conservative by construction: a dimension counts as infeasible only when every
// declared cell says so. One `pass`/`fail`/`pending` cell anywhere in that
// dimension means the work still gets built.
func declaredInfeasible(docs []matrixDoc) map[string]map[string]string {
	out := make(map[string]map[string]string, len(docs))
	for _, doc := range docs {
		for dim, lanes := range doc.Dimensions {
			reason, all := "", true
			seen := 0
			for _, l := range lanes.lanes() {
				for _, c := range l.forms.cells() {
					if c.status == "" {
						continue
					}
					seen++
					r, ok := infeasibleReason(c.status)
					if !ok {
						all = false
						break
					}
					if reason == "" {
						reason = r
					}
				}
				if !all {
					break
				}
			}
			if all && seen > 0 {
				if out[doc.Framework] == nil {
					out[doc.Framework] = map[string]string{}
				}
				out[doc.Framework][dim] = reason
			}
		}
	}
	return out
}
