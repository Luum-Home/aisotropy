//go:build keybroker

package aisotropy

import (
	"context"

	"github.com/Luum-Home/aisotropy/internal/llm"
	"github.com/Luum-Home/aisotropy/internal/taskagent"
)

// TaskOutcome is the per-task result of App.RunTasks. Err carries an INDIVIDUAL
// task's failure without aborting the batch — the other tasks still run and still
// return their own TaskOutcomes. A task whose slot was never scheduled because the
// context was cancelled mid-batch carries the context error in Err.
type TaskOutcome struct {
	// Index is the position of this task's request in the input slice; outcomes are
	// returned in input order, so Outcomes[i].Index == i.
	Index int
	// Request is the original TaskRequest this outcome corresponds to.
	Request TaskRequest
	// Result is the task's result (zero-valued when Err is set).
	Result TaskResult
	// Err is this task's individual failure, or nil on success. A non-nil Err here
	// does NOT fail the batch — see App.RunTasks's return contract.
	Err error
}

// TaskEvent is one progress signal from a RunTasks fan-out, tagged with the Index
// of the task it belongs to so a consumer can multiplex N concurrent task streams
// into one feed (e.g. an SSE channel). Kind is one of:
//
//	"started" — the task is about to begin (live, before any work)
//	"step"    — the model ran a tool (Tool set; Message is a short result preview) — live
//	"shell"   — one stdout line from a run_shell (Line set) — live, as produced
//	"commit"  — the task produced a commit (Message is the SHA) — at completion
//	"done"    — the task finished successfully (terminal; Message is a summary)
//	"error"   — the task failed (terminal; Message is the error text)
type TaskEvent struct {
	Index   int
	Kind    string
	Tool    string
	Line    string
	Message string
}

// RunTasksOptions tunes App.RunTasks.
type RunTasksOptions struct {
	// Concurrency bounds how many tasks run at once; <= 0 selects a sane default
	// (min(NumCPU, a small cap), at least 1).
	Concurrency int
	// OnEvent, when non-nil, receives a live, index-tagged TaskEvent stream
	// multiplexing all tasks. RunTasks serializes emission internally, so although
	// the tasks run concurrently OnEvent is invoked one event at a time and need
	// NOT be goroutine-safe. Keep it cheap and non-blocking — it runs on a worker
	// goroutine; do any SSE/fan-out work asynchronously.
	OnEvent func(TaskEvent)

	// SessionStore is a batch-wide default persistence backend applied to every
	// task that does not set its own TaskRequest.SessionStore — the ergonomic way
	// to give a whole fan-out one shared, tenant-scoped session store. A task with
	// its own store keeps it; a task with an empty SessionID persists nothing
	// regardless. nil (the default) leaves per-task stores untouched. See
	// TaskSessionStore / NewInMemoryTaskSessions / OpenTaskSessions.
	SessionStore TaskSessionStore
}

// RunTasks runs many sandboxed agentic coding tasks CONCURRENTLY — a fan-out over
// RunTask — bounded by opts.Concurrency, returning one TaskOutcome per request in
// input order.
//
// It builds directly ON RunTask, so every guarantee of a single task holds per
// task under the fan-out: each task gets its OWN isolated git worktree, its OWN
// sandbox container, and the KEY-BROKER invariant (the provider key is spent
// host-side and NEVER enters any container). Concurrency changes none of that —
// the tasks are isolated by construction because each RunTask already is.
//
// Return contract:
//   - PER-TASK isolation: a task that fails sets Err on its own TaskOutcome; the
//     batch CONTINUES and still returns every other outcome. An individual task
//     failure is NEVER the top-level error.
//   - The TOP-LEVEL error is non-nil only for a whole-batch failure — the passed
//     ctx being cancelled, which also stops scheduling not-yet-started tasks and
//     records the context error on their outcomes.
//
// The merge race is handled: each task's RunTask merges its branch into the shared
// RepoDir HEAD, so RunTasks serializes the per-repo git-metadata steps (worktree
// add and merge-to-HEAD) with a per-repo mutex — tasks run concurrently, merges
// apply one at a time, and a failed merge is aborted so a conflict never corrupts
// HEAD for the next task. Auto-merge semantics are thus preserved under
// concurrency (see internal/taskagent).
//
// As with RunTask, the App's configured client (WithLLM) drives inference, falling
// back to llm.FromEnv() when unset; tenant identity injected into ctx is inherited
// downstream. RunTasks boots no HTTP runtime and does not touch the App's
// Run/Turn state.
func (a *App) RunTasks(ctx context.Context, reqs []TaskRequest, opts RunTasksOptions) ([]TaskOutcome, error) {
	client := a.llmClient
	if client == nil {
		client = llm.FromEnv()
	}

	ireqs := make([]taskagent.Request, len(reqs))
	for i, r := range reqs {
		// A task's own store wins; otherwise the batch-wide default (if any) is
		// applied, so a whole fan-out can share one session store without setting
		// it on every request.
		store := r.SessionStore
		if store == nil {
			store = opts.SessionStore
		}
		ireqs[i] = taskagent.Request{
			RepoDir:        r.RepoDir,
			Prompt:         r.Prompt,
			Image:          r.Image,
			MaxSteps:       r.MaxSteps,
			MaxDuration:    r.MaxDuration,
			Scopes:         r.Scopes,
			OnLine:         r.OnLine,
			Provider:       r.Provider,
			BranchStrategy: r.BranchStrategy,
			SessionID:      r.SessionID,
			Resume:         r.Resume,
			SessionStore:   store,
		}
	}

	var onEvent func(taskagent.Event)
	if opts.OnEvent != nil {
		onEvent = func(ev taskagent.Event) {
			opts.OnEvent(TaskEvent{
				Index:   ev.Index,
				Kind:    ev.Kind,
				Tool:    ev.Tool,
				Line:    ev.Line,
				Message: ev.Message,
			})
		}
	}

	iout, err := taskagent.RunTasks(ctx, client, nil, ireqs, taskagent.Options{
		Concurrency: opts.Concurrency,
		OnEvent:     onEvent,
	})

	outcomes := make([]TaskOutcome, len(iout))
	for i, o := range iout {
		outcomes[i] = TaskOutcome{
			Index:   o.Index,
			Request: reqs[o.Index],
			Result:  toTaskResult(o.Result),
			Err:     o.Err,
		}
	}
	return outcomes, err
}

// toTaskResult maps the internal Result to the public TaskResult (the same
// projection App.RunTask performs inline).
func toTaskResult(out taskagent.Result) TaskResult {
	steps := make([]TaskStep, 0, len(out.Steps))
	for _, s := range out.Steps {
		steps = append(steps, TaskStep{Tool: s.Tool, Args: s.Args, Result: s.Result})
	}
	return TaskResult{
		Commits: out.Commits,
		Branch:  out.Branch,
		Steps:   steps,
		Output:  out.Output,
	}
}
