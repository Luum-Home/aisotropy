//go:build keybroker

package aisotropy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/gitenv"
	"github.com/Luum-Home/aisotropy/internal/llm"
)

// blockingSandboxProvider is a non-docker SandboxProvider whose Create blocks
// until the per-task ctx is cancelled, modelling a task that hangs during sandbox
// setup. The only thing that frees it is the wall-clock deadline firing.
type blockingSandboxProvider struct{}

func (blockingSandboxProvider) Name() string { return "blocking-facade-provider" }
func (blockingSandboxProvider) Create(ctx context.Context, _ SandboxSpec) (Sandbox, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestApp_RunTask_MaxDurationTearsDownRunaway proves the PUBLIC façade threads the
// per-task deadline all the way into the runner: an App.RunTask with a short
// MaxDuration over a provider that would hang forever is torn down when the
// deadline fires, rather than idling the host process. Docker-free (git-gated):
// the hang is in the provider's Create, reached after the git worktree is added.
func TestApp_RunTask_MaxDurationTearsDownRunaway(t *testing.T) {
	if gitenv.Command(context.Background(), "git", "--version").Run() != nil {
		t.Skip("git unavailable; skipping façade deadline test")
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

	// The caller's ctx is generous; only the 150ms MaxDuration should tear the
	// task down. If MaxDuration were dropped on the floor this would hang until the
	// 30s ctx deadline instead of finishing in ~150ms.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	_, err := app.RunTask(ctx, TaskRequest{
		RepoDir:     repo,
		Prompt:      "hang",
		Image:       "alpine:3.20",
		MaxSteps:    1,
		MaxDuration: 150 * time.Millisecond,
		Scopes:      SandboxToolScopes(),
		Provider:    blockingSandboxProvider{},
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a deadline error from the runaway task")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("RunTask took %v; MaxDuration was not honored (deadline not threaded through)", elapsed)
	}
}
