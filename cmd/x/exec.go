package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/Luum-Home/aisotropy/internal/procgroup"
)

// streamStdout / streamStderr name the two output streams a task's process
// writes to (Line.Stream).
const (
	streamStdout = "stdout"
	streamStderr = "stderr"
)

// Line is one line of output produced while running a task, tagged with the
// task-registry key that emitted it (for composite tasks this is the sub-task
// key, not the top-level one) and which stream it came from.
type Line struct {
	Step   string
	Stream string // streamStdout or streamStderr
	Text   string
}

// StepResult is the outcome of running a single leaf task.
type StepResult struct {
	Key         string
	Title       string
	ExitCode    int
	Pass        bool
	Unavailable bool
	Hint        string
}

// Result is the aggregate outcome of a task run: one StepResult per leaf task
// executed (a single entry for a leaf task, several for a composite one).
type Result struct {
	Steps []StepResult
	Pass  bool
}

// Event is one message produced by a Run: either a streamed output Line or,
// exactly once as the final event, the aggregate Result.
type Event struct {
	Line   *Line
	Result *Result
}

// Run is a task execution in progress. Events is closed by the producer
// goroutine once the final Result event has been sent, so callers can safely
// range over it.
type Run struct {
	Events chan Event
}

// StartTask launches t (or, for composite tasks, each of its Steps in order)
// in a background goroutine and streams output plus the final result back
// over Run.Events. The goroutine that reads a task's stdout/stderr pipes is
// the only writer of that task's captured-stdout buffer, and all UI-facing
// data crosses goroutine boundaries exclusively through the Events channel,
// so callers never need their own locking to consume a Run safely.
func StartTask(ctx context.Context, t Task) *Run {
	run := &Run{Events: make(chan Event, 64)}
	go func() {
		defer close(run.Events)

		keys := t.Steps
		if len(keys) == 0 {
			keys = []string{t.Key}
		}
		// Flatten nested composites to leaf tasks: a step that is itself a
		// composite (has Steps and no NewCmd, e.g. `check` -> `example`) would
		// otherwise reach runStep, which calls NewCmd unconditionally and panics
		// on the nil. Expanding here keeps runStep a pure leaf runner.
		keys = flattenSteps(keys)

		steps := make([]StepResult, 0, len(keys))
		overallPass := true
		for _, key := range keys {
			sub, ok := Lookup(key)
			if !ok {
				run.Events <- Event{Line: &Line{Step: key, Stream: streamStderr, Text: fmt.Sprintf("unknown task %q", key)}}
				steps = append(steps, StepResult{Key: key, Pass: false})
				overallPass = false
				continue
			}

			res := runStep(ctx, sub, run.Events)
			steps = append(steps, res)
			if !res.Pass {
				overallPass = false
			}
			if ctx.Err() != nil {
				// Cancelled mid-sequence (e.g. quitting the TUI): stop
				// launching further steps instead of running a stale chain.
				break
			}
		}

		run.Events <- Event{Result: &Result{Steps: steps, Pass: overallPass}}
	}()
	return run
}

// flattenSteps recursively expands any composite step (a task with Steps and no
// NewCmd) into its leaf tasks, so StartTask only ever hands runStep a runnable
// leaf. A missing or leaf task passes through unchanged (StartTask reports the
// missing one). Guards against a self-referential cycle by not re-expanding a
// task into itself.
func flattenSteps(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		sub, ok := Lookup(k)
		if ok && sub.NewCmd == nil && len(sub.Steps) > 0 && (len(sub.Steps) != 1 || sub.Steps[0] != k) {
			out = append(out, flattenSteps(sub.Steps)...)
			continue
		}
		out = append(out, k)
	}
	return out
}

// runStep executes a single leaf task and reports its StepResult, emitting
// every output line it produces onto events as it happens.
func runStep(ctx context.Context, t Task, events chan<- Event) StepResult {
	if t.CheckAvailable != nil {
		if ok, hint := t.CheckAvailable(); !ok {
			events <- Event{Line: &Line{Step: t.Key, Stream: streamStderr, Text: fmt.Sprintf("%s unavailable: %s", t.Title, hint)}}
			return StepResult{Key: t.Key, Title: t.Title, Unavailable: true, Hint: hint}
		}
	}

	cmd := t.NewCmd(ctx)
	exitCode, stdout, err := streamCmd(ctx, cmd, func(stream, text string) {
		events <- Event{Line: &Line{Step: t.Key, Stream: stream, Text: text}}
	})
	if err != nil {
		events <- Event{Line: &Line{Step: t.Key, Stream: streamStderr, Text: fmt.Sprintf("failed to run: %v", err)}}
		return StepResult{Key: t.Key, Title: t.Title, ExitCode: -1, Pass: false}
	}

	pass := exitCode == 0
	if t.EvalOutput != nil {
		pass = t.EvalOutput(exitCode, stdout)
	}
	return StepResult{Key: t.Key, Title: t.Title, ExitCode: exitCode, Pass: pass}
}

// streamCmd runs cmd to completion, invoking onLine for every line written to
// stdout or stderr as it arrives, and returns the process exit code plus the
// captured stdout text (used by tasks like fmt whose pass/fail depends on
// stdout content, not just the exit code).
//
// stdout and stderr are scanned by two independent goroutines. Each writes
// only to its own local buffer (no shared mutable state), and both send onto
// onLine, which for every caller in this package is a channel send — safe for
// concurrent use without extra locking. The two goroutines' output may
// interleave, which is expected for a live combined stream.
//
// The command runs as the leader of its own process group, and cancelling ctx
// kills the whole group (see internal/procgroup). Killing only the direct child
// left a forked grandchild holding the stdout/stderr pipes: the scanners below
// then blocked until it exited by itself, and cancelling `x run` (`go run`,
// whose child is the server) left the server running.
func streamCmd(ctx context.Context, cmd *exec.Cmd, onLine func(stream, text string)) (exitCode int, stdout string, err error) {
	procgroup.Isolate(cmd)
	// Only exec.CommandContext sets Cancel; setting one on a plain exec.Command
	// would make Start fail, so a ctx-less command keeps its default.
	if cmd.Cancel != nil {
		cmd.Cancel = func() error { return procgroup.Kill(cmd) }
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return -1, "", err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return -1, "", err
	}

	if err := cmd.Start(); err != nil {
		return -1, "", err
	}

	var stdoutBuf strings.Builder
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		scanLines(stdoutPipe, func(line string) {
			stdoutBuf.WriteString(line)
			stdoutBuf.WriteByte('\n')
			onLine(streamStdout, line)
		})
	}()
	go func() {
		defer wg.Done()
		scanLines(stderrPipe, func(line string) {
			onLine(streamStderr, line)
		})
	}()
	wg.Wait()

	waitErr := cmd.Wait()
	if waitErr == nil {
		return 0, stdoutBuf.String(), nil
	}

	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return exitErr.ExitCode(), stdoutBuf.String(), nil
	}
	if ctx.Err() != nil {
		// Killed via context cancellation (e.g. the TUI stopping a
		// long-running task such as "run"): not a real failure to report.
		return -1, stdoutBuf.String(), nil //nolint:nilerr // ctx cancellation is a deliberate stop, not a command failure to surface as an error
	}
	return -1, stdoutBuf.String(), waitErr
}

// scanLines reads r line by line, calling emit for each line. Buffer size is
// raised above bufio's 64KiB default because `go vet`/`golangci-lint` output
// can include long single lines (e.g. generated code diagnostics).
func scanLines(r io.Reader, emit func(line string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		emit(sc.Text())
	}
}
