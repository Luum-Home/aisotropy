package taskagent

import (
	"context"
	"runtime"
	"strconv"
	"sync"

	"golang.org/x/sync/errgroup"

	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// maxDefaultConcurrency caps the auto-chosen worker count when Options.Concurrency
// is unset. Each concurrent task holds a live container + a host-side LLM stream,
// so an unbounded fan-out over, say, a 64-core host would swamp both the Docker
// daemon and the provider's rate limit. NumCPU, clamped here, is a sane default.
const maxDefaultConcurrency = 8

// Event is one progress signal emitted while a fan-out runs, tagged with the Index
// of the task it belongs to so a consumer can multiplex N concurrent task streams
// into one feed. Kind is one of:
//
//	started — the task's RunTask is about to begin (emitted live, before any work)
//	step    — the model ran a hand (Tool set; Message is a short result preview) — live
//	shell   — one stdout line from a run_shell (Line set) — live, as produced
//	commit  — the task produced a commit (Message is the SHA) — emitted at completion,
//	          since a commit's SHA and its merge to HEAD are only settled once the
//	          run finishes
//	done    — the task finished successfully (terminal; Message is a short summary)
//	error   — the task failed (terminal; Message is the error text)
//
// started, step, and shell stream live during execution; commit is drawn from the
// finished Result (SHAs are assigned at merge time); done/error are terminal and
// always last for a given Index.
type Event struct {
	Index   int
	Kind    string
	Tool    string
	Line    string
	Message string
}

// Event kinds. Exported so a consumer can switch on them without stringly-typed
// literals drifting out of sync with the emitter.
const (
	EventStarted = "started"
	EventStep    = "step"
	EventShell   = "shell"
	EventCommit  = "commit"
	EventDone    = "done"
	EventError   = "error"
)

// Options tunes a RunTasks fan-out.
type Options struct {
	// Concurrency bounds how many tasks run at once; <= 0 selects a default
	// (min(NumCPU, maxDefaultConcurrency), at least 1).
	Concurrency int
	// OnEvent, when non-nil, receives each Event. RunTasks SERIALIZES every call
	// through an internal mutex, so although tasks run concurrently the callback is
	// invoked one Event at a time and need NOT itself be goroutine-safe. It must be
	// cheap and non-blocking — it runs on a worker goroutine, so blocking here
	// stalls a task. Do fan-out/SSE work asynchronously.
	OnEvent func(Event)
}

// Outcome is the per-task result of a fan-out. Err carries an individual task's
// failure WITHOUT aborting the batch: the other tasks still run and still return
// their own Outcomes. A task whose slot was never scheduled because the parent ctx
// was cancelled mid-batch carries ctx's error in Err.
type Outcome struct {
	Index   int
	Request Request
	Result  Result
	Err     error
}

// RunTasks runs many RunTasks CONCURRENTLY, one goroutine per request, bounded by
// opts.Concurrency, and returns one Outcome per request IN INDEX ORDER.
//
// # Isolation (by construction)
//
// Each task goes through the SAME RunTask, so each already gets its own worktree,
// its own container, and the key-broker invariant (no provider key in any
// container) per task. Parallel tasks therefore inherit full isolation for free —
// this primitive only schedules and serializes the few shared-repo git operations.
//
// # Per-task error isolation
//
// A task that fails yields an Outcome with Err set; the batch CONTINUES and still
// returns the other Outcomes. The TOP-LEVEL error is non-nil only for a
// whole-batch failure — specifically, the parent ctx being cancelled (which also
// stops scheduling any not-yet-started tasks and marks their Outcomes with ctx's
// error). An ordinary task failure never becomes the top-level error.
//
// # The merge race
//
// Every task's RunTask merges its branch into the SAME RepoDir HEAD; two `git
// merge`s at once would corrupt HEAD. RunTasks hands each task a per-repo mutex
// (repoGate) keyed by RepoDir: tasks run concurrently, but the worktree-add and
// merge-to-HEAD steps for one repo are serialized one-at-a-time, and a failed
// gated merge is aborted so a conflict never leaves HEAD half-merged for the next
// task (see runtask.go's gatedMergeToHead). This preserves RunTask's auto-merge
// semantics under concurrency instead of dropping to branch-only output.
//
// # Streaming
//
// opts.OnEvent (when set) receives a live, index-tagged Event stream multiplexing
// all tasks; emission is serialized internally, so the callback need not be
// goroutine-safe (see Options.OnEvent).
func RunTasks(ctx context.Context, client llm.Client, tracer oteltrace.TracerProvider, reqs []Request, opts Options) ([]Outcome, error) {
	outcomes := make([]Outcome, len(reqs))
	for i := range reqs {
		outcomes[i] = Outcome{Index: i, Request: reqs[i]}
	}
	if len(reqs) == 0 {
		return outcomes, nil
	}

	conc := opts.Concurrency
	if conc <= 0 {
		conc = defaultConcurrency()
	}

	// Serialize event emission so a caller's OnEvent sees one Event at a time even
	// though tasks run concurrently.
	var emitMu sync.Mutex
	emit := func(ev Event) {
		if opts.OnEvent == nil {
			return
		}
		emitMu.Lock()
		defer emitMu.Unlock()
		opts.OnEvent(ev)
	}

	gates := &repoGates{}
	launched := make([]bool, len(reqs))

	// errgroup.WithContext gives a ctx cancelled when the parent is (our task
	// goroutines never return an error, so the group is used purely as a
	// slot-limited WaitGroup — a task failure is captured in its Outcome, not
	// propagated as a group error). SetLimit bounds concurrency; Go blocks until a
	// slot frees.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(conc)

	for i := range reqs {
		if gctx.Err() != nil {
			// Parent ctx cancelled — stop scheduling further tasks.
			break
		}
		i := i
		launched[i] = true
		g.Go(func() error {
			r := reqs[i]
			r.repoGate = gates.get(r.RepoDir)
			r.onStep = func(tool, result string) {
				emit(Event{Index: i, Kind: EventStep, Tool: tool, Message: preview(result)})
			}
			// Tag each shell line with the task Index, then chain to the caller's
			// original OnLine (if any) so per-task streaming still works.
			userOnLine := reqs[i].OnLine
			r.OnLine = func(line string) {
				emit(Event{Index: i, Kind: EventShell, Line: line})
				if userOnLine != nil {
					userOnLine(line)
				}
			}

			emit(Event{Index: i, Kind: EventStarted})
			res, err := RunTask(gctx, client, tracer, r)
			outcomes[i].Result = res
			outcomes[i].Err = err
			if err != nil {
				emit(Event{Index: i, Kind: EventError, Message: err.Error()})
				return nil
			}
			for _, sha := range res.Commits {
				emit(Event{Index: i, Kind: EventCommit, Message: sha})
			}
			emit(Event{Index: i, Kind: EventDone, Message: doneSummary(res)})
			return nil
		})
	}
	_ = g.Wait()

	// A task whose slot was never scheduled (parent ctx cancelled mid-fan-out) has
	// no Result and no Err yet — attribute the cancellation to it.
	if cerr := ctx.Err(); cerr != nil {
		for i := range outcomes {
			if !launched[i] {
				outcomes[i].Err = cerr
			}
		}
		return outcomes, cerr
	}
	return outcomes, nil
}

// defaultConcurrency is min(NumCPU, maxDefaultConcurrency), at least 1.
func defaultConcurrency() int {
	n := runtime.NumCPU()
	if n > maxDefaultConcurrency {
		n = maxDefaultConcurrency
	}
	if n < 1 {
		n = 1
	}
	return n
}

// repoGates hands out one mutex per distinct repo path, so the shared-repo git
// operations of concurrent tasks on the SAME repo are serialized while tasks on
// DIFFERENT repos stay fully parallel. Its own map is guarded by mu.
type repoGates struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

// get returns the mutex for repoDir, creating it on first use.
func (g *repoGates) get(repoDir string) sync.Locker {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m == nil {
		g.m = make(map[string]*sync.Mutex)
	}
	lk := g.m[repoDir]
	if lk == nil {
		lk = &sync.Mutex{}
		g.m[repoDir] = lk
	}
	return lk
}

// doneSummary renders a compact terminal summary of a finished Result for a done
// Event: how many commits landed and how many steps ran.
func doneSummary(res Result) string {
	c := len(res.Commits)
	noun := "commits"
	if c == 1 {
		noun = "commit"
	}
	return strconv.Itoa(c) + " " + noun + ", " + strconv.Itoa(len(res.Steps)) + " steps"
}

// preview trims a tool result to a short single-line snippet for a step Event so
// the multiplexed stream stays readable (the full result lives in Outcome.Result).
func preview(s string) string {
	const max = 160
	// Collapse to the first line.
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			s = s[:i]
			break
		}
	}
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
