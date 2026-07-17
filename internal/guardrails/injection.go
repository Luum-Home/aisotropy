package guardrails

import (
	"regexp"
	"sort"
)

// Verdict is the structured result of a heuristic check (injection detection
// or an output check). It is advisory: the caller decides what to do with a
// Suspicious verdict. Signals name the individual heuristics that fired, and
// Score is a 0..1 aggregate confidence — higher means "more likely a problem".
type Verdict struct {
	Suspicious bool
	Signals    []string
	Score      float64
}

// signal pairs a named heuristic with the weight it contributes to the score.
type signal struct {
	name   string
	re     *regexp.Regexp
	weight float64
}

// injectionSignals are the prompt-injection and prompt-leak heuristics. Each
// is a case-insensitive pattern with a weight; the Detector sums the weights of
// the patterns that fire (capped at 1.0). Weights are tuned so that a single
// unambiguous jailbreak phrase ("ignore previous instructions") already clears
// the default threshold on its own, while weak signals (a lone base64 blob)
// need corroboration.
var injectionSignals = []signal{
	{
		name:   "ignore_previous_instructions",
		re:     regexp.MustCompile(`(?i)\b(?:ignore|forget|disregard|override)\b[^.\n]{0,40}\b(?:previous|prior|above|earlier|preceding|initial|all)\b[^.\n]{0,40}\b(?:instruction|instructions|prompt|prompts|command|commands|direction|directions|rule|rules|context)\b`),
		weight: 0.7,
	},
	{
		name:   "disregard_system_prompt",
		re:     regexp.MustCompile(`(?i)\b(?:ignore|disregard|forget|bypass|override)\b[^.\n]{0,30}\b(?:your|the)\b[^.\n]{0,20}\bsystem\s+prompt\b`),
		weight: 0.8,
	},
	{
		name:   "reveal_system_prompt",
		re:     regexp.MustCompile(`(?i)\b(?:reveal|repeat|print|show|expose|display|output|tell\s+me|give\s+me|what\s+(?:are|is|were))\b[^.\n]{0,40}\b(?:your|the)\b[^.\n]{0,20}\b(?:system\s+prompt|instructions|initial\s+prompt|original\s+prompt|prompt|rules|guidelines|directives)\b`),
		weight: 0.7,
	},
	{
		name:   "leak_text_above",
		re:     regexp.MustCompile(`(?i)\b(?:repeat|print|show|output|copy)\b[^.\n]{0,30}\b(?:everything|all|the\s+text|words)\b[^.\n]{0,20}\babove\b`),
		weight: 0.6,
	},
	{
		name:   "role_override",
		re:     regexp.MustCompile(`(?i)\byou\s+are\s+now\b|\bfrom\s+now\s+on\b[^.\n]{0,20}\byou\b|\b(?:act|behave|respond)\s+as\s+(?:if\s+you\s+are\s+)?(?:a|an|the)\b|\bpretend\s+(?:to\s+be|you\s+are)\b`),
		weight: 0.5,
	},
	{
		name:   "jailbreak_persona",
		re:     regexp.MustCompile(`(?i)\b(?:DAN|do\s+anything\s+now|developer\s+mode|jailbreak|unfiltered\s+mode|no\s+restrictions?\s+mode)\b`),
		weight: 0.7,
	},
	{
		name:   "bypass_safety",
		re:     regexp.MustCompile(`(?i)\b(?:bypass|ignore|disable|turn\s+off|without|override)\b[^.\n]{0,30}\b(?:safety|guardrail|guardrails|restriction|restrictions|filter|filters|policy|policies|content\s+policy)\b`),
		weight: 0.6,
	},
	{
		name:   "delimiter_break",
		re:     regexp.MustCompile(`(?i)</?(?:system|instruction|instructions|prompt|user|assistant)\s*>|<\|[^|]{1,40}\|>|\[/?INST\]|\[/?SYS\]|#{2,}\s*(?:system|instruction)`),
		weight: 0.5,
	},
	{
		name:   "base64_payload",
		re:     regexp.MustCompile(`\b[A-Za-z0-9+/]{40,}={0,2}\b`),
		weight: 0.25,
	},
}

// InputScanner is the extension seam for input-side detection. The built-in
// *Detector implements it, and a future language-aware backend (an opt-in
// multilingual injection classifier — the research names the MIT-licensed
// mDeBERTa family; NOT Llama Prompt Guard 2, whose Llama Community License is a
// constitutional license BLOCKER) can plug in behind the SAME interface via
// Detector.WithScanner. That backend is deliberately NOT built here: regex plus
// Unicode normalization hardens the existing (mostly English) patterns against
// obfuscation, but genuine arbitrary-language semantic injection detection is a
// model problem and is the documented follow-on. Implementations MUST be safe
// for concurrent use and MUST NOT panic.
type InputScanner interface {
	// Inspect scores text and returns an advisory Verdict.
	Inspect(text string) Verdict
}

// Detector flags prompt-injection and prompt-leak attempts in user input using
// pattern + weight heuristics — no model call, fully deterministic. Before
// matching, each input is canonicalized (NFKC + case-fold + homoglyph/leet fold,
// see normalize.go) so homoglyph/full-width/zero-width/leetspeak/spacing evasions
// of the built-in patterns collapse toward the detectable form. Construct one
// with NewDetector. It is safe for concurrent use.
type Detector struct {
	signals   []signal
	threshold float64
	backends  []InputScanner
}

// NewDetector returns a Detector with the built-in injection heuristics and the
// default suspicion threshold (0.5). A single strong phrase clears it.
func NewDetector() *Detector {
	return &Detector{
		signals:   injectionSignals,
		threshold: 0.5,
	}
}

// WithScanner returns a copy of the Detector with additional InputScanner
// backends registered (e.g. an opt-in multilingual model). Each backend's
// Verdict is merged into the built-in result: Suspicious if either flags, Score
// is the max, and Signals are the union. Do NOT register a Detector as its own
// backend — that would recurse.
func (d *Detector) WithScanner(scanners ...InputScanner) *Detector {
	cp := *d
	cp.backends = append(append([]InputScanner(nil), d.backends...), scanners...)
	return &cp
}

// WithThreshold returns a copy of the Detector whose Suspicious cutoff is t
// (clamped to 0..1). Lower is stricter.
func (d *Detector) WithThreshold(t float64) *Detector {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	cp := *d
	cp.threshold = t
	return &cp
}

// Threshold reports the current Suspicious cutoff.
func (d *Detector) Threshold() float64 { return d.threshold }

// Inspect scores text against the injection heuristics and returns a Verdict.
// The patterns run against the original text AND its normalized variants (folded
// + space-collapsed, see matchVariants), so an obfuscated attack that only the
// canonical form reveals is still caught; each signal's weight counts once
// regardless of how many variants it fired on. Any registered InputScanner
// backend is then merged in. Signals are returned in a stable (alphabetical)
// order so callers and tests see deterministic output. Score is capped at 1.0.
func (d *Detector) Inspect(text string) Verdict {
	if text == "" {
		return Verdict{}
	}
	v := d.inspectHeuristics(text)
	for _, b := range d.backends {
		if b == nil {
			continue
		}
		v = mergeVerdict(v, b.Inspect(text), d.threshold)
	}
	return v
}

// inspectHeuristics runs the built-in pattern signals over every match variant
// of text (original + folded + space-collapsed) and unions the fired signals so
// evasion of the raw text is caught while each weight is still counted once.
func (d *Detector) inspectHeuristics(text string) Verdict {
	variants := matchVariants(text)
	matched := make(map[string]bool, len(d.signals))
	for _, v := range variants {
		for _, s := range d.signals {
			if matched[s.name] {
				continue
			}
			if s.re.MatchString(v) {
				matched[s.name] = true
			}
		}
	}
	var score float64
	var signals []string
	for _, s := range d.signals { // defined order, weight counted once per signal
		if matched[s.name] {
			signals = append(signals, s.name)
			score += s.weight
		}
	}
	if score > 1.0 {
		score = 1.0
	}
	sort.Strings(signals)
	return Verdict{
		Suspicious: score >= d.threshold,
		Signals:    signals,
		Score:      score,
	}
}

// mergeVerdict combines a base heuristic verdict with a backend scanner's verdict:
// Suspicious if either flags (or the merged score clears the threshold), Score is
// the max of the two, and Signals are the deduplicated union in stable order.
func mergeVerdict(base, extra Verdict, threshold float64) Verdict {
	score := base.Score
	if extra.Score > score {
		score = extra.Score
	}
	seen := make(map[string]bool, len(base.Signals)+len(extra.Signals))
	var signals []string
	for _, s := range base.Signals {
		if !seen[s] {
			seen[s] = true
			signals = append(signals, s)
		}
	}
	for _, s := range extra.Signals {
		if !seen[s] {
			seen[s] = true
			signals = append(signals, s)
		}
	}
	sort.Strings(signals)
	return Verdict{
		Suspicious: base.Suspicious || extra.Suspicious || score >= threshold,
		Signals:    signals,
		Score:      score,
	}
}
