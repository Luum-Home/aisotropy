package main

// subject.go — what each dimension actually MEASURES.
//
// WHY THIS EXISTS
// ---------------
// The arena holds the model constant so that a difference between two cells is
// attributable to the framework. That reasoning is sound for the dimensions
// where the framework does the work — who stores a turn, under which key, what
// survives a kill -9. It does NOT hold for `qa`, `structured` or `stream`, where
// every framework hands the SAME prompt to the SAME model and the model does
// essentially all of it. Those cells cannot discriminate frameworks BY
// CONSTRUCTION: the variable was deliberately held fixed.
//
// Rendered as one undifferentiated check mark, a `qa` pass and a `memory_scoped`
// pass read as the same kind of statement. They are not. One says "the model
// answered", which every row shares; the other says "this framework's storage
// kept two tenants apart".
//
// This is the same medicine applied all day — declare the provenance instead of
// showing nine identical ticks — one level up: from "where did this capability
// come from" to "what is this dimension even about".
//
// Deliberately NOT a score, a weight, or a filter. It changes nothing about how
// a cell is computed; it tells the reader which question the cell answers, which
// is the difference between a benchmark and a scoreboard.

// measurementSubject is what a dimension's pass/fail is a statement about.
type measurementSubject struct {
	Subject string // MODEL | FRAMEWORK | INTEGRATION | MIXED
	Note    string // one line: what a pass actually claims
}

const (
	subjModel       = "MODEL"
	subjFramework   = "FRAMEWORK"
	subjIntegration = "INTEGRATION"
	subjMixed       = "MIXED"
)

// dimensionSubjects classifies every mode and dimension the runner can execute.
// A dimension missing from this map renders as unclassified rather than being
// silently assumed — an unlabelled cell is exactly the state this file exists to
// remove.
var dimensionSubjects = map[string]measurementSubject{
	// ---- the model does the work; the model is held constant -----------------
	"qa": {subjModel,
		"the model answered a question every framework passed it identically — a control lane, not a framework comparison"},
	"structured": {subjModel,
		"the model emitted schema-valid output; the framework's contribution is the schema instruction and any repair loop"},
	"stream": {subjModel,
		"the model streamed tokens; the framework's contribution is the transport, which TTFT measures better than the pass/fail does"},
	"safeguard": {subjModel,
		"the MODEL refused or complied. Deterministically scored, but what is scored is a model behaviour: a framework can barely influence it beyond the system prompt it forwards"},

	// ---- the framework's plumbing does the work ------------------------------
	"memory": {subjFramework,
		"a fact written in one process was re-hydrated in another — by a framework memory primitive, or by adapter code standing in for one (see the cell's L-label)"},
	"memory_cross_session": {subjFramework,
		"recall survived a change of session under one user — which needs a USER axis the framework may or may not provide"},
	"memory_scoped": {subjFramework,
		"a compound scope kept tenants apart — by construction (L3) or by a key the caller must remember to pass (L1)"},
	"isolation_leak": {subjFramework,
		"one tenant's write never surfaced to another"},
	"isolation_forgetful": {subjFramework,
		"the scope boundary held when the integrator FORGOT to pass the identity — the case that separates a padlock from a safe"},
	"crash_recovery": {subjFramework,
		"state survived a hard kill -9 — via a framework checkpointer (L2) or adapter-written checkpointing (L1)"},
	"middleware": {subjFramework,
		"a registered middleware transformed the request inbound and the stream outbound, through the framework's own seam"},
	"observability": {subjFramework,
		"spans reached an in-process collector — natively, or across a callback→OTel bridge"},
	"wasm_tool": {subjFramework,
		"a tool body executed inside a WASM guest. NOTE: this criterion names a MECHANISM only our implementation uses — see docs/architecture/arena-wasm-tool-dimension.md before reading it as a comparison"},

	// ---- both, and the split is the point ------------------------------------
	"rag": {subjMixed,
		"retrieval is the framework's (hit@1 is a plumbing number); the ANSWER is the model's. Read hit@1 for the framework and the quality score for the pair"},
	"tools": {subjMixed,
		"the MODEL decides to call and with which arguments; the FRAMEWORK dispatches and folds the result back. A failure can be either, and the per-item detail says which"},
	"memory_semantic_recall": {subjMixed,
		"retrieval by meaning: the embedder and store are plumbing, the phrasing that must be matched is the model's"},
	"memory_consolidation": {subjMixed,
		"summarising many turns into a durable fact — the framework schedules it, the model writes it"},
}

// subjectOf returns the classification for a dimension, and whether one exists.
func subjectOf(dim string) (measurementSubject, bool) {
	s, ok := dimensionSubjects[dim]
	return s, ok
}

// unclassifiedDimensions returns the executed dimensions that carry no subject,
// so a new mode cannot quietly render without declaring what it measures.
func unclassifiedDimensions(dims []string) []string {
	var out []string
	for _, d := range dims {
		if _, ok := dimensionSubjects[d]; !ok {
			out = append(out, d)
		}
	}
	return out
}
