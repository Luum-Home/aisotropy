package aisotropy

import (
	"context"
	"os/exec"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// TestOpenTaskSessions_PersistRoundTrip exercises the durable, disk-backed task
// session store the façade exposes: OpenTaskSessions creates a BadgerDB store
// under a temp dir, a Save persists a session under a tenant-scoped key, and a
// Load reads it back. No docker/git needed — this is the persistence seam, not a
// sandbox run.
func TestOpenTaskSessions_PersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenTaskSessions(dir)
	if err != nil {
		t.Fatalf("OpenTaskSessions: %v", err)
	}
	defer func() {
		if cerr := store.Close(); cerr != nil {
			t.Errorf("Close: %v", cerr)
		}
	}()

	ctx := context.Background()
	key := TaskSessionKey{Tenant: "acme", User: "alice", Session: "s1"}
	state := TaskSessionState{Prompt: "do the thing"}
	if err := store.Save(ctx, key, state); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, found, err := store.Load(ctx, key)
	if err != nil || !found {
		t.Fatalf("Load: found=%v err=%v", found, err)
	}
	if got.Prompt != "do the thing" {
		t.Errorf("loaded prompt = %q, want the saved prompt", got.Prompt)
	}

	// Isolation: a different tenant sees nothing under the same session id.
	if _, found, _ := store.Load(ctx, TaskSessionKey{Tenant: "globex", User: "bob", Session: "s1"}); found {
		t.Error("another tenant must not observe this session")
	}
}

// TestApp_RunTask_InvalidRepoReturnsError covers RunTask's error-return branch
// (the taskagent.RunTask failure is surfaced, not swallowed into a zero
// TaskResult) without needing a real sandbox: a RepoDir that is not a git
// repository fails at worktree setup, before any container is stood up.
func TestApp_RunTask_InvalidRepoReturnsError(t *testing.T) {
	if exec.Command("git", "--version").Run() != nil {
		t.Skip("git unavailable; skipping RunTask invalid-repo error-path test")
	}
	app := New(WithLLM(llm.NewStubClient()))

	// t.TempDir() is a plain directory, not a git repo: worktree creation fails.
	_, err := app.RunTask(context.Background(), TaskRequest{
		RepoDir:  t.TempDir(),
		Prompt:   "make a change",
		MaxSteps: 1,
	})
	if err == nil {
		t.Fatal("RunTask on a non-git RepoDir must return an error")
	}
}
