// Command task-fanout is a minimal, runnable demonstration of Aisotropy's
// CONCURRENT sandboxed task agents — App.RunTasks, the parallel fan-out over the
// key-brokering RunTask primitive (ADR-0037, built on ADR-0036).
//
// What it shows, in one file:
//
//   - It builds an Aisotropy runtime with NO provider credential, so inference
//     falls back to the keyless *stub* model — host-side, as always.
//   - It creates a throwaway git repository in a temp dir (so the demo never
//     touches any real checkout) and points three tasks at that SAME repo.
//   - It calls app.RunTasks with a Concurrency limit, so the three tasks run in
//     parallel — each in its OWN isolated git worktree, its OWN Docker sandbox
//     container, and (the load-bearing property) with the provider API key NEVER
//     entering any container.
//   - It passes an OnEvent callback that prints a MULTIPLEXED live view: events
//     from all three concurrent tasks, each tagged with its task index, are
//     interleaved into one feed ("[task 1] shell: …", "[task 2] committed …").
//   - It prints the per-task outcomes, honest about the stub case.
//
// # Why concurrency does not weaken isolation
//
// RunTasks builds directly ON RunTask: every guarantee of a single task holds
// per task under the fan-out. Each task still gets its own worktree, its own
// container, and the key-broker invariant — the key is spent host-side and never
// crosses into any box. Concurrency changes none of that; the tasks are isolated
// by construction because each RunTask already is. The one shared resource is the
// repo's HEAD: all three tasks target the same repo, so RunTasks serializes just
// the per-repo git-metadata steps (worktree add, merge-to-HEAD) behind a per-repo
// lock while everything expensive — the containers, the host-side loops, the
// in-worktree commits — runs fully in parallel.
//
// # Honest note on the stub vs. a real model
//
// This demo runs with the keyless stub, which produces a canned response and NO
// actionable tool call. So each task completes gracefully with ZERO commits — the
// fan-out exercises the full concurrent plumbing (three parallel worktrees →
// three parallel containers → three host-side loops → serialized merge-or-discard)
// and proves every container boots without a key under concurrency, but the
// "agents" do not actually edit files, because there is no real model deciding
// to. Point Aisotropy at a real provider (export OPENROUTER_API_KEY /
// OPENAI_API_KEY / ANTHROPIC_API_KEY before running) and the same RunTasks call
// drives three real models in parallel that will use their hands to make edits
// and commit them — each merged into the shared HEAD one at a time, still without
// any key ever entering any container.
//
// # Requirements
//
//   - Docker (the sandbox provider shells out to the `docker` CLI). Without it,
//     each task fails at sandbox creation; RunTasks isolates those failures onto
//     the individual outcomes and this program prints them per task.
//   - git (used to build the throwaway repo here and by RunTask's worktree
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
		fmt.Fprintln(os.Stderr, "task-fanout:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// 1. Build a throwaway git repo so the demo never touches a real checkout.
	//    All three tasks target THIS repo, which is what exercises the per-repo
	//    merge serialization.
	repoDir, err := newThrowawayRepo()
	if err != nil {
		return fmt.Errorf("create throwaway repo: %w", err)
	}
	defer os.RemoveAll(filepath.Dir(repoDir)) // temp parent (holds repo + worktree siblings)
	fmt.Printf("throwaway repo: %s\n\n", repoDir)

	// 2. Build the runtime. With no options and no ANTHROPIC_API_KEY/etc. set,
	//    this uses the keyless stub LLM — inference runs host-side either way.
	app := aisotropy.New()
	defer func() { _ = app.Close() }()

	// 3. Three tasks against the SAME repo, each touching a DISJOINT file so that
	//    with a real model the three branches would merge cleanly into HEAD. The
	//    provider key is NEVER passed here — there is no field for it, by design.
	reqs := []aisotropy.TaskRequest{
		{
			RepoDir: repoDir,
			Prompt:  "Create a file a.txt containing the word 'alpha', then commit it.",
			Scopes:  aisotropy.SandboxToolScopes(),
		},
		{
			RepoDir: repoDir,
			Prompt:  "Create a file b.txt containing the word 'bravo', then commit it.",
			Scopes:  aisotropy.SandboxToolScopes(),
		},
		{
			RepoDir: repoDir,
			Prompt:  "Create a file c.txt containing the word 'charlie', then commit it.",
			Scopes:  aisotropy.SandboxToolScopes(),
		},
	}

	// 4. Fan the tasks out. Concurrency=2 caps how many containers run at once —
	//    with three tasks, at most two boot in parallel and the third takes a slot
	//    as one frees. OnEvent receives a live, index-tagged stream multiplexing
	//    all tasks; RunTasks serializes emission, so this printer need not be
	//    goroutine-safe.
	fmt.Println("fanning out 3 sandboxed tasks (Concurrency=2), host-side brains, container-side hands...")
	fmt.Println("(events below are interleaved live across the concurrent tasks)")
	fmt.Println()

	outcomes, err := app.RunTasks(ctx, reqs, aisotropy.RunTasksOptions{
		Concurrency: 2,
		OnEvent:     printEvent,
	})
	if err != nil {
		// A non-nil top-level error is a WHOLE-BATCH failure: the passed context was
		// cancelled (e.g. the 3-minute timeout fired). Individual task failures do
		// NOT land here — they ride on each outcome's Err (printed below).
		return fmt.Errorf("RunTasks batch cancelled: %w", err)
	}

	// 5. Report each task's outcome in input order.
	printOutcomes(outcomes)
	return nil
}

// printEvent renders one multiplexed TaskEvent from the concurrent fan-out. Kind
// is one of the documented event kinds; every line is tagged with the task index
// so the interleaved stream stays readable.
func printEvent(ev aisotropy.TaskEvent) {
	tag := fmt.Sprintf("[task %d]", ev.Index+1)
	switch ev.Kind {
	case "started":
		fmt.Printf("%s started\n", tag)
	case "step":
		fmt.Printf("%s step: %s -> %s\n", tag, ev.Tool, ev.Message)
	case "shell":
		fmt.Printf("%s shell: %s\n", tag, ev.Line)
	case "commit":
		fmt.Printf("%s committed %s\n", tag, ev.Message)
	case "done":
		fmt.Printf("%s done (%s)\n", tag, ev.Message)
	case "error":
		fmt.Printf("%s error: %s\n", tag, ev.Message)
	default:
		fmt.Printf("%s %s: %s\n", tag, ev.Kind, ev.Message)
	}
}

// printOutcomes renders the per-task results, explicit about the stub case (zero
// commits on every task) versus a real-model run (commits merged to HEAD), and
// about per-task error isolation (one task's failure never aborts the others).
func printOutcomes(outcomes []aisotropy.TaskOutcome) {
	fmt.Printf("\n=== outcomes (%d tasks, input order) ===\n", len(outcomes))

	totalCommits := 0
	failures := 0
	for _, o := range outcomes {
		tag := fmt.Sprintf("task %d", o.Index+1)
		if o.Err != nil {
			failures++
			// This is an INDIVIDUAL task failure captured on its own outcome — the
			// batch still ran every other task. The most common cause here is Docker
			// not being available; RunTasks does not let that abort the siblings.
			fmt.Printf("  %s: FAILED (isolated, batch continued): %v\n", tag, o.Err)
			continue
		}
		totalCommits += len(o.Result.Commits)
		if len(o.Result.Commits) == 0 {
			fmt.Printf("  %s: 0 commits, %d steps (keyless stub: no tool calls, nothing to commit)\n",
				tag, len(o.Result.Steps))
			continue
		}
		fmt.Printf("  %s: %d commit(s) on branch %s (merged into HEAD), %d steps\n",
			tag, len(o.Result.Commits), o.Result.Branch, len(o.Result.Steps))
		for _, c := range o.Result.Commits {
			fmt.Printf("      %s\n", c)
		}
	}

	fmt.Printf("\ntotals: %d commit(s) across %d task(s), %d isolated failure(s)\n",
		totalCommits, len(outcomes), failures)
	if totalCommits == 0 && failures == 0 {
		fmt.Println("Every task ran the full concurrent pipeline (own worktree -> own Docker")
		fmt.Println("container -> host-side loop -> discard, since the stub produced no commits)")
		fmt.Println("and every container booted WITHOUT ever receiving a provider API key. Set a")
		fmt.Println("real provider key to see three models actually use their hands and commit in")
		fmt.Println("parallel — each merged into the shared HEAD one at a time, still key-free.")
	}
}

// newThrowawayRepo creates a fresh git repo with one initial commit under a new
// temp directory and returns the repo path.
func newThrowawayRepo() (string, error) {
	parent, err := os.MkdirTemp("", "aisotropy-task-fanout-*")
	if err != nil {
		return "", err
	}
	repoDir := filepath.Join(parent, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		return "", err
	}

	// Seed a file so HEAD exists (a worktree needs a commit to branch off).
	readme := filepath.Join(repoDir, "README.md")
	if err := os.WriteFile(readme, []byte("# throwaway repo for the aisotropy task-fanout demo\n"), 0o644); err != nil {
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
