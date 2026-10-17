package taskagent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitCommitIgnoresInheritedGitEnvironment(t *testing.T) {
	victim := newGitRepo(t)
	before := gitOut(t, victim, "rev-parse", "HEAD")
	t.Setenv("GIT_DIR", filepath.Join(victim, ".git"))
	t.Setenv("GIT_WORK_TREE", victim)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(victim, ".git/index"))
	repo := newGitRepo(t)
	writeFileHost(t, repo, "note.txt", "target only")
	e := &sandboxExecutor{sb: &fakeSandbox{}, worktreeDir: repo}
	result, err := e.gitCommit(context.Background(), "add note")
	if err != nil || !strings.Contains(result, "committed") {
		t.Fatalf("target commit = %q, error = %v", result, err)
	}
	if got := gitOut(t, repo, "show", "HEAD:note.txt"); got != "target only" {
		t.Fatalf("target content = %q", got)
	}
	if after := gitOut(t, victim, "rev-parse", "HEAD"); after != before {
		t.Fatalf("victim HEAD changed: %q -> %q", before, after)
	}
	e.worktreeDir = t.TempDir()
	result, err = e.gitCommit(context.Background(), "msg")
	if err != nil || !strings.Contains(result, "git add failed") {
		t.Fatalf("non-repo commit = %q, error = %v", result, err)
	}
}
