package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestRunHelpVariants exercises every alias of the --help dispatch branch in
// run(), which is otherwise only invoked via os.Args from main().
func TestRunHelpVariants(t *testing.T) {
	for _, arg := range []string{"--help", "-h", "help"} {
		t.Run(arg, func(t *testing.T) {
			if code := run([]string{arg}); code != 0 {
				t.Fatalf("run([%q]) = %d, want 0", arg, code)
			}
		})
	}
}

func TestRunList(t *testing.T) {
	if code := run([]string{"list"}); code != 0 {
		t.Fatalf("run([\"list\"]) = %d, want 0", code)
	}
}

// TestRunUnknownTaskDispatchesNonInteractive drives run()'s default branch
// (context/signal setup + RunNonInteractive) with an unresolvable task key,
// which fails fast without shelling out to any real tool.
func TestRunUnknownTaskDispatchesNonInteractive(t *testing.T) {
	if code := run([]string{"does-not-exist"}); code != 1 {
		t.Fatalf("run([\"does-not-exist\"]) = %d, want 1", code)
	}
}

// TestRunProgramQuitsCleanly drives runProgram over a Bubbletea Program wired
// to an in-memory pipe (no real terminal): sending "q" should make the
// program quit and Run() return a nil error, so runProgram reports 0.
func TestRunProgramQuitsCleanly(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()

	var out, stderr bytes.Buffer
	p := tea.NewProgram(NewModel(context.Background()), tea.WithInput(pr), tea.WithOutput(&out))

	go func() {
		_, _ = pw.Write([]byte("q"))
		_ = pw.Close()
	}()

	done := make(chan int, 1)
	go func() { done <- runProgram(p, &stderr) }()

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("runProgram() = %d, want 0 (stderr: %q)", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		p.Kill()
		t.Fatal("runProgram did not return after sending q")
	}
}

// errReader is an io.Reader that always fails with a non-EOF error, used to
// force Bubbletea's input read loop to surface a fatal error from Run().
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// TestRunProgramReportsError proves the error branch: a Program whose input
// reader fails outright makes Run() return an error, which runProgram must
// print to stderr and translate into exit code 1.
func TestRunProgramReportsError(t *testing.T) {
	wantErr := errors.New("boom: synthetic input failure")
	var out, stderr bytes.Buffer
	p := tea.NewProgram(NewModel(context.Background()), tea.WithInput(errReader{wantErr}), tea.WithOutput(&out))

	done := make(chan int, 1)
	go func() { done <- runProgram(p, &stderr) }()

	select {
	case code := <-done:
		if code != 1 {
			t.Fatalf("runProgram() = %d, want 1", code)
		}
		if stderr.Len() == 0 {
			t.Error("expected runProgram to write the error to stderr")
		}
	case <-time.After(5 * time.Second):
		p.Kill()
		t.Fatal("runProgram did not return after an input read failure")
	}
}
