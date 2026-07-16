package taskagent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/agentloop"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/sandbox"
)

// constClient is a stateless, keyless host-side llm.Client: every StreamChat emits
// the SAME pre-scripted Decision. Being stateless it is safe to share across the
// concurrent tasks of a fan-out (unlike scriptedClient, whose cursor would
// interleave nondeterministically), so a RunTasks unit test stays deterministic.
// It is NOT a llm.ToolCaller, so it drives the structured-output agentloop.
type constClient struct{ dec agentloop.Decision }

func (c *constClient) Name() string { return "const-test-client" }

func (c *constClient) StreamChat(_ context.Context, _, _ string) (<-chan llm.Token, error) {
	ch := make(chan llm.Token, 2)
	ch <- llm.Token{Text: marshalDecision(c.dec)}
	ch <- llm.Token{Done: true}
	close(ch)
	return ch, nil
}

// streamingSandbox is a non-docker sandbox.Sandbox whose Exec invokes opts.OnLine
// (as the real Exec contract requires) so the fan-out's "shell" events fire without
// a container. It records nothing; it just streams one canned line and returns.
type streamingSandbox struct{ line string }

func (s *streamingSandbox) Exec(_ context.Context, _ []string, opts sandbox.ExecOptions) (sandbox.ExecResult, error) {
	if opts.OnLine != nil {
		opts.OnLine(s.line)
	}
	return sandbox.ExecResult{Stdout: s.line + "\n"}, nil
}
func (s *streamingSandbox) CopyIn(context.Context, string, string) error  { return nil }
func (s *streamingSandbox) CopyOut(context.Context, string, string) error { return nil }
func (s *streamingSandbox) Workdir() string                               { return sandbox.DefaultWorkdir }
func (s *streamingSandbox) Close(context.Context) error                   { return nil }

// fakeProvider is a non-docker sandbox.Provider. Create returns createErr when set
// (to inject a task failure), otherwise a fresh streamingSandbox.
type fakeProvider struct{ createErr error }

func (p *fakeProvider) Name() string { return "fake-provider" }
func (p *fakeProvider) Create(context.Context, sandbox.Spec) (sandbox.Sandbox, error) {
	if p.createErr != nil {
		return nil, p.createErr
	}
	return &streamingSandbox{line: "brokered-hands"}, nil
}

// TestRunTasks_FanOut_OrderIsolationEvents is the fast, docker-free proof of the
// fan-out contract. It needs only git (for the per-task worktree) and skips if git
// is absent. Four tasks on FOUR separate repos run at Concurrency=2 with one
// deliberately-failing task; the test asserts:
//   - outcomes come back in INDEX order (Outcomes[i].Index == i);
//   - PER-TASK error isolation: the failing task carries Err, the others succeed;
//   - a live started→step→shell→done Event stream is emitted per successful task,
//     and started→error for the failing one.
//
// Run under -race (the package's closed-loop uses `go test -race`), the shared
// outcomes slice (distinct indices), the serialized emit, and the per-repo gates
// must show no data race.
func TestRunTasks_FanOut_OrderIsolationEvents(t *testing.T) {
	if !gitReady() {
		t.Skip("git unavailable")
	}

	const n = 4
	const failIdx = 2

	reqs := make([]Request, n)
	for i := range reqs {
		repo := t.TempDir()
		gitInit(t, repo)
		prov := &fakeProvider{}
		if i == failIdx {
			prov = &fakeProvider{createErr: errors.New("boom: injected create failure")}
		}
		reqs[i] = Request{
			RepoDir:  repo,
			Prompt:   "task",
			Image:    "alpine:3.20",
			MaxSteps: 1, // exactly one run_shell step per task → deterministic events
			Scopes:   HandScopes(),
			Provider: prov,
		}
	}

	client := &constClient{dec: agentloop.Decision{
		Tool: ToolRunShell,
		Args: map[string]string{"cmd": "echo hi"},
	}}

	// The consumer's OnEvent is invoked serially by RunTasks, but keep a mutex so a
	// -race run also proves the emit-serialization contract holds.
	var mu sync.Mutex
	byIndex := map[int][]string{} // index -> ordered event kinds

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	outcomes, err := RunTasks(ctx, client, nil, reqs, Options{
		Concurrency: 2,
		OnEvent: func(ev Event) {
			mu.Lock()
			byIndex[ev.Index] = append(byIndex[ev.Index], ev.Kind)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("RunTasks top-level error (want nil; a task failure must not fail the batch): %v", err)
	}

	// Index order.
	if len(outcomes) != n {
		t.Fatalf("got %d outcomes, want %d", len(outcomes), n)
	}
	for i, o := range outcomes {
		if o.Index != i {
			t.Errorf("outcomes[%d].Index = %d, want %d (out of order)", i, o.Index, i)
		}
		if o.Request.RepoDir != reqs[i].RepoDir {
			t.Errorf("outcomes[%d] carries the wrong request", i)
		}
	}

	// Per-task error isolation.
	if outcomes[failIdx].Err == nil {
		t.Errorf("task %d was injected to fail but Err is nil", failIdx)
	}
	for i, o := range outcomes {
		if i == failIdx {
			continue
		}
		if o.Err != nil {
			t.Errorf("task %d should have succeeded despite task %d failing, got Err: %v", i, failIdx, o.Err)
		}
		if len(o.Result.Steps) != 1 {
			t.Errorf("task %d: got %d steps, want 1", i, len(o.Result.Steps))
		}
	}

	// Events per task.
	mu.Lock()
	defer mu.Unlock()
	for i := 0; i < n; i++ {
		kinds := byIndex[i]
		if len(kinds) == 0 {
			t.Errorf("task %d emitted no events", i)
			continue
		}
		if kinds[0] != EventStarted {
			t.Errorf("task %d first event = %q, want %q", i, kinds[0], EventStarted)
		}
		last := kinds[len(kinds)-1]
		if i == failIdx {
			if last != EventError {
				t.Errorf("failing task %d terminal event = %q, want %q", i, last, EventError)
			}
			if has(kinds, EventDone) {
				t.Errorf("failing task %d must not emit a done event: %v", i, kinds)
			}
			continue
		}
		if last != EventDone {
			t.Errorf("task %d terminal event = %q, want %q", i, last, EventDone)
		}
		if !has(kinds, EventStep) {
			t.Errorf("task %d emitted no step event: %v", i, kinds)
		}
		if !has(kinds, EventShell) {
			t.Errorf("task %d emitted no shell event: %v", i, kinds)
		}
	}
}

// TestRunTasks_CancelledContextIsTopLevelError proves the whole-batch failure
// contract: a context cancelled before the fan-out starts stops scheduling and is
// returned as the TOP-LEVEL error (not swallowed into per-task Err only), with the
// unscheduled tasks marked with the context error.
func TestRunTasks_CancelledContextIsTopLevelError(t *testing.T) {
	if !gitReady() {
		t.Skip("git unavailable")
	}
	reqs := make([]Request, 3)
	for i := range reqs {
		repo := t.TempDir()
		gitInit(t, repo)
		reqs[i] = Request{RepoDir: repo, Prompt: "x", MaxSteps: 1, Scopes: HandScopes(), Provider: &fakeProvider{}}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel up front

	outcomes, err := RunTasks(ctx, &constClient{dec: agentloop.Decision{Tool: ""}}, nil, reqs, Options{Concurrency: 2})
	if err == nil {
		t.Fatal("cancelled context must be the top-level error")
	}
	if len(outcomes) != len(reqs) {
		t.Fatalf("got %d outcomes, want %d (outcomes must still be returned)", len(outcomes), len(reqs))
	}
	for i, o := range outcomes {
		if o.Err == nil {
			t.Errorf("outcome %d under a cancelled batch should carry an error", i)
		}
	}
}

// TestRunTasks_Empty returns an empty, non-nil result and no error.
func TestRunTasks_Empty(t *testing.T) {
	outcomes, err := RunTasks(context.Background(), &constClient{}, nil, nil, Options{})
	if err != nil {
		t.Fatalf("empty batch error: %v", err)
	}
	if len(outcomes) != 0 {
		t.Fatalf("empty batch outcomes = %d, want 0", len(outcomes))
	}
}

func has(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
