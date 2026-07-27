package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Model profiles — the declared answer to "what was this measured WITH".
//
// WHY THIS IS A GATE AND NOT A DOC. A board header naming `poolside/laguna-s-2.1`
// is provenance, and provenance was never the problem. The problem is that the
// name alone says nothing about whether the model was BUILT for the workload the
// arena measures, and a reader — including the people who produced the board —
// will default to reading a low cell as a framework weakness.
//
// On 2026-07-27 a full board ran on a model whose published results are
// Terminal-Bench, SWE-Bench Multilingual and DeepSWE. This arena measures a
// general assistant: QA, retrieval, memory, tool orchestration, structured
// output, safeguards. On top of that, the vendor's headline numbers are reported
// with a thinking mode the arena never enables (Terminal-Bench 70.2 vs 60.4;
// DeepSWE 40.4 vs 16.5). Several results were on their way to being written up as
// provider or framework defects before anyone asked what the model actually is.
//
// Making the profile a doc would repeat that: docs get written when someone
// remembers. Making it a GATE that runs before the first billable call means a
// board cannot exist without its own reading instructions — the same discipline
// as the matrix-completeness and determinism gates.

// modelProfile is the declared characterisation of one model. Every field exists
// because its absence produced a real misreading.
type modelProfile struct {
	Model          string `json:"model"`
	Vendor         string `json:"vendor"`
	Specialisation string `json:"specialisation"`
	// WorkloadFit against THIS arena's general-assistant workload: aligned,
	// partial, or mismatched. Blunt on purpose — "mismatched" does not invalidate
	// a board, it changes how every cell must be read.
	WorkloadFit string `json:"workload_fit"`
	FitNote     string `json:"fit_note"`

	ReasoningMode struct {
		Supported      bool   `json:"supported"`
		EnabledByArena bool   `json:"enabled_by_arena"`
		Note           string `json:"note"`
	} `json:"reasoning_mode"`

	PublishedBenchmarks map[string]string `json:"published_benchmarks"`
	APIQuirks           []string          `json:"api_quirks"`
	Sources             []string          `json:"sources"`

	sourcePath string
}

var validWorkloadFit = map[string]bool{"aligned": true, "partial": true, "mismatched": true}

// profileFileName maps a model id to its profile filename: '/' is not legal in a
// path component, so `poolside/laguna-s-2.1` lives at
// `poolside_laguna-s-2.1.json`.
func profileFileName(model string) string {
	return strings.ReplaceAll(model, "/", "_") + ".json"
}

// loadModelProfile reads and validates the profile for a model id.
//
// Every failure mode returns an error naming the exact file to write, because a
// gate that blocks without telling you how to satisfy it is a gate people bypass.
func loadModelProfile(liveDir, model string) (*modelProfile, error) {
	path := filepath.Join(liveDir, "models", profileFileName(model))
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf(
			"no model profile for %q — write %s before running a board (see arenas/live/models/README.md). "+
				"A board that cannot state what it measured with, and whether that model fits this workload, "+
				"is not publishable evidence: %w", model, path, err)
	}
	var p modelProfile
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	p.sourcePath = path

	if p.Model != model {
		return nil, fmt.Errorf("%s: declares \"model\":%q but OPENAI_MODEL is %q — a profile describing a different model is worse than none", path, p.Model, model)
	}
	if !validWorkloadFit[p.WorkloadFit] {
		return nil, fmt.Errorf("%s: \"workload_fit\" is %q, want one of aligned|partial|mismatched", path, p.WorkloadFit)
	}
	if strings.TrimSpace(p.FitNote) == "" {
		return nil, fmt.Errorf("%s: \"fit_note\" is empty — the one sentence a reader needs before any number is exactly what this gate exists to force", path)
	}
	if len(p.Sources) == 0 {
		return nil, fmt.Errorf("%s: \"sources\" is empty — a characterisation with no citation is a claim about someone else's product with no evidence (see .claude/rules/capability-absence-claims.md)", path)
	}
	return &p, nil
}

// readingCaveats returns the lines a reader must see BEFORE the tables, ordered
// most-distorting first. Empty when the model is aligned and has no uncontrolled
// condition — silence is then accurate, rather than boilerplate nobody reads.
func (p *modelProfile) readingCaveats() []string {
	var out []string
	switch p.WorkloadFit {
	case "mismatched":
		out = append(out, fmt.Sprintf(
			"**Workload MISMATCH.** %s is specialised for %s, while this arena measures a general assistant. %s",
			p.Model, p.Specialisation, p.FitNote))
	case "partial":
		out = append(out, fmt.Sprintf("**Partial workload fit.** %s", p.FitNote))
	}
	if p.ReasoningMode.Supported && !p.ReasoningMode.EnabledByArena {
		out = append(out, fmt.Sprintf(
			"**Uncontrolled reasoning-mode condition.** %s", p.ReasoningMode.Note))
	}
	if len(p.APIQuirks) > 0 {
		out = append(out, fmt.Sprintf(
			"**%d provider API quirk(s) affect frameworks unevenly** — a quirk that rejects one framework's default payload and not another's is a property of the endpoint, not of the frameworks: %s",
			len(p.APIQuirks), strings.Join(p.APIQuirks, " | ")))
	}
	return out
}
