package taskagent

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/agentloop"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/sandbox"
)

// promptScriptedClient is a stateless, keyless host-side client that drives a
// write_file → git_commit → stop sequence, keyed entirely on the accumulated
// prompt so it is deterministic even when SHARED across concurrent tasks. Each
// task's Request.Prompt names a unique file (note-<i>.txt); the client extracts
// that name (it persists as a prefix of the running prompt) and advances stage by
// spotting the tool-result markers the agentloop appends. Different files per task
// mean the concurrent merges never conflict — so a clean run proves serialized
// merges keep HEAD intact, not that conflicts are magically resolved.
type promptScriptedClient struct{}

var noteFileRe = regexp.MustCompile(`note-\d+\.txt`)

func (promptScriptedClient) Name() string { return "prompt-scripted-client" }

func (promptScriptedClient) StreamChat(_ context.Context, _, user string) (<-chan llm.Token, error) {
	fname := noteFileRe.FindString(user)
	var dec agentloop.Decision
	switch {
	case fname == "":
		dec = agentloop.Decision{} // nothing to do → stop
	case strings.Contains(user, `Tool "git_commit" returned`):
		dec = agentloop.Decision{} // committed already → stop
	case strings.Contains(user, `Tool "write_file" returned`):
		dec = agentloop.Decision{Tool: ToolGitCommit, Args: map[string]string{"message": "add " + fname}}
	default:
		dec = agentloop.Decision{Tool: ToolWriteFile, Args: map[string]string{"path": fname, "content": "hands-" + fname}}
	}
	ch := make(chan llm.Token, 2)
	ch <- llm.Token{Text: marshalDecision(dec)}
	ch <- llm.Token{Done: true}
	close(ch)
	return ch, nil
}

// TestRunTasks_ConcurrentDocker_KeyFree_NoHeadCorruption is the docker-gated proof
// of the fan-out under REAL containers on a SHARED repo. It runs three concurrent
// tasks, each writing its own file inside its own container and committing, all
// merging back into the SAME HEAD, and asserts:
//   - every task completes with a commit and a task branch;
//   - every container's environment is KEY-FREE (the broker invariant, probed the
//     same way ADR-0036's single-task test does);
//   - the serialized, concurrent merges leave HEAD UNCORRUPTED — a clean working
//     tree, all three files present, and three merge commits recorded.
//
// Skips when docker/git are absent (honesty gate).
func TestRunTasks_ConcurrentDocker_KeyFree_NoHeadCorruption(t *testing.T) {
	if !dockerReady() {
		t.Skip("docker unavailable; skipping concurrent real-container fan-out test")
	}
	if !gitReady() {
		t.Skip("git unavailable")
	}

	canary := "sk-" + "ant-" + "DUMMYcanary-" + t.Name() + "-DO-NOT-LEAK"
	t.Setenv("ANTHROPIC_API_KEY", canary)

	repo := t.TempDir()
	gitInit(t, repo)

	const n = 3
	caps := make([]*capturingProvider, n)
	reqs := make([]Request, n)
	for i := range reqs {
		caps[i] = &capturingProvider{inner: sandbox.NewDockerProvider()}
		reqs[i] = Request{
			RepoDir:  repo, // SHARED repo — this is the merge-race surface
			Prompt:   "create note-" + itoa3(i) + ".txt and commit it",
			Image:    "alpine:3.20",
			MaxSteps: 4,
			Scopes:   HandScopes(),
			Provider: caps[i],
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	var mu sync.Mutex
	commitEvents := 0
	outcomes, err := RunTasks(ctx, promptScriptedClient{}, nil, reqs, Options{
		Concurrency: n,
		OnEvent: func(ev Event) {
			if ev.Kind == EventCommit {
				mu.Lock()
				commitEvents++
				mu.Unlock()
			}
		},
	})
	if err != nil {
		t.Fatalf("RunTasks top-level error: %v", err)
	}

	// Every task completed with a commit and a branch.
	for i, o := range outcomes {
		if o.Err != nil {
			t.Fatalf("task %d failed: %v", i, o.Err)
		}
		if len(o.Result.Commits) == 0 {
			t.Fatalf("task %d produced no commit; steps=%+v", i, o.Result.Steps)
		}
		if o.Result.Branch == "" {
			t.Errorf("task %d merged but reports no branch", i)
		}
	}
	if commitEvents < n {
		t.Errorf("got %d commit events, want >= %d", commitEvents, n)
	}

	// Broker invariant: NO container leaked the key value or name.
	for i, c := range caps {
		if c.execErr != nil {
			t.Fatalf("task %d printenv probe failed: %v", i, c.execErr)
		}
		if c.env == "" {
			t.Fatalf("task %d captured env empty; probe did not run", i)
		}
		if strings.Contains(c.env, canary) {
			t.Fatalf("LEAK: task %d container env carries the key VALUE:\n%s", i, c.env)
		}
		if strings.Contains(c.env, "ANTHROPIC_API_KEY") {
			t.Fatalf("LEAK: task %d container env carries the key NAME:\n%s", i, c.env)
		}
	}

	// HEAD is uncorrupted: clean working tree, all files present, N merge commits.
	if st := strings.TrimSpace(gitOut(t, repo, "status", "--porcelain")); st != "" {
		t.Errorf("repo HEAD left dirty after concurrent merges:\n%s", st)
	}
	for i := 0; i < n; i++ {
		fname := "note-" + itoa3(i) + ".txt"
		got := strings.TrimSpace(gitOut(t, repo, "show", "HEAD:"+fname))
		if want := "hands-" + fname; got != want {
			t.Errorf("HEAD:%s = %q, want %q (a merge dropped this task's work)", fname, got, want)
		}
	}
	merges := strings.TrimSpace(gitOut(t, repo, "rev-list", "--merges", "--count", "HEAD"))
	if merges != itoa3(n) {
		t.Errorf("merge-commit count = %s, want %d (concurrent merges corrupted history)", merges, n)
	}
}

// itoa3 is a local, dependency-free small-int formatter for the test.
func itoa3(n int) string { return string(rune('0' + n)) }
