// Command task-agent is a minimal, runnable demonstration of Aisotropy's
// sandboxed task agent — the KEY-BROKERING primitive (ADR-0036).
//
// What it shows, in one file:
//
//   - It builds an Aisotropy runtime with NO provider credential, so inference
//     falls back to the keyless *stub* model (Aisotropy's TestModel equivalent).
//   - It creates a throwaway git repository in a temp dir (so the demo never
//     touches any real checkout) and points RunTask at it.
//   - RunTask opens an isolated git worktree on a fresh task branch, stands up a
//     Docker sandbox over it, and drives the HOST-SIDE agent loop over the
//     sandboxed "hands" (run_shell / write_file / read_file / git_commit).
//   - It prints the resulting commits, branch, and per-step trace.
//
// # The property this demonstrates: the container never gets your API key
//
// Aisotropy is a key-BROKER, not a key-forwarder. The LLM call — the one
// privileged operation that spends ANTHROPIC_API_KEY / OPENAI_API_KEY — happens
// HOST-SIDE, in this process. Only the EXECUTION of a tool the model chose
// ("write this file", "run that shell") is dispatched into the sandbox
// container. The container holds the agent's hands, never its wallet. That is
// the exact inverse of a coding-CLI-in-a-box (sandcastle, Claude Code, Aider,
// …), which must place the raw provider key INSIDE the sandbox because the CLI
// calls the model from in there.
//
// # Honest note on the stub vs. a real model
//
// This demo runs with the keyless stub, which produces a canned response and NO
// actionable tool call. So a stub run completes gracefully with ZERO commits —
// it exercises the full plumbing (worktree → container → host-side loop →
// merge-or-discard) and proves the container boots without a key, but the
// "agent" does not actually edit files, because there is no real model deciding
// to. Point Aisotropy at a real provider (export OPENROUTER_API_KEY /
// OPENAI_API_KEY / ANTHROPIC_API_KEY before running) and the same RunTask call
// drives a real model that will use the hands to make edits and commit them —
// still without the key ever entering the container.
//
// # Requirements
//
//   - Docker (the sandbox provider shells out to the `docker` CLI). Without it,
//     RunTask fails at sandbox creation and this program prints a clear message.
//   - git (used both to build the throwaway repo here and by RunTask's worktree
//     plumbing).
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Luum-Home/aisotropy/aisotropy"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "task-agent:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// 1. Build a throwaway git repo so the demo never touches a real checkout.
	repoDir, err := newThrowawayRepo()
	if err != nil {
		return fmt.Errorf("create throwaway repo: %w", err)
	}
	defer os.RemoveAll(filepath.Dir(repoDir)) // temp parent (holds repo + any worktree sibling)
	fmt.Printf("throwaway repo: %s\n\n", repoDir)

	// 2. Build the runtime. With no options and no ANTHROPIC_API_KEY/etc. set,
	//    this uses the keyless stub LLM — inference runs host-side either way.
	app := aisotropy.New()
	defer func() { _ = app.Close() }()

	// 3. Run one sandboxed task. SandboxToolScopes() grants the full hands set
	//    (run_shell / write_file / read_file / git_commit); pass a subset to
	//    restrict what the model may do. The provider key is NEVER passed here —
	//    there is no field for it, by design (the broker invariant).
	fmt.Println("running sandboxed task (host-side brain, container-side hands)...")
	res, err := app.RunTask(ctx, aisotropy.TaskRequest{
		RepoDir: repoDir,
		Prompt:  "Create a file greeting.txt containing the word 'hola', then commit it.",
		Scopes:  aisotropy.SandboxToolScopes(),
		OnLine:  func(line string) { fmt.Printf("  [sandbox stdout] %s\n", line) },
	})
	if err != nil {
		return fmt.Errorf(
			"RunTask failed (this demo requires Docker; is the docker daemon running?): %w", err)
	}

	// 4. Report what the run produced.
	printResult(res)
	return nil
}

// printResult renders the RunTask outcome, and is explicit about the stub case
// (zero commits) versus a real-model run (one or more commits merged to HEAD).
func printResult(res aisotropy.TaskResult) {
	fmt.Printf("\n=== result ===\n")
	fmt.Printf("steps taken: %d\n", len(res.Steps))
	for i, s := range res.Steps {
		if s.Tool == "" {
			fmt.Printf("  step %d: (model answered directly, no tool)\n", i+1)
			continue
		}
		fmt.Printf("  step %d: %s%v -> %s\n", i+1, s.Tool, s.Args, truncate(s.Result, 80))
	}

	if len(res.Commits) == 0 {
		fmt.Printf("\ncommits: 0\n")
		fmt.Println("The keyless stub produced no tool calls, so the sandboxed agent made no")
		fmt.Println("edits and there is nothing to commit. This is the honest, expected stub")
		fmt.Println("outcome: the full pipeline ran (worktree -> Docker container -> host-side")
		fmt.Println("loop -> discard, since there were no commits) and the container booted")
		fmt.Println("WITHOUT ever receiving a provider API key. Set a real provider key to see")
		fmt.Println("a model actually use the hands and commit — still key-free in the box.")
		return
	}

	fmt.Printf("\ncommits: %d on branch %s (merged into the repo's HEAD)\n", len(res.Commits), res.Branch)
	for _, c := range res.Commits {
		fmt.Printf("  %s\n", c)
	}
	if res.Output != "" {
		fmt.Printf("\nfinal output: %s\n", truncate(res.Output, 200))
	}
}

// newThrowawayRepo creates a fresh git repo with one initial commit under a new
// temp directory and returns the repo path.
func newThrowawayRepo() (string, error) {
	parent, err := os.MkdirTemp("", "aisotropy-task-agent-*")
	if err != nil {
		return "", err
	}
	repoDir := filepath.Join(parent, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		return "", err
	}

	// Seed a file so HEAD exists (a worktree needs a commit to branch off).
	readme := filepath.Join(repoDir, "README.md")
	if err := os.WriteFile(readme, []byte("# throwaway repo for the aisotropy task-agent demo\n"), 0o644); err != nil {
		return "", err
	}

	// Identity is supplied inline so the demo works on a host with no global git
	// config. `-b main` names the initial branch deterministically.
	steps := [][]string{
		{"init", "-b", "main"},
		{"add", "-A"},
		{"-c", "user.name=aisotropy-demo", "-c", "user.email=demo@local", "commit", "-m", "initial commit"},
	}
	for _, args := range steps {
		if out, err := gitIn(repoDir, args...); err != nil {
			return "", fmt.Errorf("git %v: %w\n%s", args, err, out)
		}
	}
	return repoDir, nil
}

// gitIn runs `git -C <dir> <args...>` and returns its combined output.
func gitIn(dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	return string(out), err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
