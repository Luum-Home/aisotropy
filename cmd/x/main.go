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
func runTUI() int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := tea.NewProgram(NewModel(ctx), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "x:", err)
		return 1
	}
	return 0
}
