package main

import (
	"context"
	"fmt"
	"io"
)

// RunNonInteractive executes the named task to completion outside the TUI,
// streaming its output line by line to stdout/stderr as it happens and
// returning the process exit code the caller should use (0 on success).
// It never starts Bubbletea, so it is safe to call from CI or scripts.
func RunNonInteractive(ctx context.Context, key string, stdout, stderr io.Writer) int {
	t, ok := Lookup(key)
	if !ok {
		fmt.Fprintf(stderr, "x: unknown task %q\n", key)
		fmt.Fprintln(stderr, "Run `x list` to see available tasks.")
		return 1
	}

	run := StartTask(ctx, t)
	var result Result
	for ev := range run.Events {
		switch {
		case ev.Line != nil:
			w := stdout
			if ev.Line.Stream == streamStderr {
				w = stderr
			}
			fmt.Fprintln(w, ev.Line.Text)
		case ev.Result != nil:
			result = *ev.Result
		}
	}

	if result.Pass {
		return 0
	}
	for _, s := range result.Steps {
		switch {
		case s.Unavailable:
			fmt.Fprintf(stderr, "x: %s unavailable: %s\n", s.Key, s.Hint)
		case !s.Pass:
			fmt.Fprintf(stderr, "x: %s failed (exit %d)\n", s.Key, s.ExitCode)
		}
	}
	return 1
}

// ListTasks prints every registered task's key and title to w, used by
// `x list`.
func ListTasks(w io.Writer) {
	fmt.Fprintln(w, "Available tasks:")
	for _, t := range Tasks() {
		fmt.Fprintf(w, "  %-10s %s\n", t.Key, t.Description)
	}
}

// PrintHelp prints top-level usage for the x developer console.
func PrintHelp(w io.Writer) {
	fmt.Fprintln(w, "x — Aisotropy developer console")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  x                 launch the interactive TUI")
	fmt.Fprintln(w, "  x <task>          run one task non-interactively and exit with its status")
	fmt.Fprintln(w, "  x list            list available task keys")
	fmt.Fprintln(w, "  x --help          show this message")
	fmt.Fprintln(w, "")
	ListTasks(w)
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Interactive keys: up/down or j/k navigate, enter run/stop selected, a run check, r re-run last, q/ctrl+c quit.")
	fmt.Fprintln(w, "Run from the repository root: paths like ./cmd/aisotropyd are relative to it.")
}
