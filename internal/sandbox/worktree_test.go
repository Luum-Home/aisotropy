package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/gitenv"
)

// initRepo builds a throwaway git repo with one commit and returns its dir. It
// t.Skips when git is not installed — the worktree helper is git-only, and we
// keep the honest "skip, don't fake" posture.
func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping worktree tests")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := gitenv.Command(context.Background(), "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(cmd.Env,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "seed")
	return dir
}

// commitInWorktree writes a file in the worktree and commits it, returning the
// new HEAD sha.
func commitInWorktree(t *testing.T, wtDir, file, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wtDir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := gitenv.Command(context.Background(), "git", append([]string{"-C", wtDir}, args...)...)
		cmd.Env = append(cmd.Env,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("add", ".")
	run("commit", "-m", msg)
}

func TestWorktree_AddCommitMergeRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	w := Worktree{}

	add, err := w.Add(ctx, repo, "my-task")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	t.Cleanup(func() { _ = add.Cleanup() })

	// The worktree dir exists, is a sibling of the repo, and carries the seed.
	if fi, err := os.Stat(add.Path); err != nil || !fi.IsDir() {
		t.Fatalf("worktree path %q not a dir: %v", add.Path, err)
	}
	if !strings.HasPrefix(add.Branch, "aisotropy/task/") {
		t.Fatalf("branch %q lacks aisotropy/task/ prefix", add.Branch)
	}
	if !strings.HasSuffix(add.Branch, "-my-task") {
		t.Fatalf("branch %q did not fold in the name slug", add.Branch)
	}
	if _, err := os.Stat(filepath.Join(add.Path, "README.md")); err != nil {
		t.Fatalf("seed file missing in worktree: %v", err)
	}

	// A run makes two commits inside the isolated worktree.
	commitInWorktree(t, add.Path, "a.txt", "one\n", "add a")
	commitInWorktree(t, add.Path, "b.txt", "two\n", "add b")

	// CommitsSince enumerates exactly those two.
	shas, err := w.CommitsSince(ctx, add.Path, add.Base)
	if err != nil {
		t.Fatalf("CommitsSince: %v", err)
	}
	if len(shas) != 2 {
		t.Fatalf("expected 2 commits since base, got %d: %v", len(shas), shas)
	}

	// The base branch does not have the run's files yet.
	if _, err := os.Stat(filepath.Join(repo, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected a.txt absent on base before merge, err=%v", err)
	}

	// Merge the task branch to HEAD; the files appear on the base checkout.
	if err := w.MergeToHead(ctx, repo, add.Branch); err != nil {
		t.Fatalf("MergeToHead: %v", err)
	}
	for _, f := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(repo, f)); err != nil {
			t.Fatalf("expected %s on base after merge: %v", f, err)
		}
	}
}

func TestWorktree_CleanupRemovesWorktree(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	w := Worktree{}

	add, err := w.Add(ctx, repo, "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := os.Stat(add.Path); err != nil {
		t.Fatalf("worktree should exist: %v", err)
	}
	if err := add.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(add.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree dir should be gone after Cleanup, err=%v", err)
	}
	// Prune is best-effort and must not error on a clean repo.
	if err := w.Prune(ctx, repo); err != nil {
		t.Fatalf("Prune: %v", err)
	}
}

// branchExists reports whether refs/heads/<branch> resolves in repoDir.
func branchExists(t *testing.T, repoDir, branch string) bool {
	t.Helper()
	cmd := gitenv.Command(context.Background(), "git", "-C", repoDir, "rev-parse", "--verify", "refs/heads/"+branch)
	return cmd.Run() == nil
}

func TestFinalize_MergeToHeadMerges(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	w := Worktree{}

	add, err := w.Add(ctx, repo, "merge-task")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	t.Cleanup(func() { _ = add.Cleanup() })

	commitInWorktree(t, add.Path, "a.txt", "one\n", "add a")

	res, err := w.Finalize(ctx, repo, add.Path, add.Branch, add.Base, MergeToHead)
	if err != nil {
		t.Fatalf("Finalize(MergeToHead): %v", err)
	}
	if !res.Merged {
		t.Fatalf("expected Merged=true, got %+v", res)
	}
	if res.Strategy != MergeToHead {
		t.Fatalf("Strategy = %v, want MergeToHead", res.Strategy)
	}
	if len(res.Commits) != 1 {
		t.Fatalf("expected 1 commit, got %d: %v", len(res.Commits), res.Commits)
	}
	// The run's file is now on the base checkout.
	if _, err := os.Stat(filepath.Join(repo, "a.txt")); err != nil {
		t.Fatalf("expected a.txt on base after merge-to-head: %v", err)
	}
}

func TestFinalize_MergeToHeadNoCommitsIsNoop(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	w := Worktree{}

	add, err := w.Add(ctx, repo, "empty-task")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	t.Cleanup(func() { _ = add.Cleanup() })

	// No commits in the worktree — Finalize should be a no-op merge.
	res, err := w.Finalize(ctx, repo, add.Path, add.Branch, add.Base, MergeToHead)
	if err != nil {
		t.Fatalf("Finalize(MergeToHead, empty): %v", err)
	}
	if res.Merged {
		t.Fatalf("expected Merged=false for an empty branch, got %+v", res)
	}
	if len(res.Commits) != 0 {
		t.Fatalf("expected 0 commits, got %v", res.Commits)
	}
}

func TestFinalize_ReturnBranchLeavesUnmerged(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	w := Worktree{}

	add, err := w.Add(ctx, repo, "review-task")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	t.Cleanup(func() { _ = add.Cleanup() })

	commitInWorktree(t, add.Path, "a.txt", "one\n", "add a")
	commitInWorktree(t, add.Path, "b.txt", "two\n", "add b")

	res, err := w.Finalize(ctx, repo, add.Path, add.Branch, add.Base, ReturnBranch)
	if err != nil {
		t.Fatalf("Finalize(ReturnBranch): %v", err)
	}
	if res.Merged {
		t.Fatalf("ReturnBranch must never merge, got Merged=true: %+v", res)
	}
	if res.Branch != add.Branch {
		t.Fatalf("returned branch = %q, want %q", res.Branch, add.Branch)
	}
	if len(res.Commits) != 2 {
		t.Fatalf("expected 2 commits reported, got %d: %v", len(res.Commits), res.Commits)
	}
	// The branch is still present and NOT merged into the base checkout.
	if !branchExists(t, repo, add.Branch) {
		t.Fatalf("expected branch %q to still exist", add.Branch)
	}
	if _, err := os.Stat(filepath.Join(repo, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected a.txt absent on base (unmerged), err=%v", err)
	}
}

// NamedBranch is the alias for ReturnBranch and must behave identically.
func TestFinalize_NamedBranchAlias(t *testing.T) {
	if NamedBranch != ReturnBranch {
		t.Fatalf("NamedBranch (%v) must alias ReturnBranch (%v)", NamedBranch, ReturnBranch)
	}
	if MergeToHead.String() != "merge-to-head" || ReturnBranch.String() != "return-branch" {
		t.Fatalf("unexpected strategy strings: %q / %q", MergeToHead, ReturnBranch)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"My Task":        "my-task",
		"  hello  world": "hello-world",
		"a/b\\c":         "a-b-c",
		"UPPER":          "upper",
		"":               "",
		"---":            "",
		"café":           "caf",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
