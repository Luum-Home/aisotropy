package main

import (
	"bytes"
	"context"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// withTempTasks appends extra tasks to the global registry for the duration
// of the test, restoring the original registry on cleanup. exec.go's
// StartTask/flattenSteps/runStep resolve steps exclusively through Lookup
// (which reads the package-level registry), so exercising composite/nested
// dispatch needs real, fast, deterministic synthetic entries rather than the
// real (slow, network- or toolchain-dependent) registry tasks.
func withTempTasks(t *testing.T, tasks ...Task) {
	t.Helper()
	orig := registry
	next := make([]Task, len(orig), len(orig)+len(tasks))
	copy(next, orig)
	registry = append(next, tasks...)
	t.Cleanup(func() { registry = orig })
}

// shTask builds a synthetic leaf Task whose command is a cheap `sh -c`
// script, so tests never depend on gofmt/go build/network tools.
func shTask(key, script string) Task {
	return Task{
		Key:   key,
		Title: key,
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", script)
		},
	}
}

func drain(events <-chan Event) {
	for range events {
	}
}

// --- scanLines ---

func TestScanLinesEmitsEachLine(t *testing.T) {
	var got []string
	scanLines(strings.NewReader("line1\nline2\nline3"), func(line string) {
		got = append(got, line)
	})
	want := []string{"line1", "line2", "line3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scanLines lines = %v, want %v", got, want)
	}
}

func TestScanLinesEmptyReaderEmitsNothing(t *testing.T) {
	var got []string
	scanLines(strings.NewReader(""), func(line string) { got = append(got, line) })
	if len(got) != 0 {
		t.Fatalf("scanLines on empty input emitted %v, want none", got)
	}
}

// --- streamCmd ---

func TestStreamCmdCapturesOutputAndExitCode(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "echo out1; echo err1 1>&2; exit 3")

	// streamCmd documents that stdout/stderr are scanned by two independent
	// goroutines which may both call onLine concurrently ("expected for a live
	// combined stream") — the callback itself must be the one that
	// synchronizes, exactly as any real caller (model.go's Update via
	// waitForEvent's channel send) would need to.
	var mu sync.Mutex
	var lines []string
	exitCode, stdout, err := streamCmd(context.Background(), cmd, func(stream, text string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, stream+":"+text)
	})
	if err != nil {
		t.Fatalf("streamCmd error: %v", err)
	}
	if exitCode != 3 {
		t.Fatalf("exitCode = %d, want 3", exitCode)
	}
	if stdout != "out1\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "out1\n")
	}
	wantLines := map[string]bool{"stdout:out1": true, "stderr:err1": true}
	for _, l := range lines {
		delete(wantLines, l)
	}
	if len(wantLines) != 0 {
		t.Fatalf("missing expected lines %v in %v", wantLines, lines)
	}
}

func TestStreamCmdSuccessReturnsZero(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "exit 0")
	exitCode, _, err := streamCmd(context.Background(), cmd, func(string, string) {})
	if err != nil {
		t.Fatalf("streamCmd error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
}

func TestStreamCmdStartFailureReturnsError(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "definitely-not-a-real-binary-xyz")
	exitCode, stdout, err := streamCmd(context.Background(), cmd, func(string, string) {})
	if err == nil {
		t.Fatal("expected an error for a nonexistent binary")
	}
	if exitCode != -1 {
		t.Fatalf("exitCode = %d, want -1", exitCode)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
}

// TestStreamCmdStdoutPipeErrorReturnsError forces cmd.StdoutPipe() to fail
// (by pre-setting cmd.Stdout, which os/exec documents as mutually exclusive
// with StdoutPipe) so streamCmd's own error-return branch after StdoutPipe is
// exercised.
func TestStreamCmdStdoutPipeErrorReturnsError(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "echo hi")
	cmd.Stdout = &bytes.Buffer{}

	exitCode, stdout, err := streamCmd(context.Background(), cmd, func(string, string) {})
	if err == nil {
		t.Fatal("expected an error when cmd.Stdout is already set")
	}
	if exitCode != -1 {
		t.Fatalf("exitCode = %d, want -1", exitCode)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
}

// TestStreamCmdStderrPipeErrorReturnsError is the StderrPipe equivalent.
func TestStreamCmdStderrPipeErrorReturnsError(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "echo hi")
	cmd.Stderr = &bytes.Buffer{}

	exitCode, _, err := streamCmd(context.Background(), cmd, func(string, string) {})
	if err == nil {
		t.Fatal("expected an error when cmd.Stderr is already set")
	}
	if exitCode != -1 {
		t.Fatalf("exitCode = %d, want -1", exitCode)
	}
}

// TestStreamCmdCtxCancelKillsProcess proves cancelling ctx while a command is
// running stops it without streamCmd reporting a real failure to the caller
// (exitCode -1, err nil) — the deliberate "cancellation is not an error"
// contract documented on streamCmd's ctx.Err() branch and the ExitError
// branch above it (a signal-killed process still surfaces as *exec.ExitError
// on this platform, exercising that path too).
func TestStreamCmdCtxCancelKillsProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 5")

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	exitCode, _, err := streamCmd(ctx, cmd, func(string, string) {})
	elapsed := time.Since(start)

	if elapsed > 4*time.Second {
		t.Fatalf("streamCmd took %v; ctx cancellation should have killed the process quickly", elapsed)
	}
	if exitCode == 0 {
		t.Fatalf("exitCode = %d, want nonzero/negative for a killed process", exitCode)
	}
	if err != nil {
		t.Fatalf("streamCmd error = %v, want nil (cancellation is not a reported failure)", err)
	}
}

// --- runStep ---

func TestRunStepUnavailableSkipsExecution(t *testing.T) {
	task := Task{
		Key:   "unavail",
		Title: "Unavailable Task",
		CheckAvailable: func() (bool, string) {
			return false, "install the thing first"
		},
		NewCmd: func(ctx context.Context) *exec.Cmd {
			t.Fatal("NewCmd must not be called when CheckAvailable reports false")
			return nil
		},
	}
	events := make(chan Event, 8)
	res := runStep(context.Background(), task, events)
	close(events)

	if !res.Unavailable {
		t.Fatal("expected StepResult.Unavailable = true")
	}
	if res.Hint != "install the thing first" {
		t.Fatalf("Hint = %q, want %q", res.Hint, "install the thing first")
	}
	if res.Pass {
		t.Fatal("an unavailable step must not be marked Pass")
	}

	var sawHint bool
	for ev := range events {
		if ev.Line != nil && strings.Contains(ev.Line.Text, "unavailable: install the thing first") {
			sawHint = true
		}
	}
	if !sawHint {
		t.Fatal("expected an emitted Line mentioning the unavailability hint")
	}
}

func TestRunStepEvalOutputOverridesExitCode(t *testing.T) {
	// Exit 0 but EvalOutput forces a fail.
	fail := shTask("evalfail", "exit 0")
	fail.EvalOutput = func(exitCode int, stdout string) bool { return false }
	events := make(chan Event, 8)
	res := runStep(context.Background(), fail, events)
	close(events)
	drain(events)
	if res.Pass {
		t.Fatal("EvalOutput returning false must make the step fail despite exit 0")
	}

	// Exit 1 but EvalOutput forces a pass.
	pass := shTask("evalpass", "exit 1")
	pass.EvalOutput = func(exitCode int, stdout string) bool { return true }
	events2 := make(chan Event, 8)
	res2 := runStep(context.Background(), pass, events2)
	close(events2)
	drain(events2)
	if !res2.Pass {
		t.Fatal("EvalOutput returning true must make the step pass despite exit 1")
	}
}

func TestRunStepDefaultPassFailFollowsExitCode(t *testing.T) {
	ok := shTask("ok", "exit 0")
	events := make(chan Event, 8)
	res := runStep(context.Background(), ok, events)
	close(events)
	drain(events)
	if !res.Pass || res.ExitCode != 0 {
		t.Fatalf("res = %+v, want Pass=true ExitCode=0", res)
	}

	bad := shTask("bad", "exit 7")
	events2 := make(chan Event, 8)
	res2 := runStep(context.Background(), bad, events2)
	close(events2)
	drain(events2)
	if res2.Pass || res2.ExitCode != 7 {
		t.Fatalf("res2 = %+v, want Pass=false ExitCode=7", res2)
	}
}

func TestRunStepFailedToRunEmitsLineAndNegativeResult(t *testing.T) {
	task := Task{
		Key:   "missing-binary",
		Title: "Missing Binary",
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "definitely-not-a-real-binary-xyz")
		},
	}
	events := make(chan Event, 8)
	res := runStep(context.Background(), task, events)
	close(events)

	if res.Pass {
		t.Fatal("a step whose command fails to start must not pass")
	}
	if res.ExitCode != -1 {
		t.Fatalf("ExitCode = %d, want -1", res.ExitCode)
	}
	var sawFailedToRun bool
	for ev := range events {
		if ev.Line != nil && strings.Contains(ev.Line.Text, "failed to run:") {
			sawFailedToRun = true
		}
	}
	if !sawFailedToRun {
		t.Fatal("expected an emitted Line mentioning \"failed to run:\"")
	}
}

// --- flattenSteps ---

func TestFlattenStepsPassesThroughLeafAndUnknown(t *testing.T) {
	withTempTasks(t, shTask("leaf-a", "true"))

	got := flattenSteps([]string{"leaf-a", "no-such-task"})
	want := []string{"leaf-a", "no-such-task"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("flattenSteps = %v, want %v", got, want)
	}
}

func TestFlattenStepsExpandsComposite(t *testing.T) {
	withTempTasks(t,
		shTask("leaf1", "true"),
		shTask("leaf2", "true"),
		Task{Key: "inner", Title: "inner", Steps: []string{"leaf1", "leaf2"}},
		Task{Key: "outer", Title: "outer", Steps: []string{"inner"}},
	)

	got := flattenSteps([]string{"outer"})
	want := []string{"leaf1", "leaf2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("flattenSteps(nested) = %v, want %v", got, want)
	}
}

func TestFlattenStepsGuardsSelfReferentialCycle(t *testing.T) {
	withTempTasks(t, Task{Key: "selfref", Title: "selfref", Steps: []string{"selfref"}})

	got := flattenSteps([]string{"selfref"})
	want := []string{"selfref"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("flattenSteps(self-referential) = %v, want %v (must not recurse forever)", got, want)
	}
}

// --- StartTask ---

func TestStartTaskLeafSuccess(t *testing.T) {
	withTempTasks(t, shTask("leaf-ok", "echo hi; exit 0"))
	task, ok := Lookup("leaf-ok")
	if !ok {
		t.Fatal("expected leaf-ok to be registered")
	}

	run := StartTask(context.Background(), task)
	var finalResult *Result
	for ev := range run.Events {
		if ev.Result != nil {
			r := *ev.Result
			finalResult = &r
		}
	}
	if finalResult == nil {
		t.Fatal("expected a final Result event")
	}
	if !finalResult.Pass {
		t.Fatalf("expected overall Pass=true, got %+v", finalResult)
	}
	if len(finalResult.Steps) != 1 || finalResult.Steps[0].Key != "leaf-ok" {
		t.Fatalf("Steps = %+v, want exactly one entry for leaf-ok", finalResult.Steps)
	}
}

func TestStartTaskCompositeAggregatesStepFailure(t *testing.T) {
	withTempTasks(t,
		shTask("comp-ok", "exit 0"),
		shTask("comp-bad", "exit 1"),
		Task{Key: "comp", Title: "comp", Steps: []string{"comp-ok", "comp-bad"}},
	)
	task, _ := Lookup("comp")

	run := StartTask(context.Background(), task)
	var finalResult *Result
	for ev := range run.Events {
		if ev.Result != nil {
			r := *ev.Result
			finalResult = &r
		}
	}
	if finalResult == nil {
		t.Fatal("expected a final Result event")
	}
	if finalResult.Pass {
		t.Fatal("a composite with one failing step must not report overall Pass")
	}
	if len(finalResult.Steps) != 2 {
		t.Fatalf("Steps = %+v, want 2 entries", finalResult.Steps)
	}
}

func TestStartTaskUnknownStepKeyReportsFailure(t *testing.T) {
	withTempTasks(t, Task{Key: "bad-comp", Title: "bad-comp", Steps: []string{"totally-unregistered-key"}})
	task, _ := Lookup("bad-comp")

	run := StartTask(context.Background(), task)
	var finalResult *Result
	var sawUnknown bool
	for ev := range run.Events {
		if ev.Line != nil && strings.Contains(ev.Line.Text, "unknown task") {
			sawUnknown = true
		}
		if ev.Result != nil {
			r := *ev.Result
			finalResult = &r
		}
	}
	if !sawUnknown {
		t.Fatal("expected an emitted Line reporting the unknown step key")
	}
	if finalResult == nil || finalResult.Pass {
		t.Fatalf("finalResult = %+v, want non-nil with Pass=false", finalResult)
	}
}

func TestStartTaskCtxCancelStopsSequenceEarly(t *testing.T) {
	withTempTasks(t,
		shTask("slow-first", "sleep 5"),
		shTask("never-runs", "exit 0"),
		Task{Key: "seq", Title: "seq", Steps: []string{"slow-first", "never-runs"}},
	)
	task, _ := Lookup("seq")

	ctx, cancel := context.WithCancel(context.Background())
	run := StartTask(ctx, task)

	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	var finalResult *Result
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range run.Events {
			if ev.Result != nil {
				r := *ev.Result
				finalResult = &r
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("StartTask did not stop the sequence after ctx cancellation")
	}

	if finalResult == nil {
		t.Fatal("expected a final Result event even after cancellation")
	}
	if len(finalResult.Steps) != 1 {
		t.Fatalf("Steps = %+v, want exactly 1 (the cancelled slow-first step; never-runs must be skipped)", finalResult.Steps)
	}
}
