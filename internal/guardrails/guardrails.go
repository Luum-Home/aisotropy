package guardrails

import "context"

// Config assembles the three guardrail detectors. Any nil field is filled with
// a default by New, so the zero Config yields a fully working Guardrails with
// built-in behavior.
type Config struct {
	Redactor *Redactor
	Detector *Detector
	Output   *OutputInspector
}

// Guardrails is the aggregator an integrator wires into the request lifecycle:
// CheckInput before the model call, CheckOutput before returning the response.
// It performs no I/O and is safe for concurrent use.
type Guardrails struct {
	redactor *Redactor
	detector *Detector
	output   *OutputInspector
}

// New builds a Guardrails from cfg, substituting defaults for any nil detector.
// The output inspector reuses the same Redactor so the leak check and the input
// redaction agree on what counts as a secret.
func New(cfg Config) *Guardrails {
	r := cfg.Redactor
	if r == nil {
		r = NewRedactor()
	}
	d := cfg.Detector
	if d == nil {
		d = NewDetector()
	}
	o := cfg.Output
	if o == nil {
		o = NewOutputInspector(r)
	}
	return &Guardrails{redactor: r, detector: d, output: o}
}

// Default returns a Guardrails with all built-in detectors and no custom
// patterns or extension checks.
func Default() *Guardrails { return New(Config{}) }

// InputResult is the outcome of CheckInput: the redacted text to forward to the
// model, a report of what was scrubbed, and the injection verdict. Blocked is a
// convenience flag (true when injection is Suspicious); the caller decides
// whether to actually reject. The redacted text is always safe to forward even
// when Blocked is true — redaction is fail-open.
type InputResult struct {
	Redacted  string
	Redaction RedactionReport
	Injection Verdict
	Blocked   bool
}

// CheckInput runs the input guardrails: it redacts sensitive data (fail-open on
// the text) and scores the ORIGINAL text for prompt-injection / prompt-leak.
// Injection scoring runs on the original text, not the redacted one, so a
// payload that hides a secret cannot also evade injection detection.
func (g *Guardrails) CheckInput(text string) InputResult {
	redacted, report := g.redactor.Redact(text)
	verdict := g.detector.Inspect(text)
	return InputResult{
		Redacted:  redacted,
		Redaction: report,
		Injection: verdict,
		Blocked:   verdict.Suspicious,
	}
}

// CheckOutput runs the output guardrails on a model response before it is
// returned to the user, given the original input and the system prompt in
// force. The result's Blocked flag is advisory; the caller owns the final
// decision.
func (g *Guardrails) CheckOutput(ctx context.Context, input, output, systemPrompt string) OutputResult {
	return g.output.Inspect(ctx, input, output, systemPrompt)
}

// Redactor exposes the underlying redactor for callers that need to scrub text
// outside the CheckInput flow (e.g. redacting logs).
func (g *Guardrails) Redactor() *Redactor { return g.redactor }

// Detector exposes the underlying injection detector.
func (g *Guardrails) Detector() *Detector { return g.detector }

// Output exposes the underlying output inspector.
func (g *Guardrails) Output() *OutputInspector { return g.output }
