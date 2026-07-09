// Package tools implements the behavior axis of granular control
// (docs/architecture/granular-control.md): the tools/primitives an agent may
// use are gated by the request's JWT scopes. A tool the caller lacks the scope
// for is not injected into the LLM payload at all — it is not sent disabled or
// with a "do not use" instruction, it simply does not exist for the model.
// Governance by absence of information: there is no surface for the model to
// hallucinate a bypass of a permission it was never told about.
package tools

import (
	"fmt"
	"strings"
)

// Tool is an agent capability. RequiredScope gates it: the tool is injected only
// when the granted scopes include RequiredScope. An empty RequiredScope means
// the tool is always available (no special permission needed).
type Tool struct {
	Name          string
	Description   string
	RequiredScope string
}

// Registry is an immutable, ordered set of tools.
type Registry struct {
	tools []Tool
}

// NewRegistry builds a registry from the given tools, preserving their order.
func NewRegistry(tools ...Tool) *Registry {
	return &Registry{tools: append([]Tool(nil), tools...)}
}

// Permitted returns the tools the granted scopes allow, in registry order. A
// tool whose RequiredScope is not granted is omitted entirely.
func (r *Registry) Permitted(scopes []string) []Tool {
	granted := make(map[string]struct{}, len(scopes))
	for _, s := range scopes {
		granted[s] = struct{}{}
	}
	var out []Tool
	for _, t := range r.tools {
		if t.RequiredScope == "" {
			out = append(out, t)
			continue
		}
		if _, ok := granted[t.RequiredScope]; ok {
			out = append(out, t)
		}
	}
	return out
}

// PromptFragment renders the scope-permitted tools as a system-prompt fragment
// (one scope, one subset of tools), or "" when none are permitted. This is the
// injection point: only what survives the scope gate reaches the model.
func (r *Registry) PromptFragment(scopes []string) string {
	permitted := r.Permitted(scopes)
	if len(permitted) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Tools you may use (only these exist for you):")
	for _, t := range permitted {
		fmt.Fprintf(&sb, "\n- %s: %s", t.Name, t.Description)
	}
	return sb.String()
}
