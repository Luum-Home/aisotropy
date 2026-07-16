// Package keybrokering is an adversarial arena that proves — head-to-head, under
// REAL Docker containers — where a coding agent's provider key physically lives.
//
// The claim under test used to be "asserted (we read their code)". This arena
// upgrades it to "proven adversarially", the same way the isolation matrix in
// arenas/README.md turns "should isolate" into a reproduced cross-tenant leak:
// we run BOTH execution models against a live daemon and probe the container's
// own environment with `printenv`, the ground-truth of what a process inside the
// box can read.
//
// Two execution models, one probe:
//
//   - The coding-CLI-in-a-box (the "sandcastle" pattern). The model-calling
//     process (Claude Code, Codex, Cursor, …) runs INSIDE the container, so the
//     provider credential must be injected into the container env. Sandcastle
//     does exactly this: its startContainer maps the merged provider env into
//     `docker run … -e KEY=VALUE` flags (see src/DockerLifecycle.ts:126 in the
//     pinned clone, and the README's honest note). We reproduce that model
//     faithfully at the container-env level: stand up a container WITH the key in
//     its env via aisotropy's own Docker provider (the same `-e` mechanism), then
//     `printenv` from inside — the key is PRESENT. That is the security-relevant
//     fact, independent of which coding CLI runs on top.
//
//   - aisotropy's RunTask (the broker). The model call happens HOST-side; only
//     the tool EXECUTIONS the model chooses run inside the container, and the
//     sandbox Spec.Env is left nil by construction. We drive the REAL public
//     App.RunTask with a live provider key in the HOST process, wrap the Docker
//     provider in a probe, and assert the container env carries NEITHER the key's
//     value NOR its name — while the task still completes host-side and merges a
//     real commit to HEAD.
//
// Honesty gate: docker/git-gated (t.Skip when absent, like the rqlite/qdrant
// suites), no secret-shaped literal in source (the canary is assembled at
// runtime), and the caveats are documented in README.md — a full sandcastle run
// needs a real coding CLI + model, which this arena does not stand up; the
// key-INJECTION difference is what is provable at the container-env level, and it
// is the fact that matters.
package keybrokering

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/aisotropy"
	"github.com/Luum-Home/aisotropy/internal/llm"
)

// dockerReady reports whether a working docker CLI + daemon is reachable. The
// same honesty gate every real-container test in this repo uses, so `go test`
// never fails merely because Docker is absent.
func dockerReady() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "docker", "version").Run() == nil
}

// gitReady reports whether the git CLI is present (RunTask merges a real commit).
func gitReady() bool { return exec.Command("git", "--version").Run() == nil }

// gitInit stands up a throwaway repo with one commit so RunTask has a HEAD to
// branch a worktree off and merge back into.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "arena@example.test")
	run("config", "user.name", "arena")
	run("commit", "--allow-empty", "-q", "-m", "root")
}

// gitShow returns `git show <ref>` output (trimmed) from dir.
func gitShow(t *testing.T, dir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "show", ref)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git show %s: %v\n%s", ref, err, out)
	}
	return strings.TrimSpace(string(out))
}

// probingProvider wraps a real SandboxProvider and, at Create time (while the
// container is live), captures the container's full environment via `printenv`.
// This is the broker-invariant probe: for aisotropy's RunTask, the captured env
// must contain neither the host provider key's value nor its name. It is the
// public-API twin of internal/taskagent's capturingProvider.
type probingProvider struct {
	inner   aisotropy.SandboxProvider
	env     string
	execErr error
}

func (p *probingProvider) Name() string { return p.inner.Name() }

func (p *probingProvider) Create(ctx context.Context, spec aisotropy.SandboxSpec) (aisotropy.Sandbox, error) {
	sb, err := p.inner.Create(ctx, spec)
	if err != nil {
		return nil, err
	}
	res, eerr := sb.Exec(ctx, []string{"printenv"}, aisotropy.SandboxExecOptions{})
	p.env = res.Stdout
	p.execErr = eerr
	return sb, nil
}

// scriptedHostClient is a deterministic, keyless HOST-side llm.Client: each
// StreamChat emits the next pre-scripted tool Decision as JSON, so the public
// App.RunTask drives a real write_file → git_commit → stop sequence without any
// provider key or network. It stands in for "the model, running host-side" — the
// whole point being that this inference happens OUTSIDE the container.
type scriptedHostClient struct {
	mu    sync.Mutex
	i     int
	steps []decision
}

// decision mirrors internal/agentloop.Decision's wire shape ({"tool","args"});
// the agentloop parser accepts any JSON object of this form in the model output.
type decision struct {
	Tool string            `json:"tool"`
	Args map[string]string `json:"args"`
}

func (c *scriptedHostClient) Name() string { return "scripted-host-client" }

func (c *scriptedHostClient) StreamChat(_ context.Context, _, _ string) (<-chan llm.Token, error) {
	c.mu.Lock()
	var dec decision
	if c.i < len(c.steps) {
		dec = c.steps[c.i]
		c.i++
	} // past the end: emit the empty (stop) decision
	c.mu.Unlock()

	payload, _ := json.Marshal(dec)
	ch := make(chan llm.Token, 2)
	ch <- llm.Token{Text: string(payload)}
	ch <- llm.Token{Done: true}
	close(ch)
	return ch, nil
}

// TestKeyBrokering_HeadToHead is the adversarial proof. Side A reproduces the
// coding-CLI-in-a-box key injection (key PRESENT in the container env); Side B
// drives aisotropy's real RunTask with the key live in the HOST process and
// proves the task container env is key-free (key ABSENT) while the task still
// completes and merges a commit.
func TestKeyBrokering_HeadToHead(t *testing.T) {
	if !dockerReady() {
		t.Skip("docker unavailable; skipping key-brokering arena")
	}
	if !gitReady() {
		t.Skip("git unavailable; skipping key-brokering arena")
	}

	const image = "alpine:3.20"

	// Assemble the canary at runtime so no key-shaped literal lives in source
	// (dummy value; the repo content policy rejects key-shaped literals). It
	// stands in for a real provider key.
	canary := "sk-" + "ant-" + "DUMMYcanary-" + t.Name() + "-DO-NOT-LEAK"
	const keyName = "ANTHROPIC_API_KEY"

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	// ---- Side A: coding-CLI-in-a-box (the sandcastle key-injection model) ----
	// Faithful minimal repro at the container-env level: sandcastle injects the
	// provider credential via `docker run … -e KEY=VALUE` (DockerLifecycle.ts:126)
	// because the coding agent runs INSIDE the box. We do the same through
	// aisotropy's own Docker provider by putting the key in Spec.Env — the exact
	// `-e` mechanism — and then read it back from inside the container.
	sandcastleEnv := sideACodingCLIInABox(ctx, t, image, keyName, canary)
	if !strings.Contains(sandcastleEnv, canary) {
		t.Fatalf("Side A (coding-CLI-in-a-box): expected key VALUE in container env, absent:\n%s", sandcastleEnv)
	}
	if !strings.Contains(sandcastleEnv, keyName) {
		t.Fatalf("Side A (coding-CLI-in-a-box): expected key NAME in container env, absent:\n%s", sandcastleEnv)
	}
	t.Logf("Side A  coding-CLI-in-a-box : %s PRESENT in container env (injected via docker -e, sandcastle model)", keyName)

	// ---- Side B: aisotropy RunTask (the broker) ----
	// A real provider key lives in the HOST process env; RunTask must keep it
	// host-side and stand up a key-free task container.
	t.Setenv(keyName, canary)

	repo := t.TempDir()
	gitInit(t, repo)

	probe := &probingProvider{inner: aisotropy.DockerSandbox()}
	host := &scriptedHostClient{steps: []decision{
		{Tool: "write_file", Args: map[string]string{"path": "note.txt", "content": "brokered-hands"}},
		{Tool: "git_commit", Args: map[string]string{"message": "add note from sandbox"}},
		{Tool: ""}, // stop
	}}
	app := aisotropy.New(aisotropy.WithLLM(host))

	res, err := app.RunTask(ctx, aisotropy.TaskRequest{
		RepoDir:  repo,
		Prompt:   "create note.txt and commit it",
		Image:    image,
		MaxSteps: 6,
		Scopes:   aisotropy.SandboxToolScopes(),
		Provider: probe,
	})
	if err != nil {
		t.Fatalf("Side B: App.RunTask: %v", err)
	}

	// Broker invariant: the task container env carries neither value nor name.
	if probe.execErr != nil {
		t.Fatalf("Side B: printenv probe failed: %v", probe.execErr)
	}
	if probe.env == "" {
		t.Fatal("Side B: captured container env is empty; probe did not run")
	}
	if strings.Contains(probe.env, canary) {
		t.Fatalf("LEAK: Side B task container env carries the key VALUE:\n%s", probe.env)
	}
	if strings.Contains(probe.env, keyName) {
		t.Fatalf("LEAK: Side B task container env carries the key NAME:\n%s", probe.env)
	}

	// …and the task still ran host-side: a real commit written by the sandboxed
	// hands merged back to HEAD.
	if len(res.Commits) == 0 {
		t.Fatalf("Side B: expected a commit from the host-driven task, got none; steps=%+v", res.Steps)
	}
	if got := gitShow(t, repo, "HEAD:note.txt"); got != "brokered-hands" {
		t.Fatalf("Side B: HEAD:note.txt = %q, want brokered-hands (task work missing)", got)
	}
	t.Logf("Side B  aisotropy RunTask   : %s ABSENT in container env; task still ran (commit %s merged to HEAD)", keyName, short(res.Commits[0]))

	t.Log("PASS: coding-CLI-in-a-box: key PRESENT in container env; aisotropy RunTask: key ABSENT, task still ran")
}

// sideACodingCLIInABox stands up one container with the key in its env (the
// coding-CLI-in-a-box model) and returns what `printenv` sees from inside. The
// container is torn down before return.
func sideACodingCLIInABox(ctx context.Context, t *testing.T, image, keyName, canary string) string {
	t.Helper()
	provider := aisotropy.DockerSandbox()
	sb, err := provider.Create(ctx, aisotropy.SandboxSpec{
		Image: image,
		// The key IS injected into the container env — exactly sandcastle's
		// `docker run … -e KEY=VALUE`, because the coding agent runs inside.
		Env: map[string]string{keyName: canary},
	})
	if err != nil {
		t.Fatalf("Side A: sandbox create: %v", err)
	}
	defer func() { _ = sb.Close(context.WithoutCancel(ctx)) }()

	res, err := sb.Exec(ctx, []string{"printenv"}, aisotropy.SandboxExecOptions{})
	if err != nil {
		t.Fatalf("Side A: printenv: %v", err)
	}
	return res.Stdout
}

// short trims a commit SHA for logging.
func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}
