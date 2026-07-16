package aisotropy

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

func toolingReady() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dockerOK := exec.CommandContext(ctx, "docker", "version").Run() == nil
	gitOK := exec.Command("git", "--version").Run() == nil
	return dockerOK && gitOK
}

// TestApp_RunTask_KeylessFacade drives the PUBLIC façade end-to-end: an App built
// with the keyless stub client runs a sandboxed task through App.RunTask and
// returns a mapped TaskResult without error. Skips when docker/git are absent
// (the same honesty gate the internal suite uses).
func TestApp_RunTask_KeylessFacade(t *testing.T) {
	if !toolingReady() {
		t.Skip("docker or git unavailable; skipping façade RunTask test")
	}

	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := exec.Command("git", "-C", repo, "commit", "-q", "--allow-empty", "-m", "seed").Run(); err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	app := New(WithLLM(llm.NewStubClient()))

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	res, err := app.RunTask(ctx, TaskRequest{
		RepoDir:  repo,
		Prompt:   "make a change",
		Image:    "alpine:3.20",
		MaxSteps: 3,
		Scopes:   SandboxToolScopes(),
	})
	if err != nil {
		t.Fatalf("App.RunTask: %v", err)
	}
	// The keyless stub commits nothing but the façade must return a well-formed,
	// mapped result.
	if len(res.Commits) != 0 {
		t.Errorf("keyless stub: want 0 commits, got %d", len(res.Commits))
	}
	if res.Steps == nil {
		t.Error("expected a (possibly single no-tool) step trace, got nil")
	}
}
