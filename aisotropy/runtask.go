package aisotropy

import (
	"context"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/taskagent"
)

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
	BranchStrategy BranchStrategy
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

	out, err := taskagent.RunTask(ctx, client, nil, taskagent.Request{
		RepoDir:        req.RepoDir,
		Prompt:         req.Prompt,
		Image:          req.Image,
		MaxSteps:       req.MaxSteps,
		Scopes:         req.Scopes,
		OnLine:         req.OnLine,
		Provider:       req.Provider,
		BranchStrategy: req.BranchStrategy,
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
