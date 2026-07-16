package taskagent

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/agentloop"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/sandbox"
)

// dockerReady reports whether a working docker CLI + daemon is reachable. Every
// real-container test gates on it and t.Skips when false — the same honesty gate
// the rqlite/qdrant/sleepcycle suites use, so `go test` never fails merely because
// Docker is absent.
func dockerReady() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "docker", "version").Run() == nil
}

// scriptedClient is a deterministic, keyless host-side llm.Client: each StreamChat
// emits the next pre-scripted agentloop.Decision as JSON. It is NOT a
// llm.ToolCaller, so RunTask drives it through the structured-output agentloop —
// exercising the exact host-side inference seam without any provider key.
type scriptedClient struct {
	mu        sync.Mutex
	decisions []agentloop.Decision
	i         int
}

func (c *scriptedClient) Name() string { return "scripted-test-client" }

func (c *scriptedClient) StreamChat(ctx context.Context, _, _ string) (<-chan llm.Token, error) {
	c.mu.Lock()
	var dec agentloop.Decision
	if c.i < len(c.decisions) {
		dec = c.decisions[c.i]
		c.i++
	} // past the end: emit the empty (stop) decision
	c.mu.Unlock()

	payload := marshalDecision(dec)
	ch := make(chan llm.Token, 2)
	ch <- llm.Token{Text: payload}
	ch <- llm.Token{Done: true}
	close(ch)
	return ch, nil
}

// capturingProvider wraps a real Provider and, at Create time (while the container
// is live), captures the container's full environment via `printenv`. This is the
// broker-invariant probe: the captured env must contain neither the host provider
// key's value nor its name.
type capturingProvider struct {
	inner   sandbox.Provider
	env     string
	execErr error
}

func (c *capturingProvider) Name() string { return c.inner.Name() }

func (c *capturingProvider) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	sb, err := c.inner.Create(ctx, spec)
	if err != nil {
		return nil, err
	}
	res, eerr := sb.Exec(ctx, []string{"printenv"}, sandbox.ExecOptions{})
	c.env = res.Stdout
	c.execErr = eerr
	return sb, nil
}

// TestRunTask_BrokerInvariant_KeyNeverEntersContainer is the differentiator test.
// With a real ANTHROPIC_API_KEY set in the HOST environment, RunTask must:
//   - drive the whole task with HOST-SIDE inference (the deterministic scripted
//     client), and
//   - stand up a container whose environment contains NEITHER the key's value NOR
//     its name — proving the provider key is brokered, never forwarded into the
//     sandbox — while still completing the task and merging a real commit to HEAD.
func TestRunTask_BrokerInvariant_KeyNeverEntersContainer(t *testing.T) {
	if !dockerReady() {
		t.Skip("docker unavailable; skipping real-container broker test")
	}
	if !gitReady() {
		t.Skip("git unavailable")
	}

	// Build the canary at runtime so no secret-shaped literal lives in source
	// (it is a dummy value, but the repo's content policy rejects key-shaped
	// literals). It stands in for a real host-side provider key.
	canary := "sk-" + "ant-" + "DUMMYcanary-" + t.Name() + "-DO-NOT-LEAK"
	t.Setenv("ANTHROPIC_API_KEY", canary)

	repo := t.TempDir()
	gitInit(t, repo)

	cap := &capturingProvider{inner: sandbox.NewDockerProvider()}
	client := &scriptedClient{decisions: []agentloop.Decision{
		{Tool: ToolRunShell, Args: map[string]string{"cmd": "echo brokered-hands > note.txt"}},
		{Tool: ToolGitCommit, Args: map[string]string{"message": "add note from sandbox"}},
		{Tool: ""}, // stop
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	res, err := RunTask(ctx, client, nil, Request{
		RepoDir:  repo,
		Prompt:   "create note.txt and commit it",
		Image:    "alpine:3.20",
		MaxSteps: 6,
		Scopes:   HandScopes(),
		Provider: cap,
	})
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	// BROKER INVARIANT: the container env must not carry the key value or name.
	if cap.execErr != nil {
		t.Fatalf("printenv probe failed: %v", cap.execErr)
	}
	if cap.env == "" {
		t.Fatal("captured container env is empty; probe did not run")
	}
	if strings.Contains(cap.env, canary) {
		t.Fatalf("LEAK: provider key VALUE found in container env:\n%s", cap.env)
	}
	if strings.Contains(cap.env, "ANTHROPIC_API_KEY") {
		t.Fatalf("LEAK: provider key NAME found in container env:\n%s", cap.env)
	}

	// The task still completed host-side and produced a real commit that merged
	// back to HEAD.
	if len(res.Commits) == 0 {
		t.Fatalf("expected at least one commit, got none; steps=%+v", res.Steps)
	}
	if res.Branch == "" {
		t.Error("merged run should report the task branch")
	}
	// note.txt was written INSIDE the container and is now on the repo's HEAD after
	// the merge.
	if got := gitOut(t, repo, "show", "HEAD:note.txt"); strings.TrimSpace(got) != "brokered-hands" {
		t.Errorf("HEAD:note.txt = %q, want brokered-hands", got)
	}
}

// TestRunTask_Keyless_StubCompletes proves the keyless path: the canned stub
// client (no ANTHROPIC_API_KEY) still drives RunTask to a graceful completion. The
// stub produces no actionable tool call, so the run commits nothing — but it must
// NOT error.
func TestRunTask_Keyless_StubCompletes(t *testing.T) {
	if !dockerReady() {
		t.Skip("docker unavailable; skipping real-container keyless test")
	}
	if !gitReady() {
		t.Skip("git unavailable")
	}

	repo := t.TempDir()
	gitInit(t, repo)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	res, err := RunTask(ctx, llm.NewStubClient(), nil, Request{
		RepoDir:  repo,
		Prompt:   "do something",
		Image:    "alpine:3.20",
		MaxSteps: 3,
		Scopes:   HandScopes(),
	})
	if err != nil {
		t.Fatalf("keyless stub RunTask should complete, got error: %v", err)
	}
	if len(res.Commits) != 0 {
		t.Errorf("keyless stub produced no actionable tool; want 0 commits, got %d", len(res.Commits))
	}
}

// TestRunTask_RejectsBrokerSecretInEnv unit-tests the enforcement helper directly:
// a Spec.Env carrying a provider secret is refused before any provider is touched.
func TestRunTask_RejectsBrokerSecretInEnv(t *testing.T) {
	if err := assertNoProviderSecret(map[string]string{"ANTHROPIC_API_KEY": "x"}); err == nil {
		t.Fatal("assertNoProviderSecret must reject a provider key in the sandbox env")
	}
	if err := assertNoProviderSecret(map[string]string{"LANG": "C", "PATH": "/usr/bin"}); err != nil {
		t.Fatalf("non-secret env should pass: %v", err)
	}
}

func marshalDecision(dec agentloop.Decision) string {
	// Minimal, dependency-free JSON for a Decision the agentloop parser accepts.
	var sb strings.Builder
	sb.WriteString(`{"tool":`)
	sb.WriteString(quoteJSON(dec.Tool))
	sb.WriteString(`,"args":{`)
	first := true
	for k, v := range dec.Args {
		if !first {
			sb.WriteString(",")
		}
		first = false
		sb.WriteString(quoteJSON(k))
		sb.WriteString(":")
		sb.WriteString(quoteJSON(v))
	}
	sb.WriteString("}}")
	return sb.String()
}

func quoteJSON(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
