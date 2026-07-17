//go:build keybroker

// Package keybrokering proves aisotropy's OWN key-broker invariant with running
// code, and honestly situates it against the wider agent-sandbox field.
//
// aisotropy's RunTask is a clean implementation of the STANDARD "sandbox-as-tool"
// pattern (LangChain's "Pattern 2"): the model runs HOST-side, only tool execution
// is sandboxed, and the provider key never enters the sandbox. That is NOT an
// aisotropy innovation — it is table stakes across the field (E2B, Daytona,
// deepagents, the OpenAI Agents SDK / Anthropic code-interpreter integrations all
// run the model host-side and keep the key out of the sandbox). aisotropy's
// contribution is narrower: the pattern is wired-by-default with the invariant
// ENFORCED in code (Spec.Env is left nil + assertNoProviderSecret), and there is a
// real, narrower edge specifically against coding-CLI-in-a-box orchestrators.
//
// Two execution models, one probe — but note the ASYMMETRY in what each proves:
//
//   - The coding-CLI-in-a-box (the "sandcastle" pattern). An UNMODIFIED third-party
//     coding CLI (Claude Code, Codex, Cursor, …) runs INSIDE the container, so —
//     under sandcastle's no-proxy design — the provider credential must be injected
//     into the container env. Sandcastle's own README states it: "No API key proxy
//     exists. The key flows directly into the container environment," and its
//     startContainer maps the merged provider env into `docker run … -e KEY=VALUE`
//     flags (src/DockerLifecycle.ts:126 in the pinned clone). Side A does NOT run
//     sandcastle's binary; it is aisotropy's own container reproducing that `-e`
//     injection mechanism to show `printenv` sees the key — a minimal repro of
//     sandcastle's DOCUMENTED behavior. Sandcastle's behavior is established by the
//     source citation in README.md, not by this test.
//
//   - aisotropy's RunTask (Pattern 2, invariant enforced) — the genuinely sound
//     proof. The model call happens HOST-side; only the tool EXECUTIONS the model
//     chooses run inside the container, and the sandbox Spec.Env is left nil by
//     construction. We drive the REAL public App.RunTask with a live provider key
//     in the HOST process, wrap the Docker provider in a probe, and assert the
//     container env carries NEITHER the key's value NOR its name — while the task
//     still completes host-side and merges a real commit to HEAD.
//
// Nuance: key-in-container is intrinsic to sandcastle's NO-PROXY design specifically,
// not a law of the category. A proxy sidecar (LiteLLM-style, or Claude Code pointed
// at ANTHROPIC_BASE_URL with a dummy token) could keep the real key host-side even
// with a CLI in the box.
//
// Honesty gate: docker/git-gated (t.Skip when absent, like the rqlite/qdrant
// suites), no secret-shaped literal in source (the canary is assembled at
// runtime), and the caveats are documented in README.md — a full sandcastle run
// needs a real coding CLI + model, which this arena does not stand up.
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

// TestKeyBrokering_HeadToHead proves aisotropy's own invariant. Side A is a repro,
// not an adversarial contest: it stands up aisotropy's OWN container with the key
// injected via `-e` to show `printenv` sees it (a minimal repro of sandcastle's
// documented behavior — sandcastle's binary is NOT run; see the source citation in
// README.md). Side B is the genuinely sound proof: it drives aisotropy's real
// public RunTask with the key live in the HOST process and proves the task
// container env is key-free (key ABSENT) while the task still completes and merges
// a commit.
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

	// ---- Side A: repro of sandcastle's documented `-e` key injection ----
	// NOT sandcastle's binary and NOT an adversarial contest — aisotropy's own
	// container reproducing the injection mechanism sandcastle documents (`docker
	// run … -e KEY=VALUE`, DockerLifecycle.ts:126, under its no-proxy design). We
	// put the key in Spec.Env — the exact `-e` mechanism — and read it back from
	// inside the container to show `printenv` sees it. This proves the near-
	// tautology "if you pass -e KEY=VALUE, printenv shows KEY"; sandcastle's actual
	// behavior is established by the source citation in README.md.
	sandcastleEnv := sideACodingCLIInABox(ctx, t, image, keyName, canary)
	if !strings.Contains(sandcastleEnv, canary) {
		t.Fatalf("Side A (sandcastle injection repro): expected key VALUE in container env, absent:\n%s", sandcastleEnv)
	}
	if !strings.Contains(sandcastleEnv, keyName) {
		t.Fatalf("Side A (sandcastle injection repro): expected key NAME in container env, absent:\n%s", sandcastleEnv)
	}
	t.Logf("Side A  sandcastle injection repro : %s PRESENT in container env (our own '-e' repro of sandcastle's documented behavior)", keyName)

	// ---- Side B: aisotropy RunTask (Pattern 2, invariant enforced) ----
	// The genuinely sound proof. A real provider key lives in the HOST process env;
	// RunTask must keep it host-side and stand up a key-free task container.
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
	t.Logf("Side B  aisotropy RunTask          : %s ABSENT in container env; task still ran (commit %s merged to HEAD)", keyName, short(res.Commits[0]))

	t.Log("PASS: '-e' injection is printenv-visible (sandcastle model); aisotropy RunTask: key ABSENT, task still ran")
}

// sideACodingCLIInABox stands up one of aisotropy's OWN containers with the key in
// its env and returns what `printenv` sees from inside — a minimal repro of the
// `-e` injection sandcastle documents (it does NOT run sandcastle). The container
// is torn down before return.
func sideACodingCLIInABox(ctx context.Context, t *testing.T, image, keyName, canary string) string {
	t.Helper()
	provider := aisotropy.DockerSandbox()
	sb, err := provider.Create(ctx, aisotropy.SandboxSpec{
		Image: image,
		// The key IS injected into the container env — the same `docker run …
		// -e KEY=VALUE` mechanism sandcastle uses under its no-proxy design.
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
