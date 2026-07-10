package main

import (
	"context"
	"os/exec"
	"strings"
)

// Task describes one dev-console job: what it is, how to run it, and how to
// tell whether a run passed. Tasks either wrap a single external command
// (NewCmd) or aggregate other tasks in sequence (Steps), never both.
type Task struct {
	// Key is the stable identifier used on the CLI (e.g. "x vet") and as the
	// map key for status tracking in the TUI.
	Key string
	// Title is a short human-readable label shown in the task list.
	Title string
	// Description explains what the task does; shown in `x list` and help.
	Description string
	// Category groups related tasks for display purposes.
	Category string

	// NewCmd builds the *exec.Cmd for this task, scoped to ctx so the caller
	// can cancel a running process (context cancellation kills the child).
	// nil for composite tasks, where Steps is used instead.
	NewCmd func(ctx context.Context) *exec.Cmd

	// Steps lists task keys to run in sequence for composite tasks (e.g.
	// "check" running build, fmt, vet, test). Leave nil for leaf tasks.
	Steps []string

	// CheckAvailable reports whether the task's underlying tool is present
	// on PATH. nil means the task is always considered available. When it
	// reports false, the task is skipped and marked "unavailable" instead of
	// being executed (and instead of crashing on a missing binary).
	CheckAvailable func() (available bool, hint string)

	// EvalOutput inspects the process exit code and captured stdout to
	// decide pass/fail. nil defaults to "exitCode == 0". Used by tasks such
	// as fmt, where gofmt -l can exit 0 while still listing files that need
	// formatting on stdout.
	EvalOutput func(exitCode int, stdout string) bool
}

// checkGolangciLint reports whether golangci-lint is on PATH. golangci-lint
// is a separate binary the project does not vendor or install automatically,
// so absence is an expected, non-fatal condition rather than an error.
func checkGolangciLint() (bool, string) {
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		return false, "install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"
	}
	return true, ""
}

// fmtClean reports pass only when gofmt found nothing to report: gofmt -l
// exits 0 even when it lists unformatted files on stdout, so the exit code
// alone is not a reliable pass/fail signal for this task.
func fmtClean(exitCode int, stdout string) bool {
	return exitCode == 0 && strings.TrimSpace(stdout) == ""
}

// gofmtTargets resolves the directories gofmt should walk for the fmt and
// fmt-fix tasks: every package directory in the main module, exactly what
// "go build/vet/test ./..." already scope themselves to. Unlike those Go
// tool invocations, a plain "gofmt -l ." is a filesystem walk unaware of
// module boundaries, so it would also flag vendored/reference trees such as
// external/ (a separate Go module checked out for analysis, gitignored —
// see .gitignore) that this project does not own and cannot fix. Falling
// back to "." keeps the task usable even if "go list" itself fails.
func gofmtTargets(ctx context.Context) []string {
	out, err := exec.CommandContext(ctx, "go", "list", "-f", "{{.Dir}}", "./...").Output()
	dirs := strings.Fields(string(out))
	if err != nil || len(dirs) == 0 {
		return []string{"."}
	}
	return dirs
}

// gofmtCmd builds a `gofmt <mode> <module package dirs...>` command, shared
// by the fmt (-l) and fmt-fix (-w) tasks.
func gofmtCmd(mode string) func(ctx context.Context) *exec.Cmd {
	return func(ctx context.Context) *exec.Cmd {
		args := append([]string{mode}, gofmtTargets(ctx)...)
		return exec.CommandContext(ctx, "gofmt", args...)
	}
}

// registry is the ordered list of tasks shown in the TUI and resolvable from
// the CLI. Order here is the display order in the interactive list.
var registry = []Task{
	{
		Key:         "build",
		Title:       "build",
		Description: "go build ./... — compiles every package",
		Category:    "quality-gate",
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "go", "build", "./...")
		},
	},
	{
		Key:         "fmt",
		Title:       "fmt",
		Description: "gofmt -l <module dirs> — reports files that are not gofmt-formatted",
		Category:    "quality-gate",
		NewCmd:      gofmtCmd("-l"),
		EvalOutput:  fmtClean,
	},
	{
		Key:         "fmt-fix",
		Title:       "fmt-fix",
		Description: "gofmt -w <module dirs> — rewrites files in place to match gofmt",
		Category:    "quality-gate",
		NewCmd:      gofmtCmd("-w"),
	},
	{
		Key:         "vet",
		Title:       "vet",
		Description: "go vet ./... — static analysis for suspicious constructs",
		Category:    "quality-gate",
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "go", "vet", "./...")
		},
	},
	{
		Key:            "lint",
		Title:          "lint",
		Description:    "golangci-lint run ./... — aggregated linters",
		Category:       "quality-gate",
		CheckAvailable: checkGolangciLint,
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "golangci-lint", "run", "./...")
		},
	},
	{
		Key:         "test",
		Title:       "test",
		Description: "go test ./... — runs the unit test suite",
		Category:    "quality-gate",
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "go", "test", "./...")
		},
	},
	{
		Key:         "race",
		Title:       "race",
		Description: "go test -race ./... — unit tests with the race detector",
		Category:    "quality-gate",
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "go", "test", "-race", "./...")
		},
	},
	{
		Key:         "bench",
		Title:       "bench",
		Description: "go test -run '^$' -bench=. -benchmem ./... — benchmarks only",
		Category:    "quality-gate",
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "go", "test", "-run", "^$", "-bench=.", "-benchmem", "./...")
		},
	},
	{
		Key:         "example-build",
		Title:       "example-build",
		Description: "go -C examples/weatherbot build ./... — compiles the public-surface example bot (a separate module)",
		Category:    "example",
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "go", "-C", "examples/weatherbot", "build", "./...")
		},
	},
	{
		Key:         "example-vet",
		Title:       "example-vet",
		Description: "go -C examples/weatherbot vet ./... — vets the public-surface example bot",
		Category:    "example",
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "go", "-C", "examples/weatherbot", "vet", "./...")
		},
	},
	{
		Key:         "example-build-helpdesk",
		Title:       "example-build-helpdesk",
		Description: "go -C examples/helpdesk build ./... — compiles the realistic public-surface example bot (typed tools + YAML wiring, a separate module)",
		Category:    "example",
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "go", "-C", "examples/helpdesk", "build", "./...")
		},
	},
	{
		Key:         "example-vet-helpdesk",
		Title:       "example-vet-helpdesk",
		Description: "go -C examples/helpdesk vet ./... — vets the realistic public-surface example bot",
		Category:    "example",
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "go", "-C", "examples/helpdesk", "vet", "./...")
		},
	},
	{
		Key:         "example",
		Title:       "example",
		Description: "build + vet examples/weatherbot + examples/helpdesk — proves the public adoption surface compiles against the façade only",
		Category:    "example",
		Steps:       []string{"example-build", "example-vet", "example-build-helpdesk", "example-vet-helpdesk"},
	},
	{
		Key:         "run",
		Title:       "run",
		Description: "go run ./cmd/aisotropyd — starts the server; long-running, streams until cancelled",
		Category:    "dev-server",
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "go", "run", "./cmd/aisotropyd")
		},
	},
	{
		Key:         "check",
		Title:       "check",
		Description: "build, fmt, vet, test, example in sequence — the quality gates from agent-tooling.yaml plus the adoption-surface example",
		Category:    "quality-gate",
		Steps:       []string{"build", "fmt", "vet", "test", "example"},
	},
}

// Tasks returns the ordered task registry.
func Tasks() []Task {
	return registry
}

// Lookup resolves a task by its Key.
func Lookup(key string) (Task, bool) {
	for _, t := range registry {
		if t.Key == key {
			return t, true
		}
	}
	return Task{}, false
}
