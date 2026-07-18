// Command x is Aisotropy's developer console: an interactive Bubbletea TUI
// for running the project's dev tasks (build, fmt, vet, lint, test, race,
// bench, run, check), which also works non-interactively as subcommands so
// the same task registry drives both local development and CI/scripts.
//
// Run it from the repository root — task commands (go build ./..., go run
// ./cmd/aisotropyd, etc.) are relative to it, matching cmd/aisotropyd/main.go.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run dispatches to the interactive TUI (no args) or a non-interactive task
// subcommand, returning the process exit code.
func run(args []string) int {
	if len(args) == 0 {
		return runTUI()
	}

	switch args[0] {
	case "--help", "-h", "help":
		PrintHelp(os.Stdout)
		return 0
	case "list":
		ListTasks(os.Stdout)
		return 0
	default:
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(sigCh)
		go func() {
			if _, ok := <-sigCh; ok {
				cancel()
			}
		}()

		return RunNonInteractive(ctx, args[0], os.Stdout, os.Stderr)
	}
}

// runTUI starts the interactive Bubbletea program and blocks until the user
// quits. It is never invoked from the non-interactive dispatch path.
//
// Building the *tea.Program here (rather than inline in runProgram) keeps
// runProgram free of any dependency on a real terminal, so tests can drive it
// with a Program constructed over tea.WithInput/tea.WithOutput instead of the
// real stdin/stdout — runTUI itself (real terminal, tea.WithAltScreen) is not
// unit-tested, since doing so would require a real TTY or would block reading
// from the test process's stdin.
func runTUI() int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	return runProgram(tea.NewProgram(NewModel(ctx), tea.WithAltScreen()), os.Stderr)
}

// runProgram runs p to completion and translates a Bubbletea error into the
// process exit code, printing it to stderr. Split out from runTUI so tests
// can supply a Program built with test-only I/O (tea.WithInput/tea.WithOutput)
// and a captured stderr writer, without touching the real terminal.
func runProgram(p *tea.Program, stderr io.Writer) int {
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(stderr, "x:", err)
		return 1
	}
	return 0
}
