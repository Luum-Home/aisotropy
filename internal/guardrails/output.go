package guardrails

import (
	"context"
	"sort"
	"strings"
)

// OutputCheckInput is the payload every OutputCheck receives: the original user
// input, the model's output under inspection, and the system prompt that was in
// force. Any field may be empty.
type OutputCheckInput struct {
	Input        string
	Output       string
	SystemPrompt string
}

// OutputCheck is the extension seam for output guardrails. An integrator can
// implement it to attach an LLM-backed judge (toxicity, faithfulness,
// policy-compliance) without this package ever importing the evaluation tier.
// Implementations MUST be safe to call with a cancellable context and MUST NOT
// panic; a failed check should return a non-Suspicious Verdict (fail-open) or
// encode the failure as a Signal, per the integrator's policy.
type OutputCheck interface {
	// Name identifies the check in aggregated results.
	Name() string
	// Check inspects the output and returns an advisory Verdict.
	Check(ctx context.Context, in OutputCheckInput) Verdict
}

// toxKeyword pairs a lowercase term with the weight it contributes to the
// toxicity score. The built-in list is intentionally small and non-slur; the
// point is a cheap, deterministic first-pass heuristic, not a comprehensive
// classifier. Integrators layer an OutputCheck (LLM judge) for real coverage
// and can extend this list via WithToxicKeywords.
type toxKeyword struct {
	word   string
	weight float64
}

var builtinToxKeywords = []toxKeyword{
	{"kill you", 0.6},
	{"kill yourself", 0.9},
	{"kill", 0.4},
	{"die", 0.4},
	{"attack", 0.3},
	{"destroy you", 0.4},
	{"hate you", 0.5},
	{"hate", 0.3},
	{"idiot", 0.3},
	{"stupid", 0.3},
	{"moron", 0.3},
	{"worthless", 0.4},
	{"disgusting", 0.3},
	{"shut up", 0.3},
}

// OutputInspector runs the built-in output guardrails — a heuristic toxicity
// check and a leak check — plus any registered OutputCheck extensions.
// Construct one with NewOutputInspector. Safe for concurrent use.
type OutputInspector struct {
	redactor     *Redactor
	toxKeywords  []toxKeyword
	toxThreshold float64
	minLeakLen   int
	extra        []OutputCheck
}

// NewOutputInspector returns an OutputInspector with default heuristics. The
// redactor is reused for the leak check (to detect secrets echoed in output);
// if nil, a default Redactor is created.
func NewOutputInspector(redactor *Redactor) *OutputInspector {
	if redactor == nil {
		redactor = NewRedactor()
	}
	return &OutputInspector{
		redactor:     redactor,
		toxKeywords:  builtinToxKeywords,
		toxThreshold: 0.5,
		minLeakLen:   24,
	}
}

// WithToxicKeywords returns a copy of the inspector with extra toxicity terms
// appended to the built-in list. Words are matched case-insensitively as
// substrings; weights are clamped to 0..1.
func (o *OutputInspector) WithToxicKeywords(extra map[string]float64) *OutputInspector {
	cp := *o
	cp.toxKeywords = append([]toxKeyword(nil), o.toxKeywords...)
	for w, weight := range extra {
		if weight < 0 {
			weight = 0
		}
		if weight > 1 {
			weight = 1
		}
		cp.toxKeywords = append(cp.toxKeywords, toxKeyword{word: strings.ToLower(w), weight: weight})
	}
	return &cp
}

// WithToxicityThreshold returns a copy with the toxicity Suspicious cutoff set
// to t (clamped 0..1).
func (o *OutputInspector) WithToxicityThreshold(t float64) *OutputInspector {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	cp := *o
	cp.toxThreshold = t
	return &cp
}

// WithChecks returns a copy of the inspector with the given OutputChecks
// registered. They run in Inspect after the built-in checks.
func (o *OutputInspector) WithChecks(checks ...OutputCheck) *OutputInspector {
	cp := *o
	cp.extra = append(append([]OutputCheck(nil), o.extra...), checks...)
	return &cp
}

// Toxicity scores the output against the keyword heuristic. Suspicious is true
// when the summed weight of matched terms reaches the threshold. Score is
// capped at 1.0 and Signals name the matched terms (stable order).
func (o *OutputInspector) Toxicity(output string) Verdict {
	if output == "" {
		return Verdict{}
	}
	lower := strings.ToLower(output)
	var score float64
	var signals []string
	seen := map[string]bool{}
	for _, kw := range o.toxKeywords {
		if kw.word == "" || seen[kw.word] {
			continue
		}
		if strings.Contains(lower, kw.word) {
			seen[kw.word] = true
			signals = append(signals, "toxic:"+kw.word)
			score += kw.weight
		}
	}
	if score > 1.0 {
		score = 1.0
	}
	sort.Strings(signals)
	return Verdict{Suspicious: score >= o.toxThreshold, Signals: signals, Score: score}
}

// Leak checks whether the output leaked something it should not have: a secret
// echoed back (detected by re-running the redactor over the output) or a chunk
// of the system prompt reproduced verbatim. Any hit makes the Verdict
// Suspicious with Score 1.0 — a leak is not a matter of degree.
func (o *OutputInspector) Leak(input, output, systemPrompt string) Verdict {
	if output == "" {
		return Verdict{}
	}
	var signals []string

	// (a) Secret echo: does the output itself contain redactable secrets?
	if _, rep := o.redactor.Redact(output); rep.Redacted() {
		types := rep.Types()
		strs := make([]string, 0, len(types))
		for _, t := range types {
			strs = append(strs, "secret_echo:"+string(t))
		}
		signals = append(signals, strs...)
	}

	// (b) System-prompt echo: any non-trivial segment of the system prompt
	//     reproduced verbatim in the output.
	if segmentEchoed(systemPrompt, output, o.minLeakLen) {
		signals = append(signals, "system_prompt_echo")
	}

	sort.Strings(signals)
	if len(signals) == 0 {
		return Verdict{}
	}
	return Verdict{Suspicious: true, Signals: signals, Score: 1.0}
}

// OutputResult aggregates every output guardrail verdict. Blocked is a
// convenience: it is true when any built-in or extension check is Suspicious.
// The caller still owns the final block/allow decision.
type OutputResult struct {
	Toxicity Verdict
	Leak     Verdict
	Checks   map[string]Verdict
	Blocked  bool
}

// Inspect runs the toxicity check, the leak check, and every registered
// OutputCheck, returning the aggregated result. A nil context is tolerated.
func (o *OutputInspector) Inspect(ctx context.Context, input, output, systemPrompt string) OutputResult {
	if ctx == nil {
		ctx = context.Background()
	}
	res := OutputResult{
		Toxicity: o.Toxicity(output),
		Leak:     o.Leak(input, output, systemPrompt),
	}
	res.Blocked = res.Toxicity.Suspicious || res.Leak.Suspicious

	if len(o.extra) > 0 {
		res.Checks = make(map[string]Verdict, len(o.extra))
		in := OutputCheckInput{Input: input, Output: output, SystemPrompt: systemPrompt}
		for _, c := range o.extra {
			v := c.Check(ctx, in)
			res.Checks[c.Name()] = v
			if v.Suspicious {
				res.Blocked = true
			}
		}
	}
	return res
}

// segmentEchoed reports whether any line or sentence of src at least minLen
// characters long appears verbatim (case-insensitively) inside dst. This is the
// system-prompt-leak heuristic: it catches a model reproducing a chunk of its
// instructions without flagging on short, incidental word overlap.
func segmentEchoed(src, dst string, minLen int) bool {
	if len(src) < minLen || dst == "" {
		return false
	}
	lowerDst := strings.ToLower(dst)
	for _, seg := range splitSegments(src) {
		seg = strings.TrimSpace(seg)
		if len(seg) < minLen {
			continue
		}
		if strings.Contains(lowerDst, strings.ToLower(seg)) {
			return true
		}
	}
	return false
}

// splitSegments breaks text into candidate segments on line breaks and common
// sentence terminators, so a single long system-prompt line still yields
// checkable chunks.
func splitSegments(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		switch r {
		case '\n', '\r', '.', '!', '?', ';':
			return true
		default:
			return false
		}
	})
}
