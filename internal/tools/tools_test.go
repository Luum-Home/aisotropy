package tools

import (
	"strings"
	"testing"
)

func testRegistry() *Registry {
	return NewRegistry(
		Tool{Name: "search_docs", Description: "search the tenant knowledge base"}, // no scope: always available
		Tool{Name: "read_financials", Description: "read financial records", RequiredScope: "read:financial-data"},
		Tool{Name: "write_tasks", Description: "create and update tasks", RequiredScope: "write:tasks"},
	)
}

func TestPermittedGatesByScope(t *testing.T) {
	r := testRegistry()

	// No scopes: only the always-available tool.
	got := r.Permitted(nil)
	if len(got) != 1 || got[0].Name != "search_docs" {
		t.Fatalf("no-scope permitted = %+v, want only search_docs", got)
	}

	// One scope grants exactly one gated tool (plus the always-available one).
	got = r.Permitted([]string{"write:tasks"})
	names := toolNames(got)
	if !contains(names, "search_docs") || !contains(names, "write_tasks") {
		t.Fatalf("permitted = %v, want search_docs + write_tasks", names)
	}
	if contains(names, "read_financials") {
		t.Fatal("read_financials leaked without read:financial-data scope")
	}

	// All scopes: everything.
	got = r.Permitted([]string{"read:financial-data", "write:tasks"})
	if len(got) != 3 {
		t.Fatalf("all-scope permitted = %d tools, want 3", len(got))
	}
}

// TestPromptFragmentOmitsForbiddenTools is the governance-by-absence check: a
// tool the caller lacks the scope for never appears in the injected prompt, so
// the model can't even know it exists.
func TestPromptFragmentOmitsForbiddenTools(t *testing.T) {
	r := testRegistry()

	frag := r.PromptFragment([]string{"write:tasks"})
	if !strings.Contains(frag, "write_tasks") {
		t.Fatalf("fragment missing a permitted tool: %q", frag)
	}
	if strings.Contains(frag, "read_financials") {
		t.Fatalf("forbidden tool leaked into the prompt fragment: %q", frag)
	}

	// A caller with no scopes still gets the always-available tool but nothing
	// gated.
	frag = r.PromptFragment(nil)
	if !strings.Contains(frag, "search_docs") || strings.Contains(frag, "read_financials") {
		t.Fatalf("no-scope fragment wrong: %q", frag)
	}
}

func TestPromptFragmentEmptyWhenNothingPermitted(t *testing.T) {
	// A registry whose every tool needs a scope, queried with none.
	r := NewRegistry(Tool{Name: "admin_wipe", Description: "danger", RequiredScope: "admin"})
	if frag := r.PromptFragment(nil); frag != "" {
		t.Fatalf("expected empty fragment, got %q", frag)
	}
}

func toolNames(ts []Tool) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
