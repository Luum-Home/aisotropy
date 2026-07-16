package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
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
		cmd := exec.Command("git", append([]string{"-C", wtDir}, args...)...)
		cmd.Env = append(os.Environ(),
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
