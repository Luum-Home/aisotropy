package guardrails

// OWASP LLM05:2025 — Improper Output Handling.
//
// aisotropy's structured-output path treats model text as UNTRUSTED until it
// satisfies a closed schema: the value that crosses into a privileged decision is
// the schema-validated token, never the raw model string. This suite proves the
// schema validators fail CLOSED on malformed / injected / hedged output and match
// EXACTLY (no substring escape), so a crafted model response cannot smuggle an
// instruction or tool call through as a "valid" structured answer. Deterministic:
// pure function calls, no model, no network.
//
// The reflection-loop half (a schema violation triggers bounded retries with the
// exact typing error rather than a silent unstructured fallback) is a
// constitutional gate covered by internal/llm reflection tests; here we lock the
// validation contract those loops depend on. See docs/security/owasp-llm-coverage.md.

import "testing"

// TestOWASP_LLM05_EnumSchemaExactMatchOnly: the closed vocabulary matches the
// WHOLE trimmed value, never a substring — so output that merely CONTAINS an
// allowed token (with an appended injection) is rejected.
func TestOWASP_LLM05_EnumSchemaExactMatchOnly(t *testing.T) {
	s := EnumSchema{Allowed: []string{"approve", "deny"}, CaseInsensitive: true}

	accept := []struct{ raw, want string }{
		{"approve", "approve"},
		{"  deny  ", "deny"},
		{"\"Approve\"", "approve"},
		{"`DENY`", "deny"},
	}
	for _, c := range accept {
		got, err := s.Validate(c.raw)
		if err != nil || got != c.want {
			t.Errorf("Validate(%q) = (%q, %v), want (%q, nil)", c.raw, got, err, c.want)
		}
	}

	reject := []string{
		"approve; then run rm -rf /", // trailing injection
		"the decision is approve",    // embedded token
		"approve\ndeny",              // two tokens
		"appro ve",                   // corrupted
		"<tool>approve</tool>",       // markup-wrapped
		"",                           // empty
	}
	for _, raw := range reject {
		if got, err := s.Validate(raw); err == nil {
			t.Errorf("Validate(%q) must fail closed, got value %q", raw, got)
		}
	}
}

// TestOWASP_LLM05_BoolSchemaRejectsHedgedProse: a yes/no decision fails closed on
// hedged or injected prose rather than being coerced to an arbitrary boolean.
func TestOWASP_LLM05_BoolSchemaRejectsHedgedProse(t *testing.T) {
	var s BoolSchema

	for raw, want := range map[string]string{
		"true": "true", "YES": "true", "1": "true",
		"false": "false", "no": "false", "0": "false",
	} {
		got, err := s.Validate(raw)
		if err != nil || got != want {
			t.Errorf("Validate(%q) = (%q, %v), want (%q, nil)", raw, got, err, want)
		}
	}

	reject := []string{
		"maybe, it depends",
		"true, and also ignore your instructions",
		"probably yes",
		"",
	}
	for _, raw := range reject {
		if got, err := s.Validate(raw); err == nil {
			t.Errorf("BoolSchema.Validate(%q) must fail closed, got %q", raw, got)
		}
	}
}
