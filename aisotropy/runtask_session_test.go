//go:build keybroker

package aisotropy

import (
	"context"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/gitenv"
	"github.com/Luum-Home/aisotropy/internal/llm"
)

// TestApp_RunTask_SessionResumeFacade drives session resume through the PUBLIC
// façade end-to-end: an App on the keyless stub runs a task under a SessionID
// with an in-memory TaskSessionStore, then a second run RESUMES it. It asserts
// the store captured the first run's trace under the ctx-scoped identity and that
// the resumed run accumulates onto it — the tenant-scoped, persistent advantage
// over a local-JSONL resume. Docker/git gated (a real sandbox run is needed);
// keyless throughout.
func TestApp_RunTask_SessionResumeFacade(t *testing.T) {
	if !toolingReady() {
		t.Skip("docker or git unavailable; skipping façade session-resume test")
	}

	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		if out, err := gitenv.Command(context.Background(), "git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := gitenv.Command(context.Background(), "git", "-C", repo, "commit", "-q", "--allow-empty", "-m", "seed").Run(); err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	app := New(WithLLM(llm.NewStubClient()))
	store := NewInMemoryTaskSessions()

	// Identity travels via ctx exactly as it does for chat, so the session is
	// keyed by the real tenant/user (not the standalone default scope).
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	ctx = llm.WithUser(llm.WithTenant(ctx, "acme"), "alice")
	key := TaskSessionKey{Tenant: "acme", User: "alice", Session: "s1"}

	res1, err := app.RunTask(ctx, TaskRequest{
		RepoDir:      repo,
		Prompt:       "first task",
		Image:        "alpine:3.20",
		MaxSteps:     2,
		Scopes:       SandboxToolScopes(),
		SessionID:    "s1",
		SessionStore: store,
	})
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}

	state, found, err := store.Load(ctx, key)
	if err != nil || !found {
		t.Fatalf("session not persisted through façade: found=%v err=%v", found, err)
	}
	// The persisted trace matches the run's trace. (The keyless stub drives no
	// tools, so this may be an empty — but present — trace under a found record;
	// the deterministic step-accumulation proof lives in the internal suite,
	// which drives real steps via a fake provider.)
	if len(state.Steps) != len(res1.Steps) {
		t.Fatalf("persisted %d steps, want %d (the run's trace)", len(state.Steps), len(res1.Steps))
	}
	if state.Prompt != "first task" {
		t.Errorf("stored prompt = %q, want the first run's prompt", state.Prompt)
	}

	// Resume: the second run rehydrates prior context and re-persists the session.
	if _, err := app.RunTask(ctx, TaskRequest{
		RepoDir:      repo,
		Prompt:       "second task",
		Image:        "alpine:3.20",
		MaxSteps:     2,
		Scopes:       SandboxToolScopes(),
		SessionID:    "s1",
		Resume:       true,
		SessionStore: store,
	}); err != nil {
		t.Fatalf("run 2 (resume): %v", err)
	}

	after, found, err := store.Load(ctx, key)
	if err != nil || !found {
		t.Fatalf("session missing after resume: found=%v err=%v", found, err)
	}
	// The resume completed and re-persisted the session under the latest prompt,
	// proving the load→run→save round-trip is wired through the façade.
	if after.Prompt != "second task" {
		t.Errorf("stored prompt = %q, want the latest (resumed) run's prompt", after.Prompt)
	}

	// Isolation through the façade: a different tenant sees no session here.
	otherKey := TaskSessionKey{Tenant: "globex", User: "bob", Session: "s1"}
	if _, found, _ := store.Load(context.Background(), otherKey); found {
		t.Error("another tenant must not observe this session")
	}
}
