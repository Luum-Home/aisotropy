package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// This file holds the command builders and EvalOutput functions for the
// code-optimization analysis tasks (dup, complexity, deadcode, cover, vuln,
// deps, audit, tasks). The registry table in registry.go wires them to Task
// entries; keeping the logic here mirrors how exec.go/model.go already split
// concerns out of registry.go.
//
// complexity/deadcode/vuln deliberately have no CheckAvailable: they run via
// `go run <module>@latest`, which Go resolves and caches on first use (no
// global install required, per the project's "no heavyweight deps" goal). A
// missing network connection on first run surfaces as a normal failed step
// (streamCmd already reports the tool's own stderr and marks the step
// failed) rather than a crash, so no extra availability probe is needed —
// unlike "dup" and "lint", which depend on a real pre-installed binary
// (golangci-lint) that has no auto-fetch equivalent.

// complexityThreshold is the cognitive-complexity score above which the
// "complexity" task reports a function (gocognit's own -over flag also
// makes it exit 1 when it finds anything over this threshold).
const complexityThreshold = 15

// cliRun is the "run" subcommand passed to the go/golangci-lint external
// tools this file and registry.go shell out to (distinct from keyRun, the
// registry's own "run" dev-server Task key, in registry.go).
const cliRun = "run"

// dupCmd runs golangci-lint with only the "dupl" (duplicate code) linter
// enabled, matching how the existing "lint" task shells out.
func dupCmd(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "golangci-lint", cliRun, "--enable-only", "dupl", "./...")
}

// gocognitCmd runs gocognit over every package directory in the module
// (reusing gofmtTargets, the same directory resolution "fmt"/"fmt-fix" use)
// reporting functions whose cognitive complexity exceeds complexityThreshold.
// gocognit takes directory/file arguments, not the "./..." package pattern
// go tools normally accept, so it needs the same directory-listing approach
// as gofmtCmd.
func gocognitCmd(ctx context.Context) *exec.Cmd {
	dirs := gofmtTargets(ctx)
	args := make([]string, 0, 4+len(dirs))
	args = append(args, cliRun, "github.com/uudashr/gocognit/cmd/gocognit@latest", "-over", strconv.Itoa(complexityThreshold))
	args = append(args, dirs...)
	return exec.CommandContext(ctx, "go", args...)
}

// deadcodeCmd runs golang.org/x/tools/cmd/deadcode, which reports
// unreachable functions but always exits 0 on a clean analysis run — even
// when it finds unreachable code — so this task uses the default
// exitCode==0 pass/fail (no EvalOutput): a nonzero exit here means the tool
// itself failed to run, not that dead code was found.
func deadcodeCmd(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "go", cliRun, "golang.org/x/tools/cmd/deadcode@latest", "./...")
}

// govulncheckCmd runs golang.org/x/vuln/cmd/govulncheck, which exits
// nonzero when a known vulnerability affects code actually reachable from
// this module (the default exitCode==0 pass/fail is exactly right here).
func govulncheckCmd(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "go", cliRun, "golang.org/x/vuln/cmd/govulncheck@latest", "./...")
}

// --- cover: coverage threshold gate ---

const (
	// defaultCoverageThresholdPercent is used when coverageThresholdEnvVar
	// is unset or unparsable.
	defaultCoverageThresholdPercent = 60.0
	// coverageThresholdEnvVar overrides defaultCoverageThresholdPercent,
	// e.g. AISOTROPY_X_COVER_THRESHOLD=75 for a stricter local/CI gate.
	coverageThresholdEnvVar = "AISOTROPY_X_COVER_THRESHOLD"
)

// coverageThreshold resolves the minimum total statement coverage the
// "cover" task requires to pass.
func coverageThreshold() float64 {
	if v := strings.TrimSpace(os.Getenv(coverageThresholdEnvVar)); v != "" {
		if pct, err := strconv.ParseFloat(v, 64); err == nil {
			return pct
		}
	}
	return defaultCoverageThresholdPercent
}

// coverProfilePath returns a process-unique temp file path for the "cover"
// task's coverage profile, avoiding collisions with a concurrent run on the
// same machine (e.g. a CI job and a local `x cover` at once). The file is
// left in the OS temp directory afterward (best effort only, same tradeoff
// CI coverage artifacts normally accept) rather than tracked for cleanup,
// since Task.NewCmd has no "after the command finishes" hook to remove it.
func coverProfilePath() (string, error) {
	f, err := os.CreateTemp("", "aisotropy-x-cover-*.out")
	if err != nil {
		return "", err
	}
	name := f.Name()
	_ = f.Close()
	return name, nil
}

// shellQuote wraps s in single quotes for safe interpolation into an sh -c
// script, escaping any embedded single quote. coverCmd only ever quotes a
// path os.CreateTemp produced, but this is defensive in case TMPDIR ever
// contains shell-meaningful characters.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// coverCmd runs the unit test suite with a coverage profile, then prints the
// combined total coverage via `go tool cover -func`. Chaining two real `go`
// subcommands through a shell is a deliberate, narrowly-scoped exception to
// this package's usual "exec the tool directly" style (every other task in
// the registry runs one real command with no shell involved): Task.NewCmd
// returns a single *exec.Cmd, but the combined total percentage this task's
// EvalOutput needs only exists after a *second* command reads the profile
// the first one wrote. Running both through `sh -c "A && B"` keeps that
// total visible in the streamed output the user sees, instead of computing
// it invisibly inside EvalOutput (which cannot emit new output lines).
func coverCmd(ctx context.Context) *exec.Cmd {
	profile, err := coverProfilePath()
	if err != nil {
		// Extremely unlikely (unwritable temp dir): fail loudly with the
		// real cause instead of silently skipping coverage enforcement.
		return exec.CommandContext(ctx, "sh", "-c",
			fmt.Sprintf("echo 'x: cover: failed to create temp profile: %v' >&2; exit 1", err))
	}
	script := fmt.Sprintf("go test -coverprofile=%s ./... && go tool cover -func=%s",
		shellQuote(profile), shellQuote(profile))
	return exec.CommandContext(ctx, "sh", "-c", script)
}

// coverageTotalRe matches `go tool cover -func`'s final summary line, e.g.
// "total:                                          (statements)   51.1%".
var coverageTotalRe = regexp.MustCompile(`total:\s*\(statements\)\s*([0-9.]+)%`)

// coverageMeetsThreshold is coverCmd's EvalOutput: pass requires both
// commands in coverCmd's chain to have succeeded (exitCode == 0, so a test
// failure or a `go tool cover` error already fails the task) and the parsed
// total statement coverage to be at or above coverageThreshold().
func coverageMeetsThreshold(exitCode int, stdout string) bool {
	if exitCode != 0 {
		return false
	}
	m := coverageTotalRe.FindStringSubmatch(stdout)
	if m == nil {
		return false
	}
	pct, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return false
	}
	return pct >= coverageThreshold()
}

// --- deps: memory-tier layering check ---

// forbiddenMemoryImports enforces memory-tier independence: internal/memory
// (BadgerDB working memory, L1) must not import the other memory tiers —
// internal/facts (rqlite) or internal/semantic (Qdrant) — per the memory
// hierarchy described in .claude/rules/architecture.md ("Memory hierarchy by
// access temperature"). All three tiers are meant to be siblings composed by
// the orchestration layer (internal/api, internal/llm), never coupled to one
// another directly; a direct import here would be a cross-tier leak.
var forbiddenMemoryImports = []string{
	"github.com/Luum-Home/aisotropy/internal/facts",
	"github.com/Luum-Home/aisotropy/internal/semantic",
}

// goListPackage mirrors the subset of `go list -json` output the "deps"
// task needs: a package's own import path and its direct imports.
type goListPackage struct {
	ImportPath string   `json:"ImportPath"`
	Imports    []string `json:"Imports"`
}

// depsCmd runs `go list -json` over internal/memory so EvalOutput can
// inspect its direct imports for a forbidden memory-tier edge. Unlike most
// tasks in this registry it addresses the package by its fully-qualified
// import path rather than a "./..." relative pattern, so the check resolves
// correctly regardless of the working directory the "x" binary (or its
// tests) happen to run from — every other task assumes the documented
// "run from the repository root" convention (see main.go's package doc);
// this one does not need to.
func depsCmd(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "go", "list", "-json", "github.com/Luum-Home/aisotropy/internal/memory/...")
}

// decodeGoListPackages decodes the back-to-back JSON objects `go list -json`
// emits (one per matched package; not a JSON array) from stdout.
func decodeGoListPackages(stdout string) ([]goListPackage, error) {
	dec := json.NewDecoder(strings.NewReader(stdout))
	var pkgs []goListPackage
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			return nil, fmt.Errorf("decoding go list -json output: %w", err)
		}
		pkgs = append(pkgs, pkg)
	}
	return pkgs, nil
}

func isForbiddenMemoryImport(imp string) bool {
	for _, f := range forbiddenMemoryImports {
		if imp == f {
			return true
		}
	}
	return false
}

// layerCheckPass is depsCmd's EvalOutput: it decodes the `go list -json`
// output captured in stdout and fails if any matched package's direct
// imports include a forbidden memory-tier import.
func layerCheckPass(exitCode int, stdout string) bool {
	if exitCode != 0 {
		return false
	}
	pkgs, err := decodeGoListPackages(stdout)
	if err != nil || len(pkgs) == 0 {
		return false
	}
	for _, pkg := range pkgs {
		for _, imp := range pkg.Imports {
			if isForbiddenMemoryImport(imp) {
				return false
			}
		}
	}
	return true
}

// --- tasks: self-documenting registry dump, reachable from the TUI too ---

// tasksCmd prints the full task registry (key + description) through the
// normal exec/streaming pipeline so it is reachable from inside the TUI, not
// just `x list`/`x --help` at the CLI dispatch layer (main.go). It reuses
// ListTasks — the same renderer those two CLI verbs already call — so the
// two surfaces never drift apart, writing its output to a temp file and
// `cat`-ing that file rather than piping through a shell, keeping this
// task's command a direct tool invocation like every other task in the
// registry (cover is the one deliberate exception, see its comment).
func tasksCmd(ctx context.Context) *exec.Cmd {
	f, err := os.CreateTemp("", "aisotropy-x-tasks-*.txt")
	if err != nil {
		// Extremely unlikely (unwritable temp dir): fail loudly with the
		// real cause instead of silently printing nothing.
		return exec.CommandContext(ctx, "sh", "-c",
			fmt.Sprintf("echo 'x: tasks: failed to create temp file: %v' >&2; exit 1", err))
	}
	ListTasks(f)
	_ = f.Close()
	return exec.CommandContext(ctx, "cat", f.Name())
}
