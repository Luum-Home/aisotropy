//go:build keybroker

package aisotropy

import (
	"context"
	"time"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/taskagent"
)

// DefaultTaskTimeout is the wall-clock deadline applied to a task whose caller
// supplied neither a ctx deadline nor a TaskRequest.MaxDuration. It bounds a
// runaway sandboxed task (an injected loop, a hung command) so it cannot idle the
// host process forever. ctx cancellation already tears down `docker exec`; this
// guarantees the cancellation actually fires without an operator watching.
//
// It re-exports taskagent.DefaultTaskTimeout — the single source of truth. The
// deadline is enforced inside the task runner (taskagent.RunTask) so it bounds
// both a single RunTask AND each task of a RunTasks fan-out identically.
const DefaultTaskTimeout = taskagent.DefaultTaskTimeout

// TaskRequest is the input to App.RunTask: a sandboxed, agentic coding task run
// against a git repository. It is the public, provider-agnostic projection of the
// internal request — it carries WHAT to do and the isolation knobs, and
// deliberately NO provider credential (the LLM call stays host-side; the key never
// enters the sandbox) and NO tenant field (tenant identity travels via ctx).
type TaskRequest struct {
	// RepoDir is the host path of the git repository. RunTask works on a fresh
	// worktree off its HEAD on a new task branch; the operator's checkout is never
	// touched. Required.
	RepoDir string
	// Prompt is the task instruction handed to the host-side model.
	Prompt string
	// Image is the sandbox container image; empty uses a small default sufficient
	// for the built-in hands (run_shell / write_file / read_file / git_commit).
	Image string
	// MaxSteps bounds the agent loop; <= 0 uses the runtime default.
	MaxSteps int
	// MaxDuration is the wall-clock deadline for the whole task. <= 0 applies
	// DefaultTaskTimeout when the caller's ctx has no deadline of its own (a
	// caller-supplied ctx deadline is always respected). It is the time-based
	// companion to MaxSteps: MaxSteps caps agent iterations, MaxDuration caps how
	// long a single command (or the whole run) may take before ctx cancellation
	// tears the sandbox down. Honored by App.RunTask AND per-task in the
	// App.RunTasks batch fan-out — one runaway task in a batch is bounded exactly
	// like a single RunTask, independent of the batch's own ctx. Optional.
	MaxDuration time.Duration
	// Scopes are the granted tool scopes gating which hands the model may use. Pass
	// SandboxToolScopes() for the full set, or a subset to restrict it. An empty
	// slice offers the model no tools (it can only answer directly).
	Scopes []string
	// OnLine, when non-nil, receives each line of a run_shell's stdout live — the
	// hook to fan tool output into an SSE stream. Optional.
	OnLine func(line string)
	// Provider is the sandbox backend. nil defaults to DockerSandbox(); pass
	// PodmanSandbox() for rootless Podman, or your own SandboxProvider (e.g. a
	// Kubernetes or Firecracker backend). The API key never enters it.
	Provider SandboxProvider
	// BranchStrategy decides what happens to the commits the task produced: the
	// zero value MergeToHead merges the task branch into the repo's HEAD;
	// ReturnBranch leaves them on the task branch for you to review/merge.
	//
	// SECURITY (supply chain): the zero value auto-merges an agent's commits into
	// your HEAD with no human gate. When the Prompt is UNTRUSTED
	// (attacker-influenced, from an end user, an issue tracker, etc.) that is a
	// supply-chain risk — the model can be steered into committing hostile code
	// that then lands in HEAD automatically. For untrusted prompts set
	// ReturnBranch (review-before-merge) and merge only after inspecting the
	// branch; reserve the MergeToHead default for trusted/reviewed task sources.
	// See docs/architecture/task-branch-strategy-trust.md.
	BranchStrategy BranchStrategy

	// SessionID, when non-empty AND paired with a non-nil SessionStore, enrolls
	// this run in a resumable, tenant-scoped task session: its step trace + prompt
	// are persisted under (tenant, user, SessionID) so a later run with Resume set
	// can rehydrate the agent's context. Tenant/user come from ctx (inject them
	// with the same identity you use for chat), never from a field. Empty (or a
	// nil store) disables session persistence. Optional.
	SessionID string
	// Resume, when true, rehydrates this SessionID's prior step trace + prompt and
	// feeds it to the agent instead of starting cold. It requires a non-empty
	// SessionID and a SessionStore.
	//
	// HONEST semantics: a resumed task gets a FRESH sandbox + worktree — a
	// container is ephemeral and cannot be restored — and works off the UPDATED
	// repo, since the prior run's commits are already merged into HEAD. What is
	// restored is the AGENT'S CONTEXT (its step history / "mind"), NOT the
	// filesystem. The key-broker invariant is untouched: session state is
	// host-side; no provider key ever entered a container.
	Resume bool
	// SessionStore is the persistence backend a SessionID run writes to and a
	// Resume run reads from. Build one with NewInMemoryTaskSessions (process
	// lifetime) or OpenTaskSessions (persistent, survives restart), or supply your
	// own. Reuse ONE store across the runs that should share sessions — a store
	// built fresh per run remembers nothing. nil disables persistence/resume.
	SessionStore TaskSessionStore
}

// TaskSessionStore persists and loads a task's step trace + prompt per
// (tenant, user, session) so a task can be resumed with memory of its prior run.
// Every read/write is scoped to the full identity, so one tenant's session can
// never observe another's. Build one with NewInMemoryTaskSessions or
// OpenTaskSessions, or implement it yourself.
type TaskSessionStore = taskagent.SessionStore

// TaskSessionKey identifies one resumable session: tenant + user (which travel
// via ctx at run time, never as a request field) plus the SessionID. It is the
// key a caller uses to inspect a store directly.
type TaskSessionKey = taskagent.SessionKey

// TaskSessionState is the persisted "mind" of a session: the full, verbatim step
// trace accumulated across runs plus the most recent task prompt. It is what a
// Resume run rehydrates — the agent's context/history, deliberately NOT the
// filesystem (the files already reflect the prior run's merged commits).
type TaskSessionState = taskagent.SessionState

// NewInMemoryTaskSessions returns a process-lifetime, tenant-scoped task session
// store. It remembers sessions across RunTask calls for as long as the returned
// value is kept alive (e.g. a long-running server reusing one store across many
// task requests), but NOT across process restarts — use OpenTaskSessions for
// durable state. Keyless and self-hostable; no external service.
func NewInMemoryTaskSessions() TaskSessionStore { return taskagent.NewInMemorySessions() }

// OpenTaskSessions opens (creating if needed) a persistent, tenant-scoped task
// session store rooted on disk at dir, backed by the same embedded BadgerDB
// engine aisotropy's working-memory tier uses. Sessions survive process
// restarts. The caller owns the returned store's lifecycle and must Close it.
func OpenTaskSessions(dir string) (*taskagent.BadgerSessions, error) {
	return taskagent.OpenBadgerSessions(dir)
}

// TaskResult is the outcome of App.RunTask.
type TaskResult struct {
	// Commits are the SHAs the run produced on the task branch, newest first (empty
	// when the model committed nothing). When non-empty the branch was merged into
	// RepoDir's HEAD; when empty the branch was discarded.
	Commits []string
	// Branch is the task branch the run's commits live on ("" when nothing was
	// committed).
	Branch string
	// Steps is the ordered trace of tool decisions and results.
	Steps []TaskStep
	// Output is a best-effort final output (the last non-empty tool result); Steps
	// carry the full trace.
	Output string
}

// TaskStep is one iteration of the loop: the tool the model chose and its result.
// Tool == "" means the model answered directly with no tool.
type TaskStep struct {
	Tool   string
	Args   map[string]string
	Result string
}

// SandboxToolScopes returns the scopes for the full built-in hands-toolset
// (run_shell, write_file, read_file, git_commit), for a caller that wants the
// model to have every sandbox tool. Pass a subset to TaskRequest.Scopes to
// restrict which hands the model may use.
func SandboxToolScopes() []string { return taskagent.HandScopes() }

// RunTask runs a sandboxed agentic coding task: it opens an isolated git worktree,
// stands up a sandbox container over it, drives the HOST-SIDE agent loop (the same
// llm.Client this App uses for chat) over the sandboxed hands, and — if the run
// produced commits — merges the task branch back into RepoDir's HEAD.
//
// The broker invariant holds by construction: the model call happens host-side
// (spending the provider key here in the host process); only the tool EXECUTIONS
// the model chooses run inside the container, which never receives the key.
//
// The App's configured client (WithLLM) drives inference; with no client set it
// falls back to llm.FromEnv() — the real provider when ANTHROPIC_API_KEY is set,
// otherwise the keyless stub (which still drives the loop to a graceful
// completion). Tenant identity, when injected into ctx by the caller, is inherited
// downstream via ctx; RunTask never reads a tenant from the request.
//
// It is additive: RunTask boots no HTTP runtime and does not touch the App's
// Run/Turn state — it only reads the App's llm client.
func (a *App) RunTask(ctx context.Context, req TaskRequest) (TaskResult, error) {
	client := a.llmClient
	if client == nil {
		client = llm.FromEnv()
	}

	// The per-task wall-clock deadline (req.MaxDuration, or DefaultTaskTimeout when
	// the ctx has no deadline of its own) is enforced inside taskagent.RunTask —
	// the single task-runner entry point — so a single RunTask and each task of a
	// RunTasks fan-out are bounded identically. It is threaded through as a field.
	out, err := taskagent.RunTask(ctx, client, nil, taskagent.Request{
		RepoDir:        req.RepoDir,
		Prompt:         req.Prompt,
		Image:          req.Image,
		MaxSteps:       req.MaxSteps,
		MaxDuration:    req.MaxDuration,
		Scopes:         req.Scopes,
		OnLine:         req.OnLine,
		Provider:       req.Provider,
		BranchStrategy: req.BranchStrategy,
		SessionID:      req.SessionID,
		Resume:         req.Resume,
		SessionStore:   req.SessionStore,
	})
	if err != nil {
		return TaskResult{}, err
	}

	steps := make([]TaskStep, 0, len(out.Steps))
	for _, s := range out.Steps {
		steps = append(steps, TaskStep{Tool: s.Tool, Args: s.Args, Result: s.Result})
	}
	return TaskResult{
		Commits: out.Commits,
		Branch:  out.Branch,
		Steps:   steps,
		Output:  out.Output,
	}, nil
}
