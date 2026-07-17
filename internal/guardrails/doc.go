// Package guardrails is Aisotropy's dedicated in/out safety layer — the
// "stage 2" guardrail tier described in the book's LLM-application blueprint,
// where a request is filtered on the way *in* to the model and a response is
// checked on the way *out* to the user, independently of the model itself.
//
// # What "stage 2" means here
//
// The book frames a production LLM app as a pipeline of hardening stages.
// Stage 1 is the raw model call. Stage 2 wraps that call with guardrails so
// that neither hostile input nor unsafe output can pass through unexamined:
//
//   - INPUT guardrails (redact.go): scrub sensitive data (PII, secrets,
//     credentials) from user text *before* it reaches the model, and detect
//     prompt-injection / prompt-leak attempts (injection.go) — the class of
//     attack the book illustrates with the "McDonald's chatbot" case, where a
//     user coaxes an assistant into ignoring its instructions.
//   - OUTPUT guardrails (output.go): inspect the model's response *before* it
//     is returned — a heuristic toxicity/safety check, plus a leak check that
//     asks whether the response echoed a redacted secret or the system prompt
//     itself back to the user.
//
// # Design constraints
//
// This package is deliberately self-contained: it depends only on the Go
// standard library plus golang.org/x/text/unicode/norm (the official Unicode
// normalization package, used by the R4 evasion-resistance layer in
// normalize.go — no network, no model), performs no network or LLM calls, and is
// fully deterministic so it can be exercised by table-driven tests. That keeps
// the guardrail layer cheap enough to run on every request and impossible to
// turn into a data-exfiltration or latency hazard of its own.
//
// # Evasion resistance (R4)
//
// The built-in injection and toxicity heuristics match English literals. Before
// matching, every input is canonicalized to a COPY (NFKC + case-fold + a curated
// Cyrillic/Greek homoglyph fold + leetspeak fold + zero-width/format stripping,
// see normalize.go) so obfuscated variants — homoglyph, full-width, zero-width,
// leetspeak, character-spacing — collapse toward the detectable form in ANY
// script. The stored/forwarded text is never mutated. This hardens the existing
// patterns; it does NOT add semantic coverage of genuinely non-English attacks —
// that is the opt-in model backend behind the InputScanner / OutputCheck seams
// (see docs/security/guardrails-i18n-normalization.md).
//
// Two policies are baked in:
//
//   - Input redaction is FAIL-OPEN on the text: a message is never dropped
//     because redaction failed — the worst case is that the (already
//     redacted) text still flows. But redaction always emits a report of what
//     was scrubbed (types and counts, never the raw values), so a caller can
//     log/audit without ever handling the secret itself.
//   - Detection is ADVISORY: every detector returns a structured Verdict or
//     report and the *caller* decides whether to block, degrade, or allow.
//     The guardrail layer classifies; it does not unilaterally reject, which
//     keeps policy in one place (the request handler) rather than scattered
//     across detectors.
//
// # Extensibility
//
// The heavy, model-backed judgment (an "LLM-as-judge" toxicity/faithfulness
// grader) is intentionally *not* implemented here — this package must stay
// dependency-free. Instead output.go defines the OutputCheck interface so an
// integrator can attach an LLM-backed judge later without this package ever
// importing the evaluation tier. Custom PII/secret patterns are likewise
// pluggable on the Redactor, and custom toxicity keywords on the output
// inspector.
//
// The Guardrails aggregator (guardrails.go) ties the three detectors together
// behind CheckInput and CheckOutput, which is the surface an integrator wires
// into the request lifecycle (pre-LLM for input, post-LLM/pre-response for
// output). This package does not wire itself into any handler.
package guardrails
