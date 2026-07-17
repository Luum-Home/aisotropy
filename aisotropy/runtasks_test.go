//go:build keybroker

package aisotropy

import (
	"context"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// TestApp_RunTasks_Empty is a fast, docker-free check of the façade contract for an
// empty batch: an empty, non-nil slice and no error.
func TestApp_RunTasks_Empty(t *testing.T) {
	app := New(WithLLM(llm.NewStubClient()))
	outcomes, err := app.RunTasks(context.Background(), nil, RunTasksOptions{})
	if err != nil {
		t.Fatalf("empty RunTasks: %v", err)
	}
	if len(outcomes) != 0 {
		t.Fatalf("empty batch outcomes = %d, want 0", len(outcomes))
	}
}

// TestApp_RunTasks_KeylessConcurrentFacade drives the PUBLIC fan-out end-to-end:
// an App on the keyless stub runs two concurrent sandboxed tasks on separate repos
// through App.RunTasks and returns two mapped TaskOutcomes in index order, with a
// live started→done TaskEvent stream per task and no top-level error. Skips when
// docker/git are absent (the keyless stub commits nothing, so this proves the
// wiring, mapping, and streaming — not merge behavior, which the internal
// docker-gated suite covers).
func TestApp_RunTasks_KeylessConcurrentFacade(t *testing.T) {
	if !toolingReady() {
		t.Skip("docker or git unavailable; skipping façade RunTasks test")
	}

	const n = 2
	reqs := make([]TaskRequest, n)
	for i := range reqs {
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
		reqs[i] = TaskRequest{
			RepoDir:  repo,
			Prompt:   "make a change",
			Image:    "alpine:3.20",
			MaxSteps: 2,
			Scopes:   SandboxToolScopes(),
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	var mu sync.Mutex
	started := map[int]bool{}
	done := map[int]bool{}

	outcomes, err := app().RunTasks(ctx, reqs, RunTasksOptions{
		Concurrency: 2,
		OnEvent: func(ev TaskEvent) {
			mu.Lock()
			switch ev.Kind {
			case "started":
				started[ev.Index] = true
			case "done":
				done[ev.Index] = true
			}
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("App.RunTasks: %v", err)
	}
	if len(outcomes) != n {
		t.Fatalf("got %d outcomes, want %d", len(outcomes), n)
	}
	for i, o := range outcomes {
		if o.Index != i {
			t.Errorf("outcomes[%d].Index = %d, want %d", i, o.Index, i)
		}
		if o.Err != nil {
			t.Errorf("task %d errored: %v", i, o.Err)
		}
		if o.Result.Steps == nil {
			t.Errorf("task %d: expected a step trace, got nil", i)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for i := 0; i < n; i++ {
		if !started[i] {
			t.Errorf("task %d emitted no started event", i)
		}
		if !done[i] {
			t.Errorf("task %d emitted no done event", i)
		}
	}
}

// app builds a keyless-stub App for the façade test.
func app() *App { return New(WithLLM(llm.NewStubClient())) }
