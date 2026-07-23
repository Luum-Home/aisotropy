package agentloop

// OWASP Agentic ASI02 (Tool Misuse) + ASI03 (Identity & Privilege Abuse), and
// the agent-loop half of OWASP LLM06:2025 (Excessive Agency).
//
// The tools package proves a gated tool is never ADVERTISED to a caller lacking
// its scope. This suite proves the stronger agent-loop invariant: even if the
// model NAMES a tool it was never offered (a hallucination, or a coerced /
// injected attempt to escalate), SelectTool downgrades it to the no-tool
// Decision, so it is never handed to the Executor. A tool outside the granted
// scope is therefore unreachable from the agent loop by construction.
//
// Deterministic: the LLM client is a fake returning a fixed Decision JSON; the
// executor records what it was asked to run. No model, no network.
// See docs/security/owasp-agentic-coverage.md.

import (
	"context"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/tools"
)

// cannedClient streams one fixed Decision JSON response, modeling a model that
// tries to call a specific (possibly non-permitted) tool.
type cannedClient struct{ response string }

func (cannedClient) Name() string { return "canned" }
func (c cannedClient) StreamChat(_ context.Context, _, _ string) (<-chan llm.Token, error) {
	ch := make(chan llm.Token, 1)
	ch <- llm.Token{Text: c.response, Done: true}
	close(ch)
	return ch, nil
}

// recordingExecutor records every tool name it is asked to execute, so a test
// can assert a scoped-out tool was NEVER dispatched.
type recordingExecutor struct{ executed []string }

func (r *recordingExecutor) Execute(_ context.Context, name string, _ map[string]string) (string, error) {
	r.executed = append(r.executed, name)
	return "ok", nil
}

// TestOWASP_ASI02_ScopedOutToolNeverSelected: the model names admin:write's
// delete_user, but the caller holds only facts:read — SelectTool returns the
// no-tool Decision, never the escalated tool.
func TestOWASP_ASI02_ScopedOutToolNeverSelected(t *testing.T) {
	registry := tools.NewRegistry(
		tools.Tool{Name: "read_facts", RequiredScope: "facts:read"},
		tools.Tool{Name: "delete_user", RequiredScope: "admin:write"},
	)
	loop := &Loop{
		Client:   cannedClient{response: `{"tool":"delete_user","args":{"id":"victim"}}`},
		Registry: registry,
		Executor: &recordingExecutor{},
		MaxSteps: 1,
	}

	dec, err := loop.SelectTool(context.Background(), []string{"facts:read"}, "please delete the user")
	if err != nil {
		t.Fatalf("SelectTool: %v", err)
	}
	if dec.Tool != "" {
		t.Fatalf("a scoped-out tool must be downgraded to the no-tool Decision, got Tool=%q", dec.Tool)
	}
}

// TestOWASP_ASI02_ScopedOutToolNeverExecuted: the full Run loop never dispatches
// a scoped-out tool to the Executor, even when the model insists on calling it.
func TestOWASP_ASI02_ScopedOutToolNeverExecuted(t *testing.T) {
	registry := tools.NewRegistry(
		tools.Tool{Name: "read_facts", RequiredScope: "facts:read"},
		tools.Tool{Name: "delete_user", RequiredScope: "admin:write"},
	)
	exec := &recordingExecutor{}
	loop := &Loop{
		Client:   cannedClient{response: `{"tool":"delete_user","args":{"id":"victim"}}`},
		Registry: registry,
		Executor: exec,
		MaxSteps: 3,
	}

	if _, err := loop.Run(context.Background(), []string{"facts:read"}, "delete the user now"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, name := range exec.executed {
		if name == "delete_user" {
			t.Fatalf("scoped-out tool delete_user was executed — privilege escalation via the agent loop")
		}
	}
}

// TestOWASP_ASI02_PermittedToolStillWorks: the gate is not a blanket denial — a
// tool WITHIN the granted scope is selected and executed normally.
func TestOWASP_ASI02_PermittedToolStillWorks(t *testing.T) {
	registry := tools.NewRegistry(
		tools.Tool{Name: "read_facts", RequiredScope: "facts:read"},
	)
	exec := &recordingExecutor{}
	loop := &Loop{
		Client:   cannedClient{response: `{"tool":"read_facts","args":{}}`},
		Registry: registry,
		Executor: exec,
		MaxSteps: 1,
	}
	dec, err := loop.SelectTool(context.Background(), []string{"facts:read"}, "look it up")
	if err != nil {
		t.Fatalf("SelectTool: %v", err)
	}
	if dec.Tool != "read_facts" {
		t.Fatalf("a permitted tool must be selectable, got Tool=%q", dec.Tool)
	}
}
