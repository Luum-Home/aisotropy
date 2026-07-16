package main

// The `task` subcommand is the operator-facing front door to the sandboxed
// agentic coding primitive (ADR-0036 / ADR-0038): it runs ONE key-brokered task
// against a local git repository from the command line.
//
//	aisotropy task run <repo-dir> --prompt "..." [--provider docker|podman]
//	                              [--image X] [--max-steps N]
//
// Deliberate reuse, no duplicated logic: every hard part — the isolated git
// worktree, the sandbox container, the host-side agent loop, the key-broker
// invariant (the provider key is spent HOST-SIDE and never enters the
// container), and the merge-to-HEAD — lives behind the PUBLIC aisotropy.App API
// (App.RunTask). This file only parses flags, selects a built-in SandboxProvider
// (DockerSandbox / PodmanSandbox) for --provider, streams the run's shell output
// live via TaskRequest.OnLine, and prints the resulting commits/branch/steps.
// It imports the public package only — no internal/*.
//
// Stdlib only (flag), matching the `plugins` subcommand's dependency posture.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Luum-Home/aisotropy/aisotropy"
)

// taskUsage prints the `task` subcommand summary.
func taskUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  aisotropy task run <repo-dir> --prompt "<instruction>" [flags]

Runs one sandboxed agentic coding task against a local git repository. The task
runs on a fresh worktree off HEAD in a throwaway container; commits it produces
are merged back into the repo's HEAD (your checkout is never touched directly).
The provider API key is spent host-side and NEVER enters the container.

Flags:
  --prompt string     Task instruction handed to the model (required)
  --provider string   Sandbox backend: docker (default) or podman
  --image string      Container image (empty uses the built-in default)
  --max-steps int     Bound the agent loop (<= 0 uses the runtime default)

With no ANTHROPIC_API_KEY set, the run uses the keyless stub client: the loop
still completes so you can exercise the wiring, but a real key does real work.
`)
}

// taskRunConfig is the parsed, validated form of `task run`'s arguments. Parsing
// is split from execution so the flag surface can be unit-tested without booting
// a real sandbox (which needs Docker/Podman).
type taskRunConfig struct {
	RepoDir  string
	Prompt   string
	Provider string
	Image    string
	MaxSteps int
}

// runTask dispatches the `task` subcommands. Today only `run` exists; the switch
// mirrors runPlugins so a second verb is a new case, not a new framework.
func runTask(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		taskUsage(stderr)
		return 2
	}
	switch args[0] {
	case "run":
		return taskRun(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		taskUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "aisotropy task: unknown subcommand %q\n\n", args[0])
		taskUsage(stderr)
		return 2
	}
}

// parseTaskRunArgs parses and validates `task run`'s flags + the positional
// <repo-dir>. It is pure (writes nothing, boots nothing) so tests can assert the
// argument contract directly. On a usage error it returns a non-nil error whose
// text is safe to print to stderr.
func parseTaskRunArgs(args []string) (taskRunConfig, error) {
	fs := flag.NewFlagSet("task run", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // caller renders usage; keep parsing side-effect-free
	prompt := fs.String("prompt", "", "task instruction handed to the model (required)")
	provider := fs.String("provider", "docker", "sandbox backend: docker or podman")
	image := fs.String("image", "", "container image (empty uses the built-in default)")
	maxSteps := fs.Int("max-steps", 0, "bound the agent loop (<= 0 uses the runtime default)")
	// Go's flag package stops parsing at the first non-flag token, but the usage
	// puts <repo-dir> FIRST (aisotropy task run <repo-dir> --prompt ...). Permute
	// flag pairs ahead of positionals so flags may appear on either side of the
	// repo-dir, then hand the reordered slice to Parse.
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return taskRunConfig{}, err
	}

	// Exactly one positional: the repo dir. Flags may appear before or after it,
	// but there must be one and only one non-flag argument.
	rest := fs.Args()
	if len(rest) == 0 {
		return taskRunConfig{}, fmt.Errorf("missing <repo-dir> argument")
	}
	if len(rest) > 1 {
		return taskRunConfig{}, fmt.Errorf("expected exactly one <repo-dir>, got %d: %v", len(rest), rest)
	}

	cfg := taskRunConfig{
		RepoDir:  rest[0],
		Prompt:   strings.TrimSpace(*prompt),
		Provider: strings.ToLower(strings.TrimSpace(*provider)),
		Image:    strings.TrimSpace(*image),
		MaxSteps: *maxSteps,
	}
	if cfg.Prompt == "" {
		return taskRunConfig{}, fmt.Errorf("--prompt is required")
	}
	if _, err := selectProvider(cfg.Provider); err != nil {
		return taskRunConfig{}, err
	}
	return cfg, nil
}

// reorderFlagsFirst moves flag tokens ahead of positional tokens so the stdlib
// flag parser (which halts at the first non-flag argument) accepts flags on
// EITHER side of the positional <repo-dir>. Every flag this subcommand defines
// takes a value, so a bare "--name" token consumes the following token as its
// value; the "--name=value" form is self-contained. Unknown flags are passed
// through untouched so fs.Parse still reports them as errors.
func reorderFlagsFirst(args []string) []string {
	valueFlags := map[string]bool{
		"-prompt": true, "--prompt": true,
		"-provider": true, "--provider": true,
		"-image": true, "--image": true,
		"-max-steps": true, "--max-steps": true,
	}
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			// Everything after "--" is positional by convention.
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			// A bare known value-flag (no "=") pulls in the next token as its value.
			if !strings.Contains(a, "=") && valueFlags[a] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

// selectProvider maps the --provider value to a built-in SandboxProvider from
// the public API. Only the self-hostable, no-lock-in built-ins are offered here;
// a bring-your-own provider (Kubernetes, Firecracker, …) is a library-level
// concern (see docs/architecture/sandbox-providers.md), not a CLI flag.
func selectProvider(name string) (aisotropy.SandboxProvider, error) {
	switch name {
	case "", "docker":
		return aisotropy.DockerSandbox(), nil
	case "podman":
		return aisotropy.PodmanSandbox(), nil
	default:
		return nil, fmt.Errorf("unknown --provider %q (want docker or podman)", name)
	}
}

// taskRun parses the arguments, builds an App off the same env-driven defaults
// the server uses (aisotropy.New()), and runs one task — streaming the run's
// shell output live and printing the commits/branch/steps it produced.
func taskRun(args []string, stdout, stderr io.Writer) int {
	cfg, err := parseTaskRunArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "aisotropy task run: %v\n\n", err)
		taskUsage(stderr)
		return 2
	}
	return execTaskRun(context.Background(), cfg, stdout, stderr)
}

// execTaskRun performs the actual run for an already-validated config. Split out
// so the parse path (tested everywhere) and the execute path (needs a sandbox
// backend, exercised by a Docker-gated smoke test) are separable.
func execTaskRun(ctx context.Context, cfg taskRunConfig, stdout, stderr io.Writer) int {
	provider, err := selectProvider(cfg.Provider)
	if err != nil {
		// Unreachable after parseTaskRunArgs validated it, but keep it honest.
		fmt.Fprintf(stderr, "aisotropy task run: %v\n", err)
		return 2
	}

	app := aisotropy.New()

	fmt.Fprintf(stdout, "running task on %s (provider=%s)\n", cfg.RepoDir, cfg.Provider)
	res, err := app.RunTask(ctx, aisotropy.TaskRequest{
		RepoDir:  cfg.RepoDir,
		Prompt:   cfg.Prompt,
		Image:    cfg.Image,
		MaxSteps: cfg.MaxSteps,
		Scopes:   aisotropy.SandboxToolScopes(),
		Provider: provider,
		// Stream shell stdout live — the operator sees the model's hands working
		// as the run proceeds, not only a summary at the end.
		OnLine: func(line string) {
			fmt.Fprintf(stdout, "  | %s\n", line)
		},
	})
	if err != nil {
		fmt.Fprintf(stderr, "aisotropy task run: %v\n", err)
		return 1
	}

	// Summary: branch, commits (newest first), and the tool-decision trace count.
	if res.Branch != "" {
		fmt.Fprintf(stdout, "branch: %s\n", res.Branch)
	}
	if len(res.Commits) == 0 {
		fmt.Fprintln(stdout, "commits: none (nothing was committed; branch discarded)")
	} else {
		fmt.Fprintf(stdout, "commits (%d, merged into HEAD, newest first):\n", len(res.Commits))
		for _, sha := range res.Commits {
			fmt.Fprintf(stdout, "  %s\n", sha)
		}
	}
	fmt.Fprintf(stdout, "steps: %d\n", len(res.Steps))
	for i, s := range res.Steps {
		tool := s.Tool
		if tool == "" {
			tool = "(direct answer)"
		}
		fmt.Fprintf(stdout, "  %d. %s\n", i+1, tool)
	}
	return 0
}
