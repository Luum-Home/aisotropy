package tools

// OWASP LLM06:2025 — Excessive Agency (and Agentic ASI02 Tool Misuse / ASI03
// Identity & Privilege Abuse at the tool-registry seam).
//
// aisotropy gates every agent capability by the request's JWT scopes:
// Registry.Permitted returns only the tools the granted scopes allow, and
// PromptFragment renders only those into the model's context — a tool the caller
// lacks the scope for is not shown disabled, it simply does not exist for the
// model ("governance by absence of information"). This suite proves the gate is
// fail-closed: missing scope, unknown scope, and empty scope never expose a
// gated tool, while an always-available (empty RequiredScope) tool is always
// present. Deterministic: pure function calls, no model, no network.
//
// The agent-loop half (a tool the model NAMES but lacks scope for is never
// executed) is locked in internal/agentloop/owasp_asi02_tool_misuse_test.go.
// See docs/security/owasp-llm-coverage.md and docs/security/owasp-agentic-coverage.md.

import (
	"sort"
	"strings"
	"testing"
)

func owaspTestRegistry() *Registry {
	return NewRegistry(
		Tool{Name: "echo", Description: "echo text", RequiredScope: ""}, // always available
		Tool{Name: "read_facts", Description: "read facts", RequiredScope: "facts:read"},
		Tool{Name: "delete_user", Description: "delete a user", RequiredScope: "admin:write"},
	)
}

func owaspToolNames(ts []Tool) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	sort.Strings(out)
	return out
}

func owaspEqualStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestOWASP_LLM06_ScopeGatesToolExposure: each scope set exposes exactly the
// permitted tools — a gated tool never leaks to an identity lacking its scope.
func TestOWASP_LLM06_ScopeGatesToolExposure(t *testing.T) {
	r := owaspTestRegistry()
	cases := []struct {
		name   string
		scopes []string
		want   []string
	}{
		{"no_scopes_only_open_tool", nil, []string{"echo"}},
		{"empty_scope_slice", []string{}, []string{"echo"}},
		{"facts_scope", []string{"facts:read"}, []string{"echo", "read_facts"}},
		{"admin_scope", []string{"admin:write"}, []string{"delete_user", "echo"}},
		{"both_scopes", []string{"facts:read", "admin:write"}, []string{"delete_user", "echo", "read_facts"}},
		{"unknown_scope_fails_closed", []string{"facts:write", "superuser", "*"}, []string{"echo"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := owaspToolNames(r.Permitted(c.scopes))
			if !owaspEqualStrs(got, c.want) {
				t.Errorf("Permitted(%v) = %v, want %v", c.scopes, got, c.want)
			}
		})
	}
}

// TestOWASP_LLM06_PromptFragmentNeverAdvertisesGatedTool: the system-prompt
// fragment (the actual injection point) never mentions a tool the caller lacks
// the scope for — the model is never even told the capability exists.
func TestOWASP_LLM06_PromptFragmentNeverAdvertisesGatedTool(t *testing.T) {
	r := owaspTestRegistry()

	frag := r.PromptFragment([]string{"facts:read"})
	if !strings.Contains(frag, "read_facts") {
		t.Errorf("permitted tool read_facts missing from fragment: %q", frag)
	}
	if strings.Contains(frag, "delete_user") {
		t.Errorf("gated tool delete_user leaked into the prompt fragment for a non-admin: %q", frag)
	}

	openFrag := r.PromptFragment(nil)
	if strings.Contains(openFrag, "delete_user") || strings.Contains(openFrag, "read_facts") {
		t.Errorf("gated tools leaked into the no-scope fragment: %q", openFrag)
	}
}

// TestOWASP_LLM06_UnknownScopeNeverEscalates: a fabricated / wildcard-looking
// scope grants nothing beyond the always-available tool — the gate is a
// whitelist (exact match), never a pattern that "*" could satisfy.
func TestOWASP_LLM06_UnknownScopeNeverEscalates(t *testing.T) {
	r := owaspTestRegistry()
	for _, s := range [][]string{{"*"}, {"admin:*"}, {"ADMIN:WRITE"}, {"admin:write "}, {"root"}} {
		got := owaspToolNames(r.Permitted(s))
		if !owaspEqualStrs(got, []string{"echo"}) {
			t.Errorf("scope %v must not escalate; Permitted = %v, want [echo]", s, got)
		}
	}
}
