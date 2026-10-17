package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Luum-Home/aisotropy/internal/gitenv"
)

// TestBranchStrategyString_Default covers the fmt fallback arm for an
// out-of-range strategy value.
func TestBranchStrategyString_Default(t *testing.T) {
	if got := BranchStrategy(99).String(); got != "BranchStrategy(99)" {
		t.Errorf("String() = %q, want BranchStrategy(99)", got)
	}
}

// TestFinalize_UnknownStrategy covers Finalize's default arm: an unknown
// strategy is rejected with an error (after reading commits).
func TestFinalize_UnknownStrategy(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	w := Worktree{}

	add, err := w.Add(ctx, repo, "x")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	t.Cleanup(func() { _ = add.Cleanup() })

	if _, err := w.Finalize(ctx, repo, add.Path, add.Branch, add.Base, BranchStrategy(42)); err == nil {
		t.Fatal("expected an error for an unknown branch strategy")
	}
}

// TestFinalize_CommitsSinceError covers Finalize's error path when the worktree
// dir is not a git repo (rev-list fails).
func TestFinalize_CommitsSinceError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	w := Worktree{}
	notARepo := t.TempDir()
	if _, err := w.Finalize(context.Background(), notARepo, notARepo, "b", "base", MergeToHead); err == nil {
		t.Fatal("expected Finalize to fail on a non-repo worktree dir")
	}
}

// TestWorktree_AddResolveHeadError covers Add's revParse failure branch when
// repoDir is not a git repository.
func TestWorktree_AddResolveHeadError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	w := Worktree{}
	notARepo := t.TempDir()
	if _, err := w.Add(context.Background(), notARepo, "x"); err == nil {
		t.Fatal("expected Add to fail on a non-repo dir")
	}
}

// TestWorktree_PruneError covers Prune's error-wrapping branch on a non-repo.
func TestWorktree_PruneError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	w := Worktree{}
	if err := w.Prune(context.Background(), t.TempDir()); err == nil {
		t.Fatal("expected Prune to fail on a non-repo dir")
	}
}

// TestWorktree_CommitsSinceError covers CommitsSince's error-wrapping branch.
func TestWorktree_CommitsSinceError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	w := Worktree{}
	if _, err := w.CommitsSince(context.Background(), t.TempDir(), "base"); err == nil {
		t.Fatal("expected CommitsSince to fail on a non-repo dir")
	}
}

// TestWorktree_MergeConflict covers MergeToHead's error path: a real merge
// conflict surfaces as an error carrying git's stderr.
func TestWorktree_MergeConflict(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	w := Worktree{}

	add, err := w.Add(ctx, repo, "conflict")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	t.Cleanup(func() { _ = add.Cleanup() })

	// Edit README.md differently on the task branch and on base so a merge
	// conflicts on the same lines.
	commitInWorktree(t, add.Path, "README.md", "task-side change\n", "task edit")

	// Now commit a conflicting change on the base branch.
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base-side change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	baseRun := func(args ...string) {
		cmd := gitenv.Command(context.Background(), "git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(cmd.Env,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	baseRun("add", ".")
	baseRun("commit", "-m", "base edit")

	if err := w.MergeToHead(ctx, repo, add.Branch); err == nil {
		t.Fatal("expected a merge conflict error")
		// Leave the repo in a merged-conflict state; the temp dir is discarded.
	}
}

// TestWorktree_GitBinaryDefault covers the git() default-vs-override branches.
func TestWorktree_GitBinaryDefault(t *testing.T) {
	if (Worktree{}).git() != "git" {
		t.Error("default git() should be \"git\"")
	}
	if (Worktree{Git: "/custom/git"}).git() != "/custom/git" {
		t.Error("override git() should return the custom path")
	}
}
