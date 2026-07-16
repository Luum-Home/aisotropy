// Command guardrail-policy is a runnable demonstration of the RIGHT place to
// plug custom answer-time policy into an Aisotropy runtime: the WithGuardrailCheck
// output-guardrail seam — NOT a hand-rolled wrapper around the LLM client.
//
// # The problem this example answers
//
// A common first instinct, when you want "run a policy check before the answer
// reaches the user", is to wrap the LLM client via WithLLM: your wrapper calls
// the real model, then inspects/rewrites the reply. That works, but it is the
// wrong layer — you end up re-implementing token streaming, usage accounting and
// context cancellation just to get an inspection hook, and your check sits
// OUTSIDE the runtime's own guardrail aggregation (the built-in toxicity and
// secret-leak heuristics, plus block-output substitution). It is easily ~100+
// lines of plumbing for what is really a policy decision.
//
// WithGuardrailCheck (ADR-0033) is that decision expressed at the seam. You
// implement one small interface — Name() + Check(ctx, in) — and the runtime runs
// your check on every model response ALONGSIDE its built-ins, aggregates the
// verdicts, and (when AISOTROPY_GUARDRAILS_BLOCK_OUTPUT is enabled) substitutes a
// safe refusal on the non-streaming path when any check flags. No client
// wrapping, no streaming plumbing.
//
// # What this example registers
//
// Two output checks, both via WithGuardrailCheck. Each OutputCheck receives the
// original user Input, the model's Output, and the SystemPrompt in force, so a
// single seam covers both "input-facing" and "output-facing" policy:
//
//   - intentClassifier   — reads in.Input: classifies the request (question vs
//     statement) and FLAGS an input that carries a secret-looking token. This is
//     the input-facing use of the seam: advisory annotation/refusal keyed off
//     what the user sent.
//
//   - groundedValidator  — reads in.Output: the "grounded-output" pattern. It
//     holds a tiny in-memory corpus of supported claims (stand-in for a tenant's
//     ingested knowledge) and FLAGS any claim the model asserts that the corpus
//     does not support. This is the generic shape of "approved-claims
//     validation": refuse response content not grounded in what the tenant
//     actually ingested.
//
// # Honesty gate (read this)
//
//   - This is a DEV/DEMO program, not production. It runs keyless: with no
//     ANTHROPIC_API_KEY set, the App falls back to the built-in stub LLM, whose
//     structured reply deterministically embeds your prompt text — which is what
//     lets the grounded validator see controllable output without a real model.
//     A real provider changes nothing about where or how the seam fires.
//
//   - The two checks are intentionally SIMPLE heuristics (a regex intent guess, a
//     map-backed claim lookup). Real policy uses an LLM judge / retrieval / NLI.
//     The point of the example is the SEAM and its wiring, not the classifier.
//
//   - Input REDACTION and prompt-injection detection are BUILT IN (the runtime's
//     Redactor + injection Detector run before the model). They are NOT this seam
//     and you do not wrap the client for them either. WithGuardrailCheck is the
//     OUTPUT-side, advisory (flag/refuse) stage; it does not mutate the model's
//     input.
//
//     the OutputCheckInput / Verdict payload types the Check signature references.
//     That import resolves ONLY because this example lives inside the aisotropy
//     module tree (the same way the repo's own seam tests implement the check). A
//     fully EXTERNAL module cannot import internal/... yet; re-exporting those
//     payload types as public aliases is a pending follow-up (see ADR-0033 and
//     aisotropy/extend.go). Until it lands, this in-tree example is the concrete,
//     reproducible shape of the seam.
//
// Run it (no key, no Docker):
//
//	cd examples/guardrail-policy && go run .
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"

	"github.com/Luum-Home/aisotropy/aisotropy"
)

// --- Check 1: input-facing intent classifier -------------------------------

// secretPattern matches obviously secret-looking tokens a user might paste into
// a prompt (an API-key-shaped token or an explicit "password:"/"api key:"
// preamble). It is deliberately crude — a real classifier is far richer — but it
// makes the input-facing branch of the seam concrete and deterministic.
var secretPattern = regexp.MustCompile(`(?i)\b(sk-[a-z0-9]{4,}|api[ _-]?key\s*[:=]|password\s*[:=])`)

// intentClassifier is an OutputCheck that keys off the ORIGINAL user input
// (in.Input). It shows that the output seam can carry input-facing policy: the
// check sees exactly what the user sent, annotates a coarse intent, and flags
// the turn Suspicious when the input carries a secret-looking token (so, with
// block-output on, the runtime substitutes a refusal instead of answering a
// request that leaks a credential into the transcript).
//
// Note it is ADVISORY and runs at answer time — it does NOT redact or rewrite
// the input before the model. True input redaction is the runtime's built-in
// Redactor, a separate, mutating pre-model stage that needs no seam at all.
type intentClassifier struct{}

func (intentClassifier) Name() string { return "intent-classifier" }

func (intentClassifier) Check(_ context.Context, in aisotropy.GuardrailCheckInput) aisotropy.GuardrailVerdict {
	intent := "statement"
	if strings.Contains(in.Input, "?") {
		intent = "question"
	}

	if secretPattern.MatchString(in.Input) {
		log.Printf("[intent-classifier] intent=%s secret_in_input=true -> FLAG", intent)
		return aisotropy.GuardrailVerdict{
			Suspicious: true,
			Signals:    []string{"intent:" + intent, "input-secret"},
			Score:      1.0,
		}
	}

	log.Printf("[intent-classifier] intent=%s secret_in_input=false -> pass (annotate only)", intent)
	// A non-Suspicious verdict may still carry Signals: annotation without a block.
	return aisotropy.GuardrailVerdict{Signals: []string{"intent:" + intent}}
}

// --- Check 2: output-facing grounded validator -----------------------------

// claimPattern extracts naive "<subject> is in <place>" assertions from the
// model's output so the validator has a concrete claim to check against the
// corpus. Real grounding uses retrieval + entailment; this regex is the demo's
// stand-in.
var claimPattern = regexp.MustCompile(`([a-z][a-z ]+?) is in ([a-z]+)`)

// groundedValidator is an OutputCheck implementing the generic "grounded-output"
// pattern: it flags response claims the tenant's ingested corpus does not
// support. corpus maps a supported subject to its single supported place; any
// asserted (subject, place) pair absent from the corpus is treated as an
// ungrounded claim and flagged. This is the framework-generic core of
// "approved-claims validation" — no domain policy baked in, just "is this
// claim backed by what we actually ingested?".
type groundedValidator struct {
	corpus map[string]string
}

func newGroundedValidator() groundedValidator {
	return groundedValidator{corpus: map[string]string{
		"eiffel tower": "paris",
		"colosseum":    "rome",
	}}
}

func (g groundedValidator) Name() string { return "grounded-output" }

func (g groundedValidator) Check(_ context.Context, in aisotropy.GuardrailCheckInput) aisotropy.GuardrailVerdict {
	lower := strings.ToLower(in.Output)
	var ungrounded []string
	for _, m := range claimPattern.FindAllStringSubmatch(lower, -1) {
		subject := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(m[1]), "the "))
		place := strings.TrimSpace(m[2])
		supported, known := g.corpus[subject]
		if !known {
			continue // not a claim we have any corpus opinion on; abstain.
		}
		if supported != place {
			ungrounded = append(ungrounded, fmt.Sprintf("ungrounded:%q is in %q", subject, place))
		}
	}

	if len(ungrounded) > 0 {
		log.Printf("[grounded-output] ungrounded claims=%v -> FLAG", ungrounded)
		return aisotropy.GuardrailVerdict{Suspicious: true, Signals: ungrounded, Score: 1.0}
	}
	log.Printf("[grounded-output] all inspected claims grounded -> pass")
	return aisotropy.GuardrailVerdict{}
}

// --- driver ----------------------------------------------------------------

func main() {
	// Enable block-output so a flagged turn VISIBLY becomes a safe refusal on the
	// non-streaming App.Turn path — that is the observable effect of a check
	// firing. Set before New(): the runtime reads this env at boot.
	if err := os.Setenv("AISOTROPY_GUARDRAILS_BLOCK_OUTPUT", "true"); err != nil {
		log.Fatalf("setenv: %v", err)
	}

	// Keyless: no WithLLM, so with no ANTHROPIC_API_KEY the App uses the built-in
	// stub. A generous rate limit keeps the demo turns from being throttled. The
	// ONLY policy wiring is the two guardrail checks — no client wrapping.
	app := aisotropy.New(
		aisotropy.WithRateLimit(1e9, 1e9),
		aisotropy.WithGuardrailCheck(intentClassifier{}, newGroundedValidator()),
	)
	defer func() {
		if err := app.Close(); err != nil {
			log.Printf("close: %v", err)
		}
	}()

	turns := []struct {
		title  string
		prompt string
	}{
		{"grounded, benign claim", "The Eiffel Tower is in Paris."},
		{"ungrounded claim (output check flags)", "The Eiffel Tower is in Berlin."},
		{"secret in the input (input check flags)", "Please store my api key: sk-demo-abc123"},
	}

	for i, tc := range turns {
		fmt.Printf("\n=== turn %d: %s ===\n", i+1, tc.title)
		fmt.Printf("prompt: %s\n", tc.prompt)
		out, err := app.Turn(context.Background(), aisotropy.ChatInput{
			TenantID:  "demo-tenant",
			UserID:    "demo-user",
			SessionID: "guardrail-demo",
			Prompt:    tc.prompt,
		})
		if err != nil {
			log.Fatalf("turn %d: %v", i+1, err)
		}
		fmt.Printf("reply : %s\n", out.Reply)
		if strings.Contains(out.Reply, "can't help") {
			fmt.Println("effect: a guardrail check flagged this turn -> runtime substituted a safe refusal")
		} else {
			fmt.Println("effect: all checks passed -> model reply returned unchanged")
		}
	}

	fmt.Println("\nDone. The per-check log lines above are the checks firing inside the")
	fmt.Println("runtime's output inspector — reached via WithGuardrailCheck, not a WithLLM wrapper.")
}
