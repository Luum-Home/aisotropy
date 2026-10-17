package sandbox

import (
	"context"
	"path/filepath"
	"testing"
)

func TestWorktreeIgnoresInheritedGitEnvironment(t *testing.T) {
	victim := initRepo(t)
	w := Worktree{}
	before, err := w.revParse(context.Background(), victim, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", filepath.Join(victim, ".git"))
	t.Setenv("GIT_WORK_TREE", victim)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(victim, ".git/index"))
	repo := initRepo(t) // Exercise the fixture helper under hook-like poisoning too.
	add, err := w.Add(context.Background(), repo, "environment-isolation")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = add.Cleanup() })
	commitInWorktree(t, add.Path, "note", "target only\n", "add note")
	commits, err := w.CommitsSince(context.Background(), add.Path, add.Base)
	if err != nil || len(commits) != 1 {
		t.Fatalf("target commits = %v, error = %v", commits, err)
	}
	if err := w.MergeToHead(context.Background(), repo, add.Branch); err != nil {
		t.Fatal(err)
	}
	if after, err := w.revParse(context.Background(), victim, "HEAD"); err != nil || after != before {
		t.Fatalf("victim HEAD changed: %q -> %q (%v)", before, after, err)
	}
	if _, err := w.Add(context.Background(), t.TempDir(), "not-a-repo"); err == nil {
		t.Fatal("inherited Git context made a non-repository usable")
	}
}
