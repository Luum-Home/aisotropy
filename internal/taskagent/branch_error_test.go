package taskagent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"

	"github.com/Luum-Home/aisotropy/internal/agentloop"
	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/sandbox"
)

// --- pure helpers ---------------------------------------------------------

// TestFormatExecResultNonZeroExit covers formatExecResult's failure rendering:
// a non-zero exit folds the exit note plus (trimmed) stdout and stderr into the
// result string handed back to the model.
func TestFormatExecResultNonZeroExit(t *testing.T) {
	got := formatExecResult("run_shell", sandbox.ExecResult{ExitCode: 2, Stdout: " out ", Stderr: " boom "})
	for _, want := range []string{"run_shell: exited 2", "stdout:", "out", "stderr:", "boom"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatExecResult = %q, missing %q", got, want)
		}
	}
}

// TestDecodeStringArgsInvalidJSON covers decodeStringArgs's unmarshal-error
// branch: malformed JSON yields an empty map rather than a partial/garbage one.
func TestDecodeStringArgsInvalidJSON(t *testing.T) {
	got := decodeStringArgs(`{"cmd": `) // truncated / invalid
	if len(got) != 0 {
		t.Fatalf("decodeStringArgs(invalid) = %v, want an empty map", got)
	}
}

// TestPreviewTruncatesLongResult covers preview's length cap for a step Event.
func TestPreviewTruncatesLongResult(t *testing.T) {
	got := preview(strings.Repeat("a", 300))
	if !strings.HasSuffix(got, "…") || len(got) > 200+len("…") {
		t.Fatalf("preview length not capped: len=%d suffix=%q", len(got), got[len(got)-3:])
	}
}

// --- RunTask early guards (no sandbox needed) -----------------------------

func TestRunTaskNilClient(t *testing.T) {
	_, err := RunTask(context.Background(), nil, nil, Request{RepoDir: "/tmp/x"})
	if err == nil || !strings.Contains(err.Error(), "nil llm client") {
		t.Fatalf("RunTask(nil client) = %v, want a nil-client error", err)
	}
}

func TestRunTaskEmptyRepoDir(t *testing.T) {
	_, err := RunTask(context.Background(), llm.NewStubClient(), nil, Request{})
	if err == nil || !strings.Contains(err.Error(), "RepoDir is required") {
		t.Fatalf("RunTask(empty RepoDir) = %v, want a RepoDir-required error", err)
	}
}

// TestRunTaskResolveSessionKeyError covers the resolve-session-key error branch:
// a session-active run whose SessionID carries a reserved character fails at key
// resolution — before any worktree or sandbox is created.
func TestRunTaskResolveSessionKeyError(t *testing.T) {
	_, err := RunTask(context.Background(), llm.NewStubClient(), nil, Request{
		RepoDir:      "/tmp/does-not-matter",
		SessionStore: NewInMemorySessions(),
		SessionID:    "bad:session", // reserved ':' -> validate fails
	})
	if err == nil || !strings.Contains(err.Error(), "resolve session key") {
		t.Fatalf("RunTask(bad session id) = %v, want a resolve-session-key error", err)
	}
}

// --- BadgerSessions store error branches ----------------------------------

// TestBadgerSessionsLoadCorruptRecord covers Load's decode/error-wrap branch: a
// stored value that is not valid SessionState JSON must surface a wrapped load
// error, not a silent zero state.
func TestBadgerSessionsLoadCorruptRecord(t *testing.T) {
	store, err := newInMemoryBadgerSessions()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	key := SessionKey{Tenant: "acme", User: "alice", Session: "s1"}
	if err := store.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key.storageKey(), []byte("{not json"))
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Load(context.Background(), key); err == nil || !strings.Contains(err.Error(), "load session") {
		t.Fatalf("Load over a corrupt record = %v, want a wrapped load error", err)
	}
}

// TestBadgerSessionsSaveClosedDB covers Save's db.Update error branch: a write
// to a closed store surfaces a wrapped save error.
func TestBadgerSessionsSaveClosedDB(t *testing.T) {
	store, err := newInMemoryBadgerSessions()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	key := SessionKey{Tenant: "acme", User: "alice", Session: "s1"}
	if err := store.Save(context.Background(), key, SessionState{Prompt: "x"}); err == nil || !strings.Contains(err.Error(), "save session") {
		t.Fatalf("Save on a closed DB = %v, want a wrapped save error", err)
	}
}

// --- RunTask mid-run error branches (fake sandbox provider) ----------------

// errorClient is a keyless host-side client whose StreamChat fails, so the
// structured agentloop returns a NON-malformed error that RunTask must
// propagate (distinct from the graceful keyless-stub no-op completion).
type errorClient struct{}

func (errorClient) Name() string { return "error-client" }
func (errorClient) StreamChat(context.Context, string, string) (<-chan llm.Token, error) {
	return nil, errors.New("provider unreachable")
}

// failingSessionStore fails Load or Save on demand, to drive RunTask's session
// error branches.
type failingSessionStore struct {
	loadErr error
	saveErr error
}

func (f failingSessionStore) Load(context.Context, SessionKey) (SessionState, bool, error) {
	return SessionState{}, false, f.loadErr
}
func (f failingSessionStore) Save(context.Context, SessionKey, SessionState) error {
	return f.saveErr
}

func newGitRepo(t *testing.T) string {
	t.Helper()
	if !gitReady() {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	gitInit(t, repo)
	return repo
}

// TestRunTaskWorktreeAddError covers the worktree-add error branch: a RepoDir
// that is not a git repository makes the worktree Add fail.
func TestRunTaskWorktreeAddError(t *testing.T) {
	if !gitReady() {
		t.Skip("git unavailable")
	}
	notARepo := t.TempDir() // a plain dir, never `git init`ed
	_, err := RunTask(context.Background(), llm.NewStubClient(), nil, Request{
		RepoDir:  notARepo,
		Provider: &fakeProvider{},
		Scopes:   HandScopes(),
	})
	if err == nil || !strings.Contains(err.Error(), "worktree add") {
		t.Fatalf("RunTask over a non-repo = %v, want a worktree-add error", err)
	}
}

// TestRunTaskOnReadyError covers the OnReady hook error branch: a hook that
// returns an error aborts the task after the sandbox is created.
func TestRunTaskOnReadyError(t *testing.T) {
	repo := newGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := RunTask(ctx, llm.NewStubClient(), nil, Request{
		RepoDir:  repo,
		Provider: &fakeProvider{},
		Scopes:   HandScopes(),
		OnReady: func(context.Context, sandbox.Sandbox) error {
			return errors.New("inspection failed")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "OnReady") {
		t.Fatalf("RunTask with a failing OnReady = %v, want an OnReady error", err)
	}
}

// TestRunTaskRunLoopError covers runLoop's non-malformed error propagation on
// the structured-output path: a real StreamChat failure is a task failure, not a
// graceful no-op.
func TestRunTaskRunLoopError(t *testing.T) {
	repo := newGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := RunTask(ctx, errorClient{}, nil, Request{
		RepoDir:  repo,
		Provider: &fakeProvider{},
		Scopes:   HandScopes(),
		MaxSteps: 2,
	})
	if err == nil {
		t.Fatal("RunTask with a failing client = nil, want the loop error propagated")
	}
}

// TestRunTaskResumeLoadError covers the session-load error branch: a Resume run
// whose store Load fails is a hard error (a resume that silently started cold
// would misrepresent the run).
func TestRunTaskResumeLoadError(t *testing.T) {
	repo := newGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := RunTask(ctx, llm.NewStubClient(), nil, Request{
		RepoDir:      repo,
		Provider:     &fakeProvider{},
		Scopes:       HandScopes(),
		SessionID:    "s1",
		Resume:       true,
		SessionStore: failingSessionStore{loadErr: errors.New("store down")},
	})
	if err == nil || !strings.Contains(err.Error(), "load session") {
		t.Fatalf("Resume with a failing Load = %v, want a load-session error", err)
	}
}

// TestRunTaskSessionSaveErrorIsBestEffort covers the persist-failure branch: a
// SessionStore.Save failure after a completed run is logged best-effort and must
// NOT fail the task (the real work already landed).
func TestRunTaskSessionSaveErrorIsBestEffort(t *testing.T) {
	repo := newGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Stub client -> graceful no-op run (0 commits), then Save fails.
	res, err := RunTask(ctx, llm.NewStubClient(), nil, Request{
		RepoDir:      repo,
		Provider:     &fakeProvider{},
		Scopes:       HandScopes(),
		SessionID:    "s1",
		SessionStore: failingSessionStore{saveErr: errors.New("persist failed")},
	})
	if err != nil {
		t.Fatalf("a best-effort session Save failure must not fail the task, got: %v", err)
	}
	if len(res.Commits) != 0 {
		t.Fatalf("keyless stub produced %d commits, want 0", len(res.Commits))
	}
}

// errorToolCaller drives the NATIVE tool-calling path and fails CallTools, so
// llm.RunToolAgent returns an error runLoop must propagate.
type errorToolCaller struct{}

func (errorToolCaller) Name() string { return "error-toolcaller" }
func (errorToolCaller) StreamChat(context.Context, string, string) (<-chan llm.Token, error) {
	ch := make(chan llm.Token)
	close(ch)
	return ch, nil
}
func (errorToolCaller) CallTools(context.Context, string, string, []llm.ToolSpec) (llm.ToolCallResult, error) {
	return llm.ToolCallResult{}, errors.New("tool-calling backend down")
}

// TestRunTaskToolCallerError covers runLoop's native-path error branch: a
// CallTools failure propagates out of RunToolAgent as a task failure.
func TestRunTaskToolCallerError(t *testing.T) {
	repo := newGitRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := RunTask(ctx, errorToolCaller{}, nil, Request{
		RepoDir:  repo,
		Provider: &fakeProvider{},
		Scopes:   HandScopes(),
		MaxSteps: 2,
	})
	if err == nil {
		t.Fatal("RunTask with a failing ToolCaller = nil, want the native-path error propagated")
	}
}

// --- RunTasks defaulting + emit/OnLine chaining ---------------------------

// TestRunTasksDefaultConcurrencyAndNilOnEvent covers RunTasks' conc<=0 default
// and the emit no-op when OnEvent is nil, while a per-task OnLine still chains to
// the caller's original OnLine (the userOnLine != nil branch).
func TestRunTasksDefaultConcurrencyAndNilOnEvent(t *testing.T) {
	if !gitReady() {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	gitInit(t, repo)

	var gotLine string
	reqs := []Request{{
		RepoDir:  repo,
		Prompt:   "task",
		MaxSteps: 1,
		Scopes:   HandScopes(),
		Provider: &fakeProvider{},
		OnLine:   func(line string) { gotLine = line },
	}}
	// A run_shell decision makes the fake streamingSandbox emit one line, which
	// flows through the fan-out's OnLine wrapper to the caller's OnLine above.
	client := &constClient{dec: agentloop.Decision{
		Tool: ToolRunShell,
		Args: map[string]string{"cmd": "echo hi"},
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Concurrency 0 -> defaultConcurrency(); OnEvent nil -> emit is a no-op.
	outcomes, err := RunTasks(ctx, client, nil, reqs, Options{})
	if err != nil {
		t.Fatalf("RunTasks: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Err != nil {
		t.Fatalf("outcome = %+v, want one successful task", outcomes[0])
	}
	if gotLine == "" {
		t.Fatal("caller's original OnLine was not chained through the fan-out wrapper")
	}
}
