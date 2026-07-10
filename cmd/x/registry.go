package main

import (
	"context"
	"fmt"
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

// Task keys and categories referenced from more than one place in this file
// (a Task's own Key/Title, a composite Task's Steps list, and/or the literal
// arg passed to the underlying tool). Centralizing them as constants means a
// key can never drift between where it's declared and where it's referenced.
const (
	keyBuild                = "build"
	keyFmt                  = "fmt"
	keyVet                  = "vet"
	keyLint                 = "lint"
	keyTest                 = "test"
	keyExampleBuild         = "example-build"
	keyExample              = "example" // also the shared Category for the four example-* tasks
	keyExampleVet           = "example-vet"
	keyExampleBuildHelpdesk = "example-build-helpdesk"
	keyExampleVetHelpdesk   = "example-vet-helpdesk"
	keyRun                  = "run"
	keyDup                  = "dup"
	keyComplexity           = "complexity"
	keyDeadcode             = "deadcode"
	keyCover                = "cover"
	keyVuln                 = "vuln"
	keyDeps                 = "deps"
	keyAudit                = "audit"
	keyTasks                = "tasks"

	categoryQualityGate = "quality-gate"
	categoryAnalysis    = "analysis"
)

// registry is the ordered list of tasks shown in the TUI and resolvable from
// the CLI. Order here is the display order in the interactive list.
var registry = []Task{
	{
		Key:         keyBuild,
		Title:       keyBuild,
		Description: "go build ./... — compiles every package",
		Category:    categoryQualityGate,
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "go", keyBuild, "./...")
		},
	},
	{
		Key:         keyFmt,
		Title:       keyFmt,
		Description: "gofmt -l <module dirs> — reports files that are not gofmt-formatted",
		Category:    categoryQualityGate,
		NewCmd:      gofmtCmd("-l"),
		EvalOutput:  fmtClean,
	},
	{
		Key:         "fmt-fix",
		Title:       "fmt-fix",
		Description: "gofmt -w <module dirs> — rewrites files in place to match gofmt",
		Category:    categoryQualityGate,
		NewCmd:      gofmtCmd("-w"),
	},
	{
		Key:         keyVet,
		Title:       keyVet,
		Description: "go vet ./... — static analysis for suspicious constructs",
		Category:    categoryQualityGate,
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "go", keyVet, "./...")
		},
	},
	{
		Key:            keyLint,
		Title:          keyLint,
		Description:    "golangci-lint run ./... — aggregated linters",
		Category:       categoryQualityGate,
		CheckAvailable: checkGolangciLint,
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "golangci-lint", cliRun, "./...")
		},
	},
	{
		Key:         keyTest,
		Title:       keyTest,
		Description: "go test ./... — runs the unit test suite",
		Category:    categoryQualityGate,
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "go", keyTest, "./...")
		},
	},
	{
		Key:         "race",
		Title:       "race",
		Description: "go test -race ./... — unit tests with the race detector",
		Category:    categoryQualityGate,
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "go", keyTest, "-race", "./...")
		},
	},
	{
		Key:         "bench",
		Title:       "bench",
		Description: "go test -run '^$' -bench=. -benchmem ./... — benchmarks only",
		Category:    categoryQualityGate,
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "go", keyTest, "-run", "^$", "-bench=.", "-benchmem", "./...")
		},
	},
	{
		Key:         keyExampleBuild,
		Title:       keyExampleBuild,
		Description: "go -C examples/weatherbot build ./... — compiles the public-surface example bot (a separate module)",
		Category:    keyExample,
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "go", "-C", "examples/weatherbot", keyBuild, "./...")
		},
	},
	{
		Key:         keyExampleVet,
		Title:       keyExampleVet,
		Description: "go -C examples/weatherbot vet ./... — vets the public-surface example bot",
		Category:    keyExample,
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "go", "-C", "examples/weatherbot", keyVet, "./...")
		},
	},
	{
		Key:         keyExampleBuildHelpdesk,
		Title:       keyExampleBuildHelpdesk,
		Description: "go -C examples/helpdesk build ./... — compiles the realistic public-surface example bot (typed tools + YAML wiring, a separate module)",
		Category:    keyExample,
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "go", "-C", "examples/helpdesk", keyBuild, "./...")
		},
	},
	{
		Key:         keyExampleVetHelpdesk,
		Title:       keyExampleVetHelpdesk,
		Description: "go -C examples/helpdesk vet ./... — vets the realistic public-surface example bot",
		Category:    keyExample,
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "go", "-C", "examples/helpdesk", keyVet, "./...")
		},
	},
	{
		Key:         keyExample,
		Title:       keyExample,
		Description: "build + vet examples/weatherbot + examples/helpdesk — proves the public adoption surface compiles against the façade only",
		Category:    keyExample,
		Steps:       []string{keyExampleBuild, keyExampleVet, keyExampleBuildHelpdesk, keyExampleVetHelpdesk},
	},
	{
		Key:         keyRun,
		Title:       keyRun,
		Description: "go run ./cmd/aisotropyd — starts the server; long-running, streams until cancelled",
		Category:    "dev-server",
		NewCmd: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "go", cliRun, "./cmd/aisotropyd")
		},
	},
	{
		Key:         "check",
		Title:       "check",
		Description: "build, fmt, vet, test, example in sequence — the quality gates from agent-tooling.yaml plus the adoption-surface example",
		Category:    categoryQualityGate,
		Steps:       []string{keyBuild, keyFmt, keyVet, keyTest, keyExample},
	},
	{
		Key:            keyDup,
		Title:          keyDup,
		Description:    "golangci-lint run --enable-only dupl ./... — duplicate-code clones report",
		Category:       categoryAnalysis,
		CheckAvailable: checkGolangciLint,
		NewCmd:         dupCmd,
	},
	{
		Key:         keyComplexity,
		Title:       keyComplexity,
		Description: fmt.Sprintf("go run gocognit -over %d <module dirs> — functions over cognitive-complexity threshold %d", complexityThreshold, complexityThreshold),
		Category:    categoryAnalysis,
		NewCmd:      gocognitCmd,
	},
	{
		Key:         keyDeadcode,
		Title:       keyDeadcode,
		Description: "go run golang.org/x/tools/cmd/deadcode@latest ./... — unreachable-function report",
		Category:    categoryAnalysis,
		NewCmd:      deadcodeCmd,
	},
	{
		Key:         keyCover,
		Title:       keyCover,
		Description: "go test -coverprofile + go tool cover -func ./... — total coverage %, fails below the threshold (default 60%, override via AISOTROPY_X_COVER_THRESHOLD)",
		Category:    categoryAnalysis,
		NewCmd:      coverCmd,
		EvalOutput:  coverageMeetsThreshold,
	},
	{
		Key:         keyVuln,
		Title:       keyVuln,
		Description: "go run golang.org/x/vuln/cmd/govulncheck@latest ./... — known-vulnerability scan of the standard library and dependencies",
		Category:    categoryAnalysis,
		NewCmd:      govulncheckCmd,
	},
	{
		Key:         keyDeps,
		Title:       keyDeps,
		Description: "go list -json internal/memory/... — fails if internal/memory imports internal/facts or internal/semantic (memory tiers must stay independent)",
		Category:    categoryAnalysis,
		NewCmd:      depsCmd,
		EvalOutput:  layerCheckPass,
	},
	{
		Key:         keyAudit,
		Title:       keyAudit,
		Description: "dup, complexity, deadcode, vuln, deps in sequence — consolidated code-optimization report",
		Category:    categoryAnalysis,
		Steps:       []string{keyDup, keyComplexity, keyDeadcode, keyVuln, keyDeps},
	},
}

// init appends the "tasks" entry to registry separately from the registry
// literal above. tasksCmd (analysis.go) calls ListTasks, which calls
// Tasks(), which reads registry itself; if "tasks" were declared inline
// in the registry literal like every other entry, that reference would make
// the Go compiler's package-initializer dependency analysis walk the chain
// registry -> tasksCmd -> ListTasks -> Tasks -> registry and reject it as an
// initialization cycle, even though tasksCmd is only ever invoked long after
// package initialization has finished. Appending in init() — which always
// runs after every package-level variable has finished initializing —
// breaks that static analysis without changing runtime behavior or the
// resulting task order (it still ends up last).
func init() {
	registry = append(registry, Task{
		Key:         keyTasks,
		Title:       keyTasks,
		Description: "prints every registered task's key and description (same content as `x list`, runnable from inside the TUI)",
		Category:    "meta",
		NewCmd:      tasksCmd,
	})
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
