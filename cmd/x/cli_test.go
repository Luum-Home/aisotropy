package main

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestListTasks(t *testing.T) {
	var buf bytes.Buffer
	ListTasks(&buf)

	out := buf.String()
	for _, task := range Tasks() {
		if !strings.Contains(out, task.Key) {
			t.Errorf("ListTasks output missing key %q:\n%s", task.Key, out)
		}
	}
}

func TestRunNonInteractiveUnknownTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	code := RunNonInteractive(ctx, "does-not-exist", &stdout, &stderr)

	if code == 0 {
		t.Fatal("expected nonzero exit code for an unknown task")
	}
	if !strings.Contains(stderr.String(), "unknown task") {
		t.Errorf("stderr = %q, want it to mention \"unknown task\"", stderr.String())
	}
}

// TestRunNonInteractiveStreamsOutputAndReportsFailure drives RunNonInteractive
// over a synthetic task (registered via withTempTasks from exec_test.go) that
// deterministically emits a stdout line, a stderr line, and a nonzero exit —
// exercising the Line-streaming switch cases and the per-step failure summary
// loop, which a gofmt-clean repository's real "fmt" task never triggers (no
// output, exit 0).
func TestRunNonInteractiveStreamsOutputAndReportsFailure(t *testing.T) {
	withTempTasks(t, shTask("rn-fail", "echo out-line; echo err-line 1>&2; exit 1"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	code := RunNonInteractive(ctx, "rn-fail", &stdout, &stderr)

	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(stdout.String(), "out-line") {
		t.Errorf("stdout = %q, want it to contain \"out-line\"", stdout.String())
	}
	if !strings.Contains(stderr.String(), "err-line") {
		t.Errorf("stderr = %q, want it to contain \"err-line\"", stderr.String())
	}
	if !strings.Contains(stderr.String(), "rn-fail failed (exit 1)") {
		t.Errorf("stderr = %q, want the per-step failure summary", stderr.String())
	}
}

// TestRunNonInteractiveReportsUnavailableStep proves the "unavailable" branch
// of the post-run failure summary.
func TestRunNonInteractiveReportsUnavailableStep(t *testing.T) {
	withTempTasks(t, Task{
		Key:            "rn-unavail",
		Title:          "rn-unavail",
		CheckAvailable: func() (bool, string) { return false, "install it first" },
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "true")
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	code := RunNonInteractive(ctx, "rn-unavail", &stdout, &stderr)

	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "rn-unavail unavailable: install it first") {
		t.Errorf("stderr = %q, want the unavailable-step summary", stderr.String())
	}
}

func TestPrintHelp(t *testing.T) {
	var buf bytes.Buffer
	PrintHelp(&buf)

	out := buf.String()
	for _, want := range []string{
		"x — Aisotropy developer console",
		"launch the interactive TUI",
		"Available tasks:",
		"Interactive keys:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("PrintHelp output missing %q:\n%s", want, out)
		}
	}
	for _, task := range Tasks() {
		if !strings.Contains(out, task.Key) {
			t.Errorf("PrintHelp output missing task key %q", task.Key)
		}
	}
}

func TestRunNonInteractiveFmt(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to gofmt; skipped with -short")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	code := RunNonInteractive(ctx, "fmt", &stdout, &stderr)

	// The repository is expected to be gofmt-clean; if it ever isn't, this
	// still exercises the dispatch path correctly (nonzero + files listed on
	// stdout) rather than failing for the wrong reason.
	if code != 0 && stdout.Len() == 0 {
		t.Fatalf("fmt failed with no output to explain why (stderr: %q)", stderr.String())
	}
}
