package taskagent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// fakeToolCaller is a keyless host-side llm.ToolCaller: it drives RunTask's
// NATIVE tool-calling path (llm.RunToolAgent) rather than the structured-output
// agentloop. Its first CallTools turn asks for one run_shell call; the next
// turn returns no calls (terminal), so the loop finishes with one executed step.
type fakeToolCaller struct {
	mu       sync.Mutex
	turn     int
	gotTools []llm.ToolSpec
}

func (c *fakeToolCaller) Name() string { return "fake-toolcaller" }

// StreamChat is required by the embedded Client interface but is never used on
// the native tool-calling path.
func (c *fakeToolCaller) StreamChat(context.Context, string, string) (<-chan llm.Token, error) {
	ch := make(chan llm.Token)
	close(ch)
	return ch, nil
}

func (c *fakeToolCaller) CallTools(_ context.Context, _, _ string, tools []llm.ToolSpec) (llm.ToolCallResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gotTools = tools
	c.turn++
	if c.turn == 1 {
		return llm.ToolCallResult{
			Calls: []llm.ToolCall{{
				Name:    ToolRunShell,
				Args:    map[string]string{"cmd": "echo native"},
				RawArgs: json.RawMessage(`{"cmd":"echo native"}`),
			}},
		}, nil
	}
	// Terminal turn: no more tool calls.
	return llm.ToolCallResult{Message: "done"}, nil
}

// TestRunTaskNativeToolCallingPath drives RunTask with a ToolCaller client so
// runLoop takes the llm.RunToolAgent branch (the native tool-calling dispatch,
// previously uncovered). The scope gate must still filter the offered tools and
// the executed step must round-trip through the step trace.
func TestRunTaskNativeToolCallingPath(t *testing.T) {
	if !gitReady() {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	gitInit(t, repo)

	client := &fakeToolCaller{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := RunTask(ctx, client, nil, Request{
		RepoDir:  repo,
		Prompt:   "do a native tool call",
		MaxSteps: 3,
		Scopes:   HandScopes(),
		Provider: &fakeProvider{},
	})
	if err != nil {
		t.Fatalf("RunTask (native tool-calling): %v", err)
	}
	if len(res.Steps) != 1 {
		t.Fatalf("got %d steps, want 1 (one run_shell then terminal)", len(res.Steps))
	}
	if res.Steps[0].Tool != ToolRunShell {
		t.Fatalf("step tool = %q, want %q", res.Steps[0].Tool, ToolRunShell)
	}
	// The tools offered to the model were scope-filtered to exactly the hands.
	if len(client.gotTools) != len(HandScopes()) {
		t.Fatalf("offered %d tools, want %d (scope-gated hand set)", len(client.gotTools), len(HandScopes()))
	}
}

// TestRunTaskNativeToolCallingScopeGated confirms the offered tool set honors a
// restricted scope on the native path: a read-only scope offers exactly one
// tool.
func TestRunTaskNativeToolCallingScopeGated(t *testing.T) {
	if !gitReady() {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	gitInit(t, repo)

	client := &fakeToolCaller{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Only read_file permitted. The model asks for run_shell anyway on turn 1,
	// but that name is not in the offered set — it must never execute.
	if _, err := RunTask(ctx, client, nil, Request{
		RepoDir:  repo,
		Prompt:   "restricted",
		MaxSteps: 2,
		Scopes:   []string{ScopeReadFile},
		Provider: &fakeProvider{},
	}); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if len(client.gotTools) != 1 || client.gotTools[0].Name != ToolReadFile {
		t.Fatalf("offered tools = %v, want exactly [read_file]", client.gotTools)
	}
}
