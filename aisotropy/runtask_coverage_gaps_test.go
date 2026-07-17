//go:build keybroker

package aisotropy

import (
	"context"
	"os/exec"
	"testing"
)

// TestRunTask_NoLLMClientFallback covers RunTask's client==nil fallback branch
// (llm.FromEnv when the App has no WithLLM) together with its error-return path:
// New() with no client + a non-git RepoDir fails at worktree setup, before any
// container, exercising the fallback client selection on the error path.
//
// Extracted from coverage_gaps_test.go under the keybroker build tag: RunTask and
// its TaskRequest surface are compiled only under -tags keybroker.
func TestRunTask_NoLLMClientFallback(t *testing.T) {
	if exec.Command("git", "--version").Run() != nil {
		t.Skip("git unavailable; skipping RunTask no-client fallback test")
	}
	// No WithLLM: RunTask must fall back to llm.FromEnv() (the keyless stub with
	// no ANTHROPIC_API_KEY), then fail on the invalid repo.
	app := New()

	if _, err := app.RunTask(context.Background(), TaskRequest{
		RepoDir:  t.TempDir(),
		Prompt:   "make a change",
		MaxSteps: 1,
	}); err == nil {
		t.Fatal("RunTask on a non-git RepoDir must return an error")
	}
}

// TestRunTasks_NoLLMClientAndMapping covers RunTasks' client==nil fallback, the
// per-outcome toTaskResult mapping, and the OnEvent adapter without docker: one
// invalid-repo task fails at git worktree setup, still yielding a mapped outcome
// carrying the error.
func TestRunTasks_NoLLMClientAndMapping(t *testing.T) {
	if exec.Command("git", "--version").Run() != nil {
		t.Skip("git unavailable; skipping RunTasks mapping test")
	}
	app := New() // no WithLLM -> client==nil fallback path

	var events int
	outcomes, err := app.RunTasks(context.Background(),
		[]TaskRequest{{RepoDir: t.TempDir(), Prompt: "change", MaxSteps: 1}},
		RunTasksOptions{OnEvent: func(TaskEvent) { events++ }},
	)
	_ = err // a batch-level error may or may not be set; the per-task error is the contract
	if len(outcomes) != 1 {
		t.Fatalf("outcomes = %d, want 1", len(outcomes))
	}
	if outcomes[0].Err == nil {
		t.Fatal("the invalid-repo task must carry a per-outcome error")
	}
	if outcomes[0].Index != 0 {
		t.Errorf("outcome index = %d, want 0", outcomes[0].Index)
	}
	// toTaskResult always runs on the mapping path; a failed task has no commits.
	if len(outcomes[0].Result.Commits) != 0 {
		t.Errorf("a failed task must map to zero commits, got %d", len(outcomes[0].Result.Commits))
	}
}
