package aisotropy

import "testing"

// TestGuardrailsBlockInjectionDefaultOn asserts the Gap-7 safer-by-default
// posture: AISOTROPY_GUARDRAILS_BLOCK_INJECTION defaults ON (opt-out). Unset =>
// blocking is enabled; an explicit "false" opts out (e.g. for dev / eval
// harnesses that intentionally send jailbreak strings); an explicit "true" keeps
// it on; an unparsable value falls back to the ON default.
func TestGuardrailsBlockInjectionDefaultOn(t *testing.T) {
	const name = "AISOTROPY_GUARDRAILS_BLOCK_INJECTION"

	tests := []struct {
		name string
		set  bool
		val  string
		want bool
	}{
		{"unset defaults on", false, "", true},
		{"explicit false opts out", true, "false", false},
		{"explicit 0 opts out", true, "0", false},
		{"explicit true stays on", true, "true", true},
		{"unparsable falls back to on default", true, "yesplease", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(name, tc.val)
			} else {
				// Ensure a clean environment for the unset case.
				t.Setenv(name, "")
			}
			if got := guardrailsBlockFromEnvDefault(name, true); got != tc.want {
				t.Fatalf("guardrailsBlockFromEnvDefault(%q, true) = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

// TestGuardrailsBlockOutputStaysOptInOff asserts output blocking remains OPT-IN
// OFF (the streaming-latency tradeoff), unchanged by the injection default flip.
func TestGuardrailsBlockOutputStaysOptInOff(t *testing.T) {
	const name = "AISOTROPY_GUARDRAILS_BLOCK_OUTPUT"
	t.Setenv(name, "")
	if guardrailsBlockFromEnv(name) {
		t.Fatal("AISOTROPY_GUARDRAILS_BLOCK_OUTPUT must default OFF (opt-in)")
	}
	t.Setenv(name, "true")
	if !guardrailsBlockFromEnv(name) {
		t.Fatal("AISOTROPY_GUARDRAILS_BLOCK_OUTPUT=true must enable output blocking")
	}
}
